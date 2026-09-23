package core

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ctxstore"
)

// TestRunRefusesSecondRunner: once a task's row is running, a second run()
// invocation (a duplicated resume or context_ack racing in) must not spawn a
// duplicate executor — and must not disturb the row the first runner owns.
func TestRunRefusesSecondRunner(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "exec", "127.0.0.1:18002")
	tk := createTask(t, c.store, "", "dup-run", c.nodeID)
	if err := c.store.Queue(ctx, tk.TaskID, c.nodeID); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.Dispatch(ctx, tk.TaskID, c.nodeID, c.nodeID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := c.store.Accept(ctx, tk.TaskID, c.nodeID); err != nil {
		t.Fatalf("accept: %v", err)
	}

	_, err := c.run(ctx, tk.TaskID, "intent", []string{"sys:info"}, nil)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("run on a running task = %v, want ErrAlreadyRunning", err)
	}
	// ErrAlreadyRunning rides the ErrCancelled checks so callers stay quiet.
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("ErrAlreadyRunning must satisfy errors.Is(err, ErrCancelled)")
	}
	// The live row belongs to the first runner: still running, never failed
	// by the duplicate's error path.
	fresh, gerr := c.store.Get(ctx, tk.TaskID)
	if gerr != nil {
		t.Fatalf("get: %v", gerr)
	}
	if fresh.State != StateRunning {
		t.Fatalf("task state = %s after the duplicate run, want %s", fresh.State, StateRunning)
	}
}

// TestResultChainMustEndAtSender: a result that arrives for a task this node
// has no row for gets its history reconstructed from the echoed chain — so
// that chain must end at the authenticated sender, exactly like the
// delegation-time check, or a peer could launder a fabricated upstream path
// into the task record.
func TestResultChainMustEndAtSender(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "origin", "127.0.0.1:18003")

	forged, err := bus.NewEnvelope(bus.MsgTaskResult, "mallory", "r-forged", bus.TaskResultPayload{
		TaskID: "t-forged", AttemptID: "a1", State: StateDone, OK: true,
		Chain: []string{"origin", "innocent-relay"},
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	c.handleResult(ctx, forged)
	if _, err := c.store.Get(ctx, "t-forged"); err == nil {
		t.Fatal("a result whose chain does not end at the sender created a task row")
	}

	// Positive control: a chain ending at the sender is adopted, as the
	// Phase-0 reconstruction path intends.
	legit, err := bus.NewEnvelope(bus.MsgTaskResult, "worker", "r-legit", bus.TaskResultPayload{
		TaskID: "t-legit", AttemptID: "a2", State: StateDone, OK: true,
		Chain: []string{"origin", "worker"},
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	c.handleResult(ctx, legit)
	tk, err := c.store.Get(ctx, "t-legit")
	if err != nil {
		t.Fatalf("legit result did not reconstruct the row: %v", err)
	}
	if tk.State != StateDone {
		t.Fatalf("reconstructed row state = %s, want %s", tk.State, StateDone)
	}
}

// TestDuplicateContextAckResumesOnce: a context_ack racing a copy of itself
// (re-sent over a flapping connection) must consume the parked entry exactly
// once. The release func is intentionally not once-guarded here so a second
// claimant — which under the old Load-then-Delete window would also have
// reached the resume path — shows up as a count above one.
func TestDuplicateContextAckResumesOnce(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "exec", "127.0.0.1:18005")
	tk := setupDispatchedTask(t, c, "t-ack-race", "exec")
	if err := c.store.SetWaitingContext(ctx, tk.TaskID, c.nodeID); err != nil {
		t.Fatalf("waiting: %v", err)
	}
	var releases atomic.Int32
	c.pendingCtx.Store(tk.TaskID, &pendingContext{
		intent: "x", ctxType: "file", source: "context-src",
		release: func() { releases.Add(1) },
	})

	data := []byte("snapshot")
	const n = 32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			env, err := bus.NewEnvelope(bus.MsgContextAck, "context-src",
				fmt.Sprintf("ack-%d", i), bus.ContextAckPayload{
					TaskID: tk.TaskID, Hash: ctxstore.Hash(data), OK: true, Data: data,
				})
			if err != nil {
				t.Errorf("envelope: %v", err)
				return
			}
			c.handleContextAck(ctx, env)
		}(i)
	}
	wg.Wait()

	if got := releases.Load(); got != 1 {
		t.Fatalf("pending entry claimed %d times across duplicate acks, want 1", got)
	}
	if _, ok := c.pendingCtx.Load(tk.TaskID); ok {
		t.Fatal("pending entry survived the ack")
	}
}

// TestDelegateTimeoutClampedToLease: the wire timeout becomes the executor's
// own lease, so it is bounded by the silence window this node grants itself.
// An arbitrary sender value would let a dead delegator's orphan hold an
// execution slot for as long as it named.
func TestDelegateTimeoutClampedToLease(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "exec", "127.0.0.1:18004")

	env, err := bus.NewEnvelope(bus.MsgTaskDelegate, "parent", "d-timeout", bus.TaskDelegatePayload{
		TaskID:    "t-timeout",
		Title:     "clamped lease",
		Intent:    "x",
		Requires:  []string{"ability:nowhere"},
		Chain:     []string{"parent"},
		TimeoutMS: math.MaxInt64 / 2,
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	c.handleDelegate(ctx, env)

	tk, err := c.store.Get(ctx, "t-timeout")
	if err != nil {
		t.Fatalf("delegated task missing: %v", err)
	}
	if tk.LeaseExpires <= 0 {
		t.Fatal("wire timeout was not stamped on the row")
	}
	bound := time.Now().Unix() + int64(c.lease().Seconds()) + 5
	if tk.LeaseExpires > bound {
		t.Fatalf("wire timeout produced lease expiry %d, beyond own lease bound %d",
			tk.LeaseExpires, bound)
	}
}

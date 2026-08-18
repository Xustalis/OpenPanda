package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// startChain boots a linear three-node chain root → mid → leaf over real
// WebSocket on loopback and exchanges capability cards. root knows only mid;
// mid knows both root and leaf; leaf knows only mid. This exercises the
// sub-scheduler forward path: root forwards to mid (no match, tier>1), mid
// forwards to leaf (matches gpio:read).
func startChain(t *testing.T, ctx context.Context, rootAddr, midAddr, leafAddr string) (root, mid, leaf *Core) {
	t.Helper()
	root = newCore(t, "opi3b", rootAddr)
	mid = newCore(t, "mac", midAddr)
	leaf = newCoreWithNative(t, "windows", leafAddr, ledger.NativeAbility{
		ID: "gpio:read", Command: "echo", Args: []string{"gpio-ok"},
	})

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("startup: %v", err)
		}
	}
	must(root.Register(ctx))
	must(mid.Register(ctx))
	must(leaf.Register(ctx))

	go func() { _ = root.Listen(ctx, rootAddr) }()
	go func() { _ = mid.Listen(ctx, midAddr) }()
	go func() { _ = leaf.Listen(ctx, leafAddr) }()
	time.Sleep(200 * time.Millisecond)

	// root → mid and mid → leaf, so the chain is linear.
	must(root.DialPeer(ctx, midAddr))
	must(mid.DialPeer(ctx, leafAddr))
	time.Sleep(300 * time.Millisecond)
	return root, mid, leaf
}

// TestThreeNodeForward is the Sprint 2.1 end-to-end: the root schedules a
// task it cannot run, the middle node forwards it to a capable leaf, the leaf
// executes, and the result flows back up the chain. Every node's copy of the
// task must end done, and the root must hold a non-empty result.
func TestThreeNodeForward(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	root, mid, leaf := startChain(t, ctx, "127.0.0.1:17901", "127.0.0.1:17902", "127.0.0.1:17903")

	in := TaskInput{Title: "read gpio", Intent: "read gpio", Requires: []string{"gpio:read"}}
	task, result, err := root.Submit(ctx, in)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !result.OK {
		t.Fatalf("result not ok: %+v", result)
	}
	if task.State != StateDone {
		t.Fatalf("root task state = %s, want done", task.State)
	}
	if task.ResultJSON == "" {
		t.Fatalf("root task result_json empty")
	}
	if !strings.Contains(result.Stdout, "gpio-ok") {
		t.Fatalf("result stdout = %q, want gpio-ok", result.Stdout)
	}

	// Both intermediate and leaf copies must have completed too.
	if tk, err := mid.store.Get(ctx, task.TaskID); err != nil || tk.State != StateDone {
		t.Fatalf("mid task state = %v (err %v), want done", tk, err)
	}
	if tk, err := leaf.store.Get(ctx, task.TaskID); err != nil || tk.State != StateDone {
		t.Fatalf("leaf task state = %v (err %v), want done", tk, err)
	}
}

// TestResultRelay verifies the result of a forwarded task propagates through
// the middle node to the root carrying the same attempt_id, so the root is not
// fooled into treating it as stale. It asserts the middle node's copy reached
// done via the relayed result (not local execution) — its native abilities do
// not include gpio:read.
func TestResultRelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	root, mid, _ := startChain(t, ctx, "127.0.0.1:17921", "127.0.0.1:17922", "127.0.0.1:17923")

	in := TaskInput{Title: "relay", Intent: "relay", Requires: []string{"gpio:read"}}
	task, result, err := root.Submit(ctx, in)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !result.OK {
		t.Fatalf("result not ok: %+v", result)
	}

	midTask, err := mid.store.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("mid get: %v", err)
	}
	if midTask.State != StateDone {
		t.Fatalf("mid task state = %s, want done via relay", midTask.State)
	}
	if midTask.AttemptID != task.AttemptID {
		t.Fatalf("mid attempt_id = %q, want root %q (attempt must survive relay)", midTask.AttemptID, task.AttemptID)
	}
}

// TestLoopGuard verifies a delegate whose chain already contains the receiver
// is rejected with a task_decline ("delegation loop") instead of being
// re-created, and that the decline bounces the parent's task back to queued.
func TestLoopGuard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	entry := newCore(t, "entry-loop", "127.0.0.1:17911")
	worker := newCore(t, "worker-loop", "127.0.0.1:17912")
	startPair(t, ctx, entry, worker, "127.0.0.1:17911", "127.0.0.1:17912")

	// Pre-create and dispatch a task at entry so the decline is observable as
	// a dispatched → queued transition.
	tk, err := entry.store.Create(ctx, "", "proj", "loop", "entry-loop", []string{"entry-loop"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, f := range []func() error{
		func() error { return entry.store.Queue(ctx, tk.TaskID, "entry-loop") },
		func() error { return entry.store.Dispatch(ctx, tk.TaskID, "entry-loop", "worker-loop") },
	} {
		if err := f(); err != nil {
			t.Fatalf("setup state: %v", err)
		}
	}

	// A delegate whose chain already contains the worker is a routing loop.
	env, _ := bus.NewEnvelope(bus.MsgTaskDelegate, "entry-loop", "m1", bus.TaskDelegatePayload{
		TaskID: tk.TaskID, Title: "loop", Intent: "x", Requires: []string{"sys:info"},
		Chain: []string{"entry-loop", "worker-loop"},
	})
	if err := entry.sendTo("worker-loop", env); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Wait for the decline to bounce the entry task back to queued.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := entry.store.Get(ctx, tk.TaskID)
		if err == nil && cur.State == StateQueued {
			// The worker must not have created the looped task.
			if _, werr := worker.store.Get(ctx, tk.TaskID); werr == nil {
				t.Fatalf("worker should not have created the looped task")
			}
			// The decline must be recorded as a loop, not some other cause.
			evs, _ := entry.store.Events(ctx, tk.TaskID)
			for _, e := range evs {
				if e.Type == EvDecline && strings.Contains(e.DataJSON, "delegation loop") {
					return
				}
			}
			t.Fatalf("decline recorded but reason is not 'delegation loop': %+v", evs)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("entry task did not return to queued (decline) within deadline")
}

// TestDeclineTerminalizesLocalRow verifies a node that creates a task row and
// then declines it (no capability match) moves its local row to a terminal state
// instead of leaving it submitted/dispatched as an orphan (D2).
func TestDeclineTerminalizesLocalRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	entry := newCore(t, "entry-dec", "127.0.0.1:17941")
	// worker has only sys:info, so a gpio:read delegate is declined.
	worker := newCore(t, "worker-dec", "127.0.0.1:17942")
	startPair(t, ctx, entry, worker, "127.0.0.1:17941", "127.0.0.1:17942")

	env, _ := bus.NewEnvelope(bus.MsgTaskDelegate, "entry-dec", "m1", bus.TaskDelegatePayload{
		TaskID: "dec-task", Title: "t", Intent: "x", Requires: []string{"gpio:read"},
		Chain: []string{"entry-dec"},
	})
	if err := entry.sendTo("worker-dec", env); err != nil {
		t.Fatalf("delegate: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tk, err := worker.store.Get(ctx, "dec-task")
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if tk.State == StateCancelled {
			return // declined task was terminalized, not left orphaned
		}
		if tk.State == StateDone || tk.State == StateFailed || tk.State == StateExpired {
			t.Fatalf("declined task reached unexpected terminal state %s", tk.State)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("worker did not terminalize the declined task within deadline")
}

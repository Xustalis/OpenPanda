package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// TestSubmitLocalRunsNative verifies the local entry loop end-to-end: create a
// task with detail, execute it via a native ability, and observe done + result.
func TestSubmitLocalRunsNative(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithNative(t, "local-native", "", ledger.NativeAbility{
		ID: "sys:echo", Command: "echo", Args: []string{"hello"},
	})

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "echo hello",
		Project:     "proj",
		ContextType: "command",
		Intent:      "echo hello",
		SpecJSON:    `{"scope":"stdout","target":"hello"}`,
		Requires:    []string{"sys:echo"},
		Complexity:  0.1,
		Risk:        "low",
	})
	if err != nil {
		t.Fatalf("submit local: %v", err)
	}
	if task.State != StateDone {
		t.Fatalf("state = %s, want done", task.State)
	}
	if !result.OK {
		t.Fatalf("result not ok: %+v", result)
	}
	if result.Stdout != "hello\n" {
		t.Fatalf("stdout = %q, want %q", result.Stdout, "hello\n")
	}

	// Detail must round-trip through the store.
	got, err := c.store.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ContextType != "command" || got.Intent != "echo hello" || got.Complexity != 0.1 || got.Risk != "low" {
		t.Fatalf("detail mismatch: %+v", got)
	}
	if got.SpecJSON != `{"scope":"stdout","target":"hello"}` {
		t.Fatalf("spec = %q", got.SpecJSON)
	}
}

// TestSubmitLocalNoCapability verifies a local task with no matching ability
// is failed (terminal), not left stuck in the queue.
func TestSubmitLocalNoCapability(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "local-none", "")

	task, _, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "unroutable",
		ContextType: "command",
		Intent:      "unroutable",
		Requires:    []string{"sys:missing"},
	})
	if err == nil {
		t.Fatalf("expected error for unroutable task")
	}
	got, gerr := c.store.Get(ctx, task.TaskID)
	if gerr != nil {
		t.Fatalf("get: %v", gerr)
	}
	if got.State != StateFailed {
		t.Fatalf("state = %s, want failed", got.State)
	}
}

// TestSetDetailRoundTrip verifies SetDetail writes and Get reads all six fields.
func TestSetDetailRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := createTask(t, s, "", "detail", "root")

	if err := s.SetDetail(ctx, tk.TaskID, TaskDetail{
		ContextType: "file", Intent: "refactor", SpecJSON: `{"scope":"a.go"}`,
		Complexity: 0.7, Risk: "high", ResourceJSON: `{"cpu":4}`,
	}); err != nil {
		t.Fatalf("set detail: %v", err)
	}

	got, err := s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ContextType != "file" || got.Intent != "refactor" || got.Complexity != 0.7 ||
		got.Risk != "high" || got.ResourceJSON != `{"cpu":4}` || got.SpecJSON != `{"scope":"a.go"}` {
		t.Fatalf("detail mismatch: %+v", got)
	}
}

// TestSubmitKeepsFileTaskLocalWhenCapable verifies that when a task has ContextType "file"
// and no distributed project (Project == ""), Submit executes it locally if the local node
// matches the required ability, preventing it from being forwarded to a foreign node without files.
func TestSubmitKeepsFileTaskLocalWhenCapable(t *testing.T) {
	ctx := context.Background()
	localEcho := ledger.NativeAbility{ID: "code:edit", Command: "echo", Args: []string{"local done"}}
	c := newCoreWithNative(t, "local-node", "", localEcho)

	// Register a peer in ledger that has much higher capacity, which would otherwise win the score
	peerCard := ledger.Card{
		Device:        "remote-supercomputer",
		ResourceClass: "Full",
		Native: []ledger.NativeAbility{
			{ID: "code:edit", Command: "echo", Args: []string{"remote done"}},
		},
		Capacity: ledger.Capacity{CPUCores: 128, RAMGB: 512, MaxConcurrent: 50},
	}
	if err := ledger.Register(c.db, peerCard, "remote-supercomputer", 1); err != nil {
		t.Fatalf("register peer: %v", err)
	}
	capJSON, _ := json.Marshal(peerCard.Capacity)
	if err := ledger.Heartbeat(c.db, "remote-supercomputer", "online", string(capJSON)); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	task, result, err := c.Submit(ctx, TaskInput{
		Title:       "edit local code",
		ContextType: "file",
		Project:     "",
		Intent:      "edit something locally",
		Requires:    []string{"code:edit"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.State != StateDone {
		t.Fatalf("state = %s, want done", task.State)
	}
	if result.Stdout != "local done\n" {
		t.Fatalf("expected local execution, got stdout: %q", result.Stdout)
	}
}

func TestSubmitFallsBackToDTNWhenTargetNonLive(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "local-node", "")

	peerCard := ledger.Card{
		Device:        "remote-worker",
		ResourceClass: "Full",
		Native: []ledger.NativeAbility{
			{ID: "gpu:train", Command: "true"},
		},
		Capacity: ledger.Capacity{CPUCores: 64, RAMGB: 256, MaxConcurrent: 10},
	}
	if err := ledger.Register(c.db, peerCard, "remote-worker", 1); err != nil {
		t.Fatalf("register peer: %v", err)
	}
	capJSON, _ := json.Marshal(peerCard.Capacity)
	if err := ledger.Heartbeat(c.db, "remote-worker", "online", string(capJSON)); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	// Submit task requiring gpu:train - target has no live WebSocket connection
	task, result, err := c.Submit(ctx, TaskInput{
		Title:    "train model",
		Intent:   "train",
		Requires: []string{"gpu:train"},
	})
	if err != nil {
		t.Fatalf("submit should succeed with async DTN queueing, got: %v", err)
	}
	if task.State != StateQueued {
		t.Fatalf("task state = %s, want %s (queued for DTN)", task.State, StateQueued)
	}
	if result.State != StateQueued {
		t.Fatalf("result state = %s, want %s", result.State, StateQueued)
	}
}

// TestWaitRemoteResultHonoursRenewedLease is the §7.2 regression: a healthy
// executor keeps refreshing lease_expires_at on the origin's row, so the wait
// must outlive the deadline stamped when the wait began — the old fixed
// time.After(c.lease()) fired at wait-start+lease regardless and mis-killed a
// long task that was provably alive.
func TestWaitRemoteResultHonoursRenewedLease(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "origin", "")
	c.mu.Lock()
	c.leaseTimeout = 900 * time.Millisecond
	c.mu.Unlock()

	tk := createTask(t, c.store, "", "long work", c.nodeID)
	if err := c.store.SetLease(ctx, tk.TaskID, 1000); err != nil {
		t.Fatalf("stamp lease: %v", err)
	}

	// Executor-side liveness: renew the origin row's deadline every 200ms for
	// ~1.4s — well past both the stamped deadline and the fixed-timer bound.
	stop := make(chan struct{})
	go func() {
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				_ = c.store.SetLease(context.Background(), tk.TaskID, 1000)
			}
		}
	}()
	defer close(stop)

	ch := make(chan bus.TaskResultPayload, 1)
	go func() {
		time.Sleep(1400 * time.Millisecond)
		ch <- bus.TaskResultPayload{TaskID: tk.TaskID, State: StateDone, OK: true}
	}()

	final, res, err := c.waitRemoteResult(ctx, tk, ch, "delegation")
	if err != nil {
		t.Fatalf("renewed task must not time out: %v", err)
	}
	if !res.OK || final.State != StateDone && res.State != StateDone {
		t.Fatalf("result = %+v state=%s, want done", res, final.State)
	}
}

// TestWaitRemoteResultExpiresLapsedLease verifies the other half: once the
// stamped deadline actually passes (no beats), the wait fails the row and
// returns a timeout — a dead executor cannot wedge the caller either.
func TestWaitRemoteResultExpiresLapsedLease(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "origin", "")
	c.mu.Lock()
	c.leaseTimeout = 900 * time.Millisecond
	c.mu.Unlock()

	tk := createTask(t, c.store, "", "dead executor", c.nodeID)
	if err := c.store.SetLease(ctx, tk.TaskID, 1000); err != nil {
		t.Fatalf("stamp lease: %v", err)
	}

	ch := make(chan bus.TaskResultPayload)
	start := time.Now()
	_, _, err := c.waitRemoteResult(ctx, tk, ch, "delegation")
	if err == nil || !strings.Contains(err.Error(), "delegation timeout") {
		t.Fatalf("err = %v, want delegation timeout", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("wait took %s, should have observed the lapsed lease quickly", d)
	}
	got, gerr := c.store.Get(ctx, tk.TaskID)
	if gerr != nil {
		t.Fatalf("get: %v", gerr)
	}
	if got.State != StateFailed {
		t.Fatalf("state = %s, want failed after lease lapse", got.State)
	}
}

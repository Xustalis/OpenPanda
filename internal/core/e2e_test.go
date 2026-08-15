package core

import (
	"context"
	"testing"
	"time"

	"github.com/xenith/panda/internal/bus"
	"github.com/xenith/panda/internal/config"
	"github.com/xenith/panda/internal/ledger"
)

// TestDelegateIdempotent sends the same task_delegate twice; the second must
// be ignored (P0-40).
func TestDelegateIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCore(t, "entry-idem", "127.0.0.1:17846")
	worker := newCore(t, "worker-idem", "127.0.0.1:17847")
	startPair(t, ctx, entry, worker, "127.0.0.1:17846", "127.0.0.1:17847")

	env, _ := bus.NewEnvelope(bus.MsgTaskDelegate, "entry-idem", "m1", bus.TaskDelegatePayload{
		TaskID: "idem-task", Title: "t", Intent: "x", Requires: []string{"sys:info"},
		Chain: []string{"entry-idem"},
	})
	if err := entry.sendTo("worker-idem", env); err != nil {
		t.Fatalf("send 1: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Re-send the same task_id with a different msg_id.
	env2, _ := bus.NewEnvelope(bus.MsgTaskDelegate, "entry-idem", "m2", bus.TaskDelegatePayload{
		TaskID: "idem-task", Title: "t", Intent: "x", Requires: []string{"sys:info"},
		Chain: []string{"entry-idem"},
	})
	if err := entry.sendTo("worker-idem", env2); err != nil {
		t.Fatalf("send 2: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	tasks, err := worker.store.ListByState(ctx, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	done := 0
	for _, tk := range tasks {
		if tk.TaskID == "idem-task" && tk.State == StateDone {
			done++
		}
	}
	if done != 1 {
		t.Fatalf("expected exactly 1 done task, got %d", done)
	}
}

// TestTaskTimeoutFails verifies a running task with an expired lease becomes
// failed (P0-36).
func TestTaskTimeoutFails(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := createTask(t, s, "", "slow", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(ctx, tk.TaskID, "root"))
	must(s.Dispatch(ctx, tk.TaskID, "root", "root"))
	must(s.Accept(ctx, tk.TaskID, "root"))
	// Stamp an already-expired lease directly (SetLease ignores non-positive).
	now := s.now()
	if _, err := s.db.Exec(`UPDATE tasks SET lease_expires_at=? WHERE task_id=?`, now-1, tk.TaskID); err != nil {
		t.Fatalf("stamp lease: %v", err)
	}

	n, err := s.ExpireTasks(ctx)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 expired, got %d", n)
	}
	got, _ := s.Get(ctx, tk.TaskID)
	if got.State != StateFailed {
		t.Fatalf("state = %s, want failed", got.State)
	}
}

// TestRecoverRestoresState verifies a restart normalizes interrupted tasks
// (P0-39).
func TestRecoverRestoresState(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	running := createTask(t, s, "", "running", "root")
	dispatched := createTask(t, s, "", "dispatched", "root")
	done := createTask(t, s, "", "done", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(ctx, running.TaskID, "root"))
	must(s.Dispatch(ctx, running.TaskID, "root", "root"))
	must(s.Accept(ctx, running.TaskID, "root"))

	must(s.Queue(ctx, dispatched.TaskID, "root"))
	must(s.Dispatch(ctx, dispatched.TaskID, "root", "root"))

	must(s.Queue(ctx, done.TaskID, "root"))
	must(s.Dispatch(ctx, done.TaskID, "root", "root"))
	must(s.Accept(ctx, done.TaskID, "root"))
	must(s.Complete(ctx, done.TaskID, "root", map[string]any{"ok": true}))

	if _, err := s.Recover(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}

	for _, want := range []struct{ id, state string }{
		{running.TaskID, StateFailed},
		{dispatched.TaskID, StateQueued},
		{done.TaskID, StateDone},
	} {
		got, err := s.Get(ctx, want.id)
		if err != nil {
			t.Fatalf("get %s: %v", want.id, err)
		}
		if got.State != want.state {
			t.Fatalf("%s state = %s, want %s", want.id, got.State, want.state)
		}
	}
}

// TestCancelPropagates verifies a cancel message reaches the executor and the
// task is marked cancelled (P0-37).
func TestCancelPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCore(t, "entry-cancel", "127.0.0.1:17856")
	// Worker has a slow native command so the task stays running long
	// enough to receive the cancel.
	worker := newCoreWithNative(t, "worker-cancel", "127.0.0.1:17857", ledger.NativeAbility{
		ID: "sys:sleep", Command: "sleep", Args: []string{"5"},
	})
	startPair(t, ctx, entry, worker, "127.0.0.1:17856", "127.0.0.1:17857")

	// Delegate a task.
	env, _ := bus.NewEnvelope(bus.MsgTaskDelegate, "entry-cancel", "m1", bus.TaskDelegatePayload{
		TaskID: "cancel-task", Title: "t", Intent: "x", Requires: []string{"sys:sleep"},
		Chain: []string{"entry-cancel"},
	})
	if err := entry.sendTo("worker-cancel", env); err != nil {
		t.Fatalf("delegate: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Send cancel while the task is still sleeping.
	cenv, _ := bus.NewEnvelope(bus.MsgTaskCancel, "entry-cancel", "m2", bus.TaskCancelPayload{
		TaskID: "cancel-task", Reason: "changed mind",
	})
	if err := entry.sendTo("worker-cancel", cenv); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	tk, err := worker.store.Get(ctx, "cancel-task")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if tk.State != StateCancelled {
		t.Fatalf("worker state = %s, want cancelled", tk.State)
	}
}

// startPair boots two cores and wires them together, failing the test on error.
func startPair(t *testing.T, ctx context.Context, entry, worker *Core, entryAddr, workerAddr string) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("startup: %v", err)
		}
	}
	must(entry.Register(ctx))
	must(worker.Register(ctx))

	ed := make(chan error, 1)
	wd := make(chan error, 1)
	go func() { ed <- entry.Listen(ctx, entryAddr) }()
	go func() { wd <- worker.Listen(ctx, workerAddr) }()
	time.Sleep(200 * time.Millisecond)

	must(entry.DialPeer(ctx, workerAddr))
	time.Sleep(200 * time.Millisecond)
}

// newCoreWithNative builds a Core whose card has the given native ability.
func newCoreWithNative(t *testing.T, id, addr string, native ledger.NativeAbility) *Core {
	t.Helper()
	db := openTestDB(t)
	card := ledger.Card{
		Device:        id,
		ResourceClass: "Standard",
		Native:        []ledger.NativeAbility{native},
		Capacity:      ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
	c := NewCore(db, id, card, 5, testLogger(), config.ModelConfig{})
	c.SetSharedSecret(testSharedSecret)
	return c
}

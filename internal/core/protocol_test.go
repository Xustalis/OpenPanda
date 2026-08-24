package core

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// TestRemoteReviewStatePropagates verifies the long-running supervision
// contract over a real authenticated WebSocket: an executor that exhausts its
// supervisor budget reports review, and the delegator must not promote that
// result to done merely because the adapter exited successfully.
func TestRemoteReviewStatePropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCore(t, "entry-review-wire", "127.0.0.1:17936")
	worker := newSuperviseCore(t, "worker-review-wire", 1)
	worker.SetWorkDir(t.TempDir())
	worker.SetSuperviseRounds(1)
	worker.SetSupervisor(newFakeSupervisor(t, func(int) string {
		return `{"status":"continue","reason":"incomplete","followup":"finish remaining work"}`
	}))
	var calls atomic.Int32
	worker.router.SetAdapterRunner(agentRunner(&calls))
	startPair(t, ctx, entry, worker, "127.0.0.1:17936", "127.0.0.1:17937")

	env, err := bus.NewEnvelope(bus.MsgTaskDelegate, "entry-review-wire", "review-wire-1", bus.TaskDelegatePayload{
		TaskID: "review-wire-task", Title: "long task", Intent: "finish a multi-step change",
		Requires: []string{"code:modify"}, Chain: []string{"entry-review-wire"}, AttemptID: "attempt-review-wire",
	})
	if err != nil {
		t.Fatalf("build delegate: %v", err)
	}
	if err := entry.sendTo("worker-review-wire", env); err != nil {
		t.Fatalf("send delegate: %v", err)
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		entryTask, entryErr := entry.store.Get(ctx, "review-wire-task")
		workerTask, workerErr := worker.store.Get(ctx, "review-wire-task")
		if entryErr == nil && workerErr == nil && entryTask.State == StateReview && workerTask.State == StateReview {
			if calls.Load() != 1 {
				t.Fatalf("worker ran %d rounds, want 1", calls.Load())
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	entryTask, _ := entry.store.Get(ctx, "review-wire-task")
	workerTask, _ := worker.store.Get(ctx, "review-wire-task")
	t.Fatalf("review state did not propagate: entry=%s worker=%s", entryTask.State, workerTask.State)
}

func TestRemoteReviewRejectsLateDone(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := createTask(t, s, "", "review-protected", "entry")
	if err := s.Queue(ctx, tk.TaskID, "entry"); err != nil {
		t.Fatal(err)
	}
	if err := s.Dispatch(ctx, tk.TaskID, "entry", "worker"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReviewFromRemote(ctx, tk.TaskID, "entry", map[string]any{"state": StateReview}); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteFromRemote(ctx, tk.TaskID, "entry", map[string]any{"state": StateDone}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateReview {
		t.Fatalf("late remote done changed protected review state to %s", got.State)
	}
}

func TestCapabilitySummaryCarriesNodeIdentity(t *testing.T) {
	c := newCore(t, "vm-node", "127.0.0.1:17986")
	c.card.NodeKind = "vm"
	c.card.NodeIdentity = "vm-test-identity"
	s := c.summary()
	if s.NodeKind != "vm" || s.NodeIdentity != "vm-test-identity" {
		t.Fatalf("summary identity = %+v", s)
	}
}

// TestTwoNodeProtocol spins up two cores over real WebSocket on loopback and
// verifies: hello exchange registers peers, task_delegate is routed, and the
// result returns to the delegator.
func TestTwoNodeProtocol(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCore(t, "entry", "127.0.0.1:17836")
	worker := newCore(t, "worker", "127.0.0.1:17837")

	if err := entry.Register(ctx); err != nil {
		t.Fatalf("register entry: %v", err)
	}
	if err := worker.Register(ctx); err != nil {
		t.Fatalf("register worker: %v", err)
	}

	entryDone := make(chan error, 1)
	workerDone := make(chan error, 1)
	go func() { entryDone <- entry.Listen(ctx, "127.0.0.1:17836") }()
	go func() { workerDone <- worker.Listen(ctx, "127.0.0.1:17837") }()

	// Wait for both servers to accept.
	time.Sleep(200 * time.Millisecond)

	if err := entry.DialPeer(ctx, "127.0.0.1:17837"); err != nil {
		t.Fatalf("dial worker: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Delegate a task from entry to worker.
	env, err := bus.NewEnvelope(bus.MsgTaskDelegate, "entry", "msg-1", bus.TaskDelegatePayload{
		TaskID:   "task-proto-1",
		Title:    "say hello",
		Intent:   "print hello from worker",
		Requires: []string{"sys:info"},
		Chain:    []string{"entry"},
	})
	if err != nil {
		t.Fatalf("build delegate: %v", err)
	}
	if err := entry.sendTo("worker", env); err != nil {
		t.Fatalf("send delegate: %v", err)
	}

	// Wait for the result to be stored on the entry side.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tk, err := entry.store.Get(ctx, "task-proto-1")
		if err == nil && tk.State == StateDone {
			if tk.ResultJSON == "" {
				t.Fatalf("task done but no result recorded")
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Diagnose on failure.
	if tk, err := entry.store.Get(ctx, "task-proto-1"); err == nil {
		evs, _ := entry.store.Events(ctx, tk.TaskID)
		t.Logf("entry task state=%s events=%d result=%q", tk.State, len(evs), tk.ResultJSON)
		for _, e := range evs {
			t.Logf("  event %s data=%s", e.Type, e.DataJSON)
		}
	} else {
		t.Logf("entry has no task: %v", err)
	}
	if tk, err := worker.store.Get(ctx, "task-proto-1"); err == nil {
		t.Logf("worker task state=%s", tk.State)
	} else {
		t.Logf("worker has no task: %v", err)
	}
	t.Fatalf("task did not reach done on entry within deadline")
}

// TestDelegatePersistsDetail verifies the entry-model detail carried on the
// wire (context_type/intent/spec/complexity/risk) lands in the executor's
// local store, so the queue shows it even before execution completes.
func TestDelegatePersistsDetail(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCore(t, "entry-detail", "127.0.0.1:17866")
	worker := newCore(t, "worker-detail", "127.0.0.1:17867")
	startPair(t, ctx, entry, worker, "127.0.0.1:17866", "127.0.0.1:17867")

	env, _ := bus.NewEnvelope(bus.MsgTaskDelegate, "entry-detail", "m1", bus.TaskDelegatePayload{
		TaskID: "detail-task", Title: "refactor", ContextType: "file",
		Intent: "重构 Hero.vue", SpecJSON: `{"scope":"Hero.vue"}`,
		Requires: []string{"sys:info"}, Complexity: 0.6, Risk: "high",
		Chain: []string{"entry-detail"},
	})
	if err := entry.sendTo("worker-detail", env); err != nil {
		t.Fatalf("delegate: %v", err)
	}

	// Wait for the worker to finish the task.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tk, err := worker.store.Get(ctx, "detail-task")
		if err == nil && Terminal(tk.State) {
			if tk.ContextType != "file" || tk.Intent != "重构 Hero.vue" ||
				tk.Complexity != 0.6 || tk.Risk != "high" || tk.SpecJSON != `{"scope":"Hero.vue"}` {
				t.Fatalf("worker detail mismatch: %+v", tk)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("worker did not persist detail within deadline")
}

func newCore(t *testing.T, id, addr string) *Core {
	t.Helper()
	db := openTestDB(t)
	card := ledger.Card{
		Device:        id,
		ResourceClass: "Standard",
		Native:        []ledger.NativeAbility{{ID: "sys:info", Command: "uname"}},
		Capacity:      ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
	c := NewCore(db, id, card, 5, verboseTestLogger(), config.ModelConfig{})
	c.SetSharedSecret(testSharedSecret)
	return c
}

// testSharedSecret is the shared HMAC secret used by every test core that
// exchanges hellos, so the transport-auth handshake (P0-1) succeeds in tests.
const testSharedSecret = "test-secret"

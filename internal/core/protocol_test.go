package core

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
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

// TestRemoteResumeApprovedReRunsOnExecutor pins the cross-node approval
// closure: a tier-2 task delegated to a peer refuses unauthorized, the review
// propagates back to the delegator (surfacing as an approval request), and
// the user's consent re-runs the task on the executor — the node holding the
// capability — via task_resume, not on the delegator (which cannot route the
// ability at all). Both copies converge to done, and the agent spawned exactly
// once: the refusal preceded the first spawn, so the resume is the sole run.
func TestRemoteResumeApprovedReRunsOnExecutor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCore(t, "entry-resume-wire", "127.0.0.1:17938")
	worker := newSuperviseCore(t, "worker-resume-wire", 2)
	worker.SetWorkDir(t.TempDir())
	var calls atomic.Int32
	worker.router.SetAdapterRunner(agentRunner(&calls))
	startPair(t, ctx, entry, worker, "127.0.0.1:17938", "127.0.0.1:17939")

	// Unauthorized submit: the entry cannot match code:modify locally, so the
	// task forwards to the worker, whose tier-2 gate refuses before the agent
	// spawns and parks the task in review on both nodes.
	task, result, err := entry.Submit(ctx, TaskInput{
		Title: "push changes", Project: "proj", ContextType: "command",
		Intent: "push to remote", Requires: []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.State != StateReview {
		t.Fatalf("pre-approval entry state = %s, want review", task.State)
	}
	if !commander.IsAuthorizationRefusal(result.Stderr) {
		t.Fatalf("pre-approval stderr = %q, want the tier-2 refusal", result.Stderr)
	}
	if calls.Load() != 0 {
		t.Fatalf("agent ran %d times before approval, want 0 (gate refuses before spawn)", calls.Load())
	}

	final, res, err := entry.ResumeApproved(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("resume approved: %v", err)
	}
	if final.State != StateDone {
		t.Fatalf("post-approval entry state = %s, want done", final.State)
	}
	if !res.OK {
		t.Fatalf("resumed result OK = false (stderr=%q)", res.Stderr)
	}
	if calls.Load() != 1 {
		t.Fatalf("agent ran %d times, want 1 (the resume is the sole spawn)", calls.Load())
	}

	// The executor's copy must converge too: a local-only re-run would have
	// stranded the worker's review row while the entry reported done.
	workerTask, err := worker.store.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("load worker copy: %v", err)
	}
	if workerTask.State != StateDone {
		t.Fatalf("post-approval worker state = %s, want done", workerTask.State)
	}
	if !workerTask.Authorized {
		t.Fatal("worker copy must carry the tier-2 consent")
	}
}

// TestRemoteResumeRejectedFromNonDelegator guards the resume authorization:
// only the task's delegator (the chain predecessor) may grant tier-2 consent.
// A resume from any other authenticated peer is dropped, and the task stays
// parked in review.
func TestRemoteResumeRejectedFromNonDelegator(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCore(t, "entry-resume-guard", "127.0.0.1:17940")
	worker := newSuperviseCore(t, "worker-resume-guard", 2)
	worker.SetWorkDir(t.TempDir())
	stranger := newCore(t, "stranger-resume-guard", "127.0.0.1:17941")
	var calls atomic.Int32
	worker.router.SetAdapterRunner(agentRunner(&calls))
	startPair(t, ctx, entry, worker, "127.0.0.1:17940", "127.0.0.1:17942")
	if err := stranger.DialPeer(ctx, "127.0.0.1:17942"); err != nil {
		t.Fatalf("stranger dial worker: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	task, _, err := entry.Submit(ctx, TaskInput{
		Title: "deploy", Project: "proj", ContextType: "command",
		Intent: "deploy the build", Requires: []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.State != StateReview {
		t.Fatalf("pre-approval state = %s, want review", task.State)
	}

	env, err := bus.NewEnvelope(bus.MsgTaskResume, "stranger-resume-guard", "resume-stranger-1", bus.TaskResumePayload{
		TaskID: task.TaskID, AttemptID: task.AttemptID,
	})
	if err != nil {
		t.Fatalf("build resume: %v", err)
	}
	if err := stranger.sendTo("worker-resume-guard", env); err != nil {
		t.Fatalf("send resume: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	workerTask, err := worker.store.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("load worker copy: %v", err)
	}
	if workerTask.State != StateReview {
		t.Fatalf("worker state after forged resume = %s, want review (unauthorized resume must be dropped)", workerTask.State)
	}
	if workerTask.Authorized {
		t.Fatal("forged resume must not grant authorization")
	}
	if calls.Load() != 0 {
		t.Fatalf("agent ran %d times, want 0 (forged resume must not re-run)", calls.Load())
	}
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

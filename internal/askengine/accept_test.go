// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// TestEngineAcceptMirrorsViaScheduler pins acceptReviewedWork's scheduler
// path: approving an AcceptWork park through the engine must go through the
// Core (which mirrors the accept to the sibling copy on the peer), not the
// store-only fallback. With a remote dispatch target and no live peer the
// mirror parks in resume_outbox — a row the store-only path never writes, so
// its presence proves the scheduler path ran.
func TestEngineAcceptMirrorsViaScheduler(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	card := ledger.Card{
		Device:   "test-node",
		NodeKind: "physical",
		Capacity: ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 2},
	}
	cfg := &config.Config{}
	cfg.Node.Name = "test-node"
	cfg.Node.Kind = "physical"
	cfg.Model.Model = "test-model"

	sched := core.NewCore(db, "test-node", card, 1, nil, cfg.Model)
	e := &Engine{cfg: cfg, db: db, schedCtx: context.Background()}
	e.sched.Store(sched)

	store := core.NewTaskStore(db, nil)
	task, err := store.Create(ctx, "", "proj", "accepted remotely", "test-node", []string{"test-node"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.Queue(ctx, task.TaskID, "test-node"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := store.Dispatch(ctx, task.TaskID, "test-node", "remote-worker"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := store.Accept(ctx, task.TaskID, "test-node"); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := store.PauseWithResult(ctx, task.TaskID, "test-node", map[string]any{"ok": true}); err != nil {
		t.Fatalf("pause: %v", err)
	}

	out := e.ResumeApproved(ctx, task.TaskID, "", StreamCallbacks{})
	if out == nil || out.TaskState != core.StateDone {
		t.Fatalf("accept result = %+v, want state done", out)
	}
	var parked int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM resume_outbox WHERE task_id = ?`, task.TaskID).Scan(&parked); err != nil {
		t.Fatalf("count parked mirror: %v", err)
	}
	if parked != 1 {
		t.Fatalf("parked mirror frames = %d, want 1 (scheduler path must carry the accept)", parked)
	}
}

// TestEngineDeferredResumeSkipsSummarize pins the rendering contract for a
// parked approval: when the executor's link is down the consent is claimed
// and queued in resume_outbox, and the deferred receipt IS the answer —
// passing it through SummarizeResult lets the model invent a stale "waiting
// for approval, re-run the task" story that contradicts a consent already
// granted. The result must carry the receipt verbatim, not a summary.
func TestEngineDeferredResumeSkipsSummarize(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	card := ledger.Card{
		Device:   "test-node",
		NodeKind: "physical",
		Capacity: ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 2},
	}
	cfg := &config.Config{}
	cfg.Node.Name = "test-node"
	cfg.Node.Kind = "physical"
	cfg.Model.Model = "test-model"

	sched := core.NewCore(db, "test-node", card, 1, nil, cfg.Model)
	e := &Engine{cfg: cfg, db: db, schedCtx: context.Background()}
	e.sched.Store(sched)

	store := core.NewTaskStore(db, nil)
	task, err := store.Create(ctx, "", "proj", "consent for offline peer", "test-node", []string{"test-node"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, step := range []func() error{
		func() error { return store.Queue(ctx, task.TaskID, "test-node") },
		func() error { return store.Dispatch(ctx, task.TaskID, "test-node", "remote-worker") },
		func() error { return store.Accept(ctx, task.TaskID, "test-node") },
		func() error {
			return store.PauseForAnswer(ctx, task.TaskID, "test-node", map[string]any{"question": "run it?"})
		},
	} {
		if err := step(); err != nil {
			t.Fatalf("drive task: %v", err)
		}
	}

	out := e.ResumeApproved(ctx, task.TaskID, "", StreamCallbacks{})
	if out == nil {
		t.Fatal("deferred resume returned nil result")
	}
	if !out.Deferred {
		t.Fatalf("deferred = false, want the parked receipt flagged deferred: %+v", out)
	}
	if out.Answer != out.Stdout {
		t.Fatalf("answer = %q, want the deferred receipt verbatim (%q) — the model summary must not run", out.Answer, out.Stdout)
	}
	var parked int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM resume_outbox WHERE task_id = ?`, task.TaskID).Scan(&parked); err != nil {
		t.Fatalf("count parked resume: %v", err)
	}
	if parked != 1 {
		t.Fatalf("resume_outbox rows = %d, want the consent parked for delivery", parked)
	}
}

// newStageTestEngine is the accept_test engine setup factored for plan tests:
// a real Core as the scheduler (so ResumeApproved walks the wire paths) over
// a migrated test store.
func newStageTestEngine(t *testing.T) (*Engine, *core.TaskStore, context.Context) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	card := ledger.Card{
		Device:   "test-node",
		NodeKind: "physical",
		Capacity: ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 2},
	}
	cfg := &config.Config{}
	cfg.Node.Name = "test-node"
	cfg.Node.Kind = "physical"
	cfg.Model.Model = "test-model"
	sched := core.NewCore(db, "test-node", card, 1, nil, cfg.Model)
	e := &Engine{cfg: cfg, db: db, schedCtx: context.Background(),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	e.sched.Store(sched)
	return e, core.NewTaskStore(db, nil), ctx
}

// driveStage walks a stage task through the local run lifecycle to done.
func driveStage(t *testing.T, store *core.TaskStore, ctx context.Context, taskID, owner string) {
	t.Helper()
	for _, step := range []func() error{
		func() error { return store.Queue(ctx, taskID, owner) },
		func() error { return store.Dispatch(ctx, taskID, owner, owner) },
		func() error { return store.Accept(ctx, taskID, owner) },
		func() error { return store.Complete(ctx, taskID, owner, map[string]any{"exit_code": 0}) },
	} {
		if err := step(); err != nil {
			t.Fatalf("drive stage %s: %v", taskID, err)
		}
	}
}

// TestAwaitPlanOutcomeSettledBoard covers the re-entry the approval path
// relies on: a plan whose stages all settled returns the verdict-shaped plan
// result — same shape the submit-time follow produces — not the resumed
// stage's lone receipt.
func TestAwaitPlanOutcomeSettledBoard(t *testing.T) {
	e, store, ctx := newStageTestEngine(t)

	s1, err := store.Create(ctx, "", "proj", "stage one", "test-node", []string{"test-node"})
	if err != nil {
		t.Fatalf("stage1: %v", err)
	}
	s2, err := store.Create(ctx, "", "proj", "stage two", "test-node", []string{"test-node"})
	if err != nil {
		t.Fatalf("stage2: %v", err)
	}
	if err := store.SetStage(ctx, s1.TaskID, "plan-1", "01-first", nil); err != nil {
		t.Fatalf("set stage1: %v", err)
	}
	if err := store.SetStage(ctx, s2.TaskID, "plan-1", "02-second", []string{"01-first"}); err != nil {
		t.Fatalf("set stage2: %v", err)
	}
	driveStage(t, store, ctx, s1.TaskID, "test-node")
	driveStage(t, store, ctx, s2.TaskID, "test-node")

	res := e.AwaitPlanOutcome(ctx, "plan-1", StreamCallbacks{})
	if res == nil {
		t.Fatal("AwaitPlanOutcome returned nil for a real plan")
	}
	if res.Kind != "plan" || res.PlanID != "plan-1" || !res.OK || res.NeedsApproval {
		t.Fatalf("res = %+v, want settled OK plan board", res)
	}
	if len(res.PlanStages) != 2 {
		t.Fatalf("board = %d stages, want 2", len(res.PlanStages))
	}
}

// TestAwaitPlanOutcomeSurfacesNextGate is the chain case: the resumed stage
// finished but the NEXT stage parks for consent — the re-await must hand back
// an approval result for that stage so the caller raises the card again
// instead of reporting the pipeline as finished.
func TestAwaitPlanOutcomeSurfacesNextGate(t *testing.T) {
	e, store, ctx := newStageTestEngine(t)

	s1, err := store.Create(ctx, "", "proj", "stage one", "test-node", []string{"test-node"})
	if err != nil {
		t.Fatalf("stage1: %v", err)
	}
	s2, err := store.Create(ctx, "", "proj", "stage two", "test-node", []string{"test-node"})
	if err != nil {
		t.Fatalf("stage2: %v", err)
	}
	if err := store.SetStage(ctx, s1.TaskID, "plan-2", "01-first", nil); err != nil {
		t.Fatalf("set stage1: %v", err)
	}
	if err := store.SetStage(ctx, s2.TaskID, "plan-2", "02-second", []string{"01-first"}); err != nil {
		t.Fatalf("set stage2: %v", err)
	}
	driveStage(t, store, ctx, s1.TaskID, "test-node")
	for _, step := range []func() error{
		func() error { return store.Queue(ctx, s2.TaskID, "test-node") },
		func() error { return store.Dispatch(ctx, s2.TaskID, "test-node", "test-node") },
		func() error { return store.Accept(ctx, s2.TaskID, "test-node") },
		func() error {
			return store.PauseForAnswer(ctx, s2.TaskID, "test-node", map[string]any{"question": "stage two ok?"})
		},
	} {
		if err := step(); err != nil {
			t.Fatalf("drive stage2: %v", err)
		}
	}

	res := e.AwaitPlanOutcome(ctx, "plan-2", StreamCallbacks{})
	if res == nil || !res.NeedsApproval || res.Approval == nil {
		t.Fatalf("res = %+v, want the parked stage surfaced as an approval", res)
	}
	if res.Approval.TaskID != s2.TaskID {
		t.Fatalf("approval task = %s, want the parked stage %s", res.Approval.TaskID, s2.TaskID)
	}
}

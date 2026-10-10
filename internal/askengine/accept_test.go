// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
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

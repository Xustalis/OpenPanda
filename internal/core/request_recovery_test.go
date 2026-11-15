// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/plan"
	"github.com/Xustalis/OpenPanda/internal/storage"
	"github.com/Xustalis/OpenPanda/internal/testdb"
)

func recoveryFileCore(t *testing.T) (*Core, func() *Core) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "work.db")
	workDir := t.TempDir()
	makeCore := func(db *sql.DB) *Core {
		c := NewCore(db, "recovery-node", ledger.Card{Device: "recovery-node"}, 5, testLogger(), config.ModelConfig{})
		c.SetWorkDir(workDir)
		return c
	}
	c := makeCore(testdb.OpenFile(t, path))
	return c, func() *Core {
		t.Helper()
		if err := c.db.Close(); err != nil {
			t.Fatal(err)
		}
		db, err := storage.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		c = makeCore(db)
		return c
	}
}

func reserveRecoveryTask(t *testing.T, c *Core, req ExecutionRequest, in TaskInput, q QueueSpec) {
	t.Helper()
	identityIn, identityQ := in, q
	identityIn.SessionID, identityQ.SessionID = "", ""
	if _, err := c.store.reserveExecution(context.Background(), req, queuedRequestPayload{identityIn, identityQ}, "task", queuedRequestPayload{in, q}); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionRequestRecoversAfterDatabaseReopen(t *testing.T) {
	c, restart := recoveryFileCore(t)
	ctx := context.Background()
	q := DefaultQueueSpec()
	q.SessionID, q.Priority = "original-thread", PriorityHigh
	q.ResourceKeys = []string{"device:scanner"}
	reserveRecoveryTask(t, c, businessRequest(), TaskInput{Title: "durable task", Intent: "inspect", Authorized: true}, q)
	c = restart()
	if n, err := c.Recover(ctx); err != nil || n != 1 {
		t.Fatalf("recover=%d err=%v", n, err)
	}
	var id string
	if err := c.db.QueryRow(`SELECT task_id FROM execution_requests WHERE request_id='request-one'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	got, err := c.store.Get(ctx, id)
	if err != nil || got.Title != "durable task" || got.SessionID != q.SessionID || got.Priority != q.Priority || !got.Authorized || len(got.ResourceKeys) != 1 || got.ResourceKeys[0] != q.ResourceKeys[0] {
		t.Fatalf("lost replay input: %+v err=%v", got, err)
	}
	c = restart()
	if n, err := c.Recover(ctx); err != nil || n != 0 {
		t.Fatalf("second recover=%d err=%v", n, err)
	}
	for _, table := range []string{"tasks", "execution_requests", "execution_links"} {
		requireRowCount(t, c, table, 1)
	}
}

func TestExecutionRequestRecoversWholePlanAfterDatabaseReopen(t *testing.T) {
	c, restart := recoveryFileCore(t)
	ctx := context.Background()
	p := plan.Plan{Goal: "recover all stages", Stages: []plan.Stage{{ID: "inspect", Intent: "inspect"}, {ID: "report", Intent: "report", Needs: []string{"inspect"}}}}
	q := DefaultQueueSpec()
	q.SessionID = "plan-thread"
	identityQ := q
	identityQ.SessionID = ""
	if _, err := c.store.reserveExecution(ctx, businessRequest(), planRequestPayload{p, identityQ}, "plan", planRequestPayload{p, q}); err != nil {
		t.Fatal(err)
	}
	c = restart()
	if n, err := c.Recover(ctx); err != nil || n != 1 {
		t.Fatalf("recover plan=%d err=%v", n, err)
	}
	var id string
	if err := c.db.QueryRow(`SELECT plan_id FROM execution_requests`).Scan(&id); err != nil || id == "" {
		t.Fatalf("plan id=%q err=%v", id, err)
	}
	stages, err := c.store.PlanStages(ctx, id)
	if err != nil || len(stages) != 2 {
		t.Fatalf("stages=%+v err=%v", stages, err)
	}
	for _, stage := range stages {
		if stage.SessionID != q.SessionID || stage.Authorized {
			t.Fatalf("stage association or consent changed: %+v", stage)
		}
		if stage.StageID == "report" && (len(stage.Needs) != 1 || stage.Needs[0] != "inspect" || stage.State != StateSubmitted) {
			t.Fatalf("dependency lost: %+v", stage)
		}
	}
	c = restart()
	if _, err := c.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	requireRowCount(t, c, "tasks", 2)
	requireRowCount(t, c, "execution_links", 2)
}

func TestExecutionRequestRecoveryDoesNotReviveRejectedOverlap(t *testing.T) {
	c, restart := recoveryFileCore(t)
	ctx := context.Background()
	req, in, q := businessRequest(), TaskInput{Title: "one logical task"}, DefaultQueueSpec()
	if _, err := c.EnqueueRequest(ctx, req, in, q); err != nil {
		t.Fatal(err)
	}
	req.RequestID, req.TriggerKey = "rejected-rerun", "rerun"
	if _, err := c.EnqueueRequest(ctx, req, in, q); !errors.Is(err, ErrConflict) {
		t.Fatalf("overlap=%v", err)
	}
	if _, err := c.db.Exec(`UPDATE tasks SET state='cancelled'`); err != nil {
		t.Fatal(err)
	}
	c = restart()
	if n, err := c.Recover(ctx); err != nil || n != 0 {
		t.Fatalf("revived rejected request: n=%d err=%v", n, err)
	}
	requireRowCount(t, c, "tasks", 1)
	// Only an explicit resubmission can re-enable the rejected intention.
	if _, err := c.EnqueueRequest(ctx, req, in, q); err != nil {
		t.Fatal(err)
	}
	requireRowCount(t, c, "tasks", 2)
}

func TestExecutionRequestRecoveryLeavesRemoteForLeaseValidation(t *testing.T) {
	c, restart := recoveryFileCore(t)
	req := businessRequest()
	req.ConnectionID, req.AccountID, req.TaskKey = "remote", "person", "instance/workspace/space/task"
	reserveRecoveryTask(t, c, req, TaskInput{Title: "remote work"}, DefaultQueueSpec())
	c = restart()
	if n, err := c.Recover(context.Background()); err != nil || n != 0 {
		t.Fatalf("remote request executed without a lease: n=%d err=%v", n, err)
	}
	requireRowCount(t, c, "tasks", 0)
}

func TestExecutionRequestRecoveryLegacyPayloadRequiresValidatedResubmission(t *testing.T) {
	c, restart := recoveryFileCore(t)
	ctx := context.Background()
	req, in, q := businessRequest(), TaskInput{Title: "legacy intention"}, DefaultQueueSpec()
	reserveRecoveryTask(t, c, req, in, q)
	if _, err := c.db.Exec(`UPDATE execution_requests SET payload_kind='',payload_json=''`); err != nil {
		t.Fatal(err)
	}
	c = restart()
	if n, err := c.Recover(ctx); err != nil || n != 0 {
		t.Fatalf("legacy recover=%d err=%v", n, err)
	}
	var reason string
	if err := c.db.QueryRow(`SELECT preparation_error FROM execution_requests`).Scan(&reason); err != nil || !strings.Contains(reason, "resubmit") {
		t.Fatalf("missing actionable recovery error: %q err=%v", reason, err)
	}
	if _, err := c.EnqueueRequest(ctx, req, TaskInput{Title: "changed input"}, q); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed legacy input=%v", err)
	}
	if _, err := c.EnqueueRequest(ctx, req, in, q); err != nil {
		t.Fatal(err)
	}
	requireRowCount(t, c, "tasks", 1)
}

func TestExecutionRequestRecoveryRejectsTamperedPayload(t *testing.T) {
	c, restart := recoveryFileCore(t)
	reserveRecoveryTask(t, c, businessRequest(), TaskInput{Title: "original"}, DefaultQueueSpec())
	if _, err := c.db.Exec(`UPDATE execution_requests SET payload_json=replace(payload_json,'original','tampered')`); err != nil {
		t.Fatal(err)
	}
	c = restart()
	if n, err := c.Recover(context.Background()); err != nil || n != 0 {
		t.Fatalf("tampered recover=%d err=%v", n, err)
	}
	requireRowCount(t, c, "tasks", 0)
	var reason string
	if err := c.db.QueryRow(`SELECT preparation_error FROM execution_requests`).Scan(&reason); err != nil || !strings.Contains(reason, "different input") {
		t.Fatalf("tampered payload not diagnosed: %q err=%v", reason, err)
	}
}

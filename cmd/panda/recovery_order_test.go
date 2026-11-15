// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/testdb"
)

// TestRecoveredTaskSurvivesStartup pins the daemon's startup order.
//
// A task queued while the daemon was down is exactly what task recovery
// exists to save. It was being lost instead: the queue scheduler started
// first and its first pass claimed the queued row (queued → dispatched),
// handing it to a runner; task recovery then ran and its "requeue the
// dispatched rows" branch moved that very row back to queued, under the
// runner. The runner refused a row that is not in a runnable state ("cannot
// run task in state queued"), the task died as failed, and the recovery
// that was supposed to save it was what killed it.
//
// The test drives the real startup helper and asserts the task is not lost
// to that state error. Two things prevent it now: the startup order (recover
// first, then let the scheduler claim) and the runner's reclaim of its own
// claim (internal/core's ReclaimClaimed) for the case where recovery lands
// inside the claim→run window anyway.
func TestRecoveredTaskSurvivesStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "node.db")
	db := testdb.OpenFile(t, dbPath)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	nodeID := "recovery-order-node"
	c := core.NewCore(db, nodeID, ledger.Card{Device: "test", ResourceClass: "Standard"}, 3, logger, config.ModelConfig{})

	// The row a previous process left behind: queued, waiting for a runner.
	task, err := c.Enqueue(ctx, core.TaskInput{
		Title:  "queued while the daemon was down",
		Intent: "echo recovered",
	}, core.DefaultQueueSpec())
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if got := taskState(t, db, task.TaskID); got != core.StateQueued {
		t.Fatalf("task state before startup = %s, want %s", got, core.StateQueued)
	}

	// The daemon's startup sequence, in the order it must happen.
	recoverThenStartQueue(ctx, logger, c)

	// Give the scheduler time to claim the row and hand it to a runner.
	deadline := time.Now().Add(20 * time.Second)
	var state string
	for time.Now().Before(deadline) {
		state = taskState(t, db, task.TaskID)
		if state != core.StateQueued {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if state == core.StateQueued {
		t.Fatalf("the recovered task is still queued — the scheduler never claimed it")
	}
	// The row left `queued`, so the scheduler did claim it. The bug this
	// pins is not that the task fails — without a model the run legitimately
	// fails — but that recovery undoes the claim underneath the runner and
	// the row dies with the state error instead of the task's own verdict.
	if reason := taskResult(t, db, task.TaskID); strings.Contains(reason, "cannot run task in state") {
		t.Fatalf("the recovered task was lost to the startup order: result=%q", reason)
	}
}

// taskState reads a task's state straight from the store.
func taskState(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var state string
	if err := db.QueryRow(`SELECT state FROM tasks WHERE task_id = ?`, id).Scan(&state); err != nil {
		t.Fatalf("read task state: %v", err)
	}
	return state
}

// taskResult reads a task's result blob — the place the lost-task failure
// recorded its reason.
func taskResult(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var result sql.NullString
	if err := db.QueryRow(`SELECT result_json FROM tasks WHERE task_id = ?`, id).Scan(&result); err != nil {
		t.Fatalf("read task result: %v", err)
	}
	return result.String
}

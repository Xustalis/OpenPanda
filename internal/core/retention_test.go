// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestDeleteSettledBeforeAgesOutOnlySettled is the retention contract: old
// done/failed/cancelled/expired rows go, everything a human or a live state
// machine still needs stays — review and active rows are never swept however
// old they get.
func TestDeleteSettledBeforeAgesOutOnlySettled(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	old := time.Now().Add(-40 * 24 * time.Hour).Unix()
	fresh := time.Now().Unix()

	mkAged := func(title, state string, ts int64) Task {
		tk := createTask(t, s, "", title, "node")
		if _, err := s.db.ExecContext(ctx,
			`UPDATE tasks SET state = ?, updated_at = ? WHERE task_id = ?`, state, ts, tk.TaskID); err != nil {
			t.Fatalf("age task: %v", err)
		}
		tk.State = state
		tk.UpdatedAt = ts
		return tk
	}

	oldDone := mkAged("old done", StateDone, old)
	oldFailed := mkAged("old failed", StateFailed, old)
	oldCancelled := mkAged("old cancelled", StateCancelled, old)
	oldExpired := mkAged("old expired", StateExpired, old)
	oldReview := mkAged("old review", StateReview, old)
	oldQueued := mkAged("old queued", StateQueued, old)
	freshDone := mkAged("fresh done", StateDone, fresh)

	cutoff := time.Now().Add(-30 * 24 * time.Hour).Unix()
	n, err := s.DeleteSettledBefore(ctx, cutoff)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 4 {
		t.Fatalf("deleted = %d, want 4", n)
	}
	for _, tk := range []Task{oldDone, oldFailed, oldCancelled, oldExpired} {
		if _, err := s.Get(ctx, tk.TaskID); err == nil {
			t.Fatalf("%s should have been swept", tk.Title)
		}
	}
	for _, tk := range []Task{oldReview, oldQueued, freshDone} {
		if _, err := s.Get(ctx, tk.TaskID); err != nil {
			t.Fatalf("%s must survive the sweep: %v", tk.Title, err)
		}
	}
}

// TestDeleteSettledBeforeTakesSatellites proves the sweep removes the event
// timeline and pending outbox rows along with the task — a settled row must
// not leave deliveries replaying for a task that no longer exists.
func TestDeleteSettledBeforeTakesSatellites(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	old := time.Now().Add(-40 * 24 * time.Hour).Unix()

	tk := createTask(t, s, "", "satellites", "node")
	if err := s.RecordEvent(ctx, tk.TaskID, EvProgress, map[string]any{"note": "x"}); err != nil {
		t.Fatalf("record event: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO result_outbox (peer, task_id, payload_json, created_at) VALUES ('p', ?, '{}', 1)`,
		`INSERT INTO cancel_outbox (peer, task_id, reason, created_at) VALUES ('p', ?, 'r', 1)`,
		`INSERT INTO task_outbox (peer, task_id, payload_json, transport_type, ttl, created_at) VALUES ('p', ?, '{}', 'dtn', 0, 1)`,
		`INSERT INTO artifact_push_outbox (peer, hash, task_id, total, created_at) VALUES ('p', 'h', ?, 1, 1)`,
		`INSERT INTO delegation_metrics (task_id, delegator, executor, success, latency_ms, created_at) VALUES (?, 'd', 'e', 1, 0, 1)`,
	} {
		if _, err := s.db.ExecContext(ctx, q, tk.TaskID); err != nil {
			t.Fatalf("seed satellite: %v", err)
		}
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET state = ?, updated_at = ? WHERE task_id = ?`, StateDone, old, tk.TaskID); err != nil {
		t.Fatalf("age task: %v", err)
	}

	cutoff := time.Now().Add(-30 * 24 * time.Hour).Unix()
	if n, err := s.DeleteSettledBefore(ctx, cutoff); err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v; want 1, nil", n, err)
	}
	for _, table := range append(append([]string{}, taskSatellites...), "tasks") {
		var n int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM `+table+` WHERE task_id = ?`, tk.TaskID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Fatalf("%s still holds %d row(s) for swept task", table, n)
		}
	}
}

// TestDeleteSettledBeforeBatches proves the delete walks more than one batch:
// a backlog larger than retentionSweepBatch is fully cleared, and the loop
// terminates rather than re-reading the rows it just deleted.
func TestDeleteSettledBeforeBatches(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	old := time.Now().Add(-40 * 24 * time.Hour).Unix()
	want := retentionSweepBatch + 10

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for i := 0; i < want; i++ {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO tasks (task_id, title, state, owner_node, attempt_id, chain_json, updated_at, created_at)
			 VALUES (?, ?, 'done', 'n', ?, '[]', ?, ?)`,
			fmt.Sprintf("t-%d", i), fmt.Sprintf("bulk %d", i), fmt.Sprintf("a-%d", i), old, old); err != nil {
			tx.Rollback()
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	cutoff := time.Now().Add(-30 * 24 * time.Hour).Unix()
	n, err := s.DeleteSettledBefore(ctx, cutoff)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != want {
		t.Fatalf("deleted = %d, want %d", n, want)
	}
	var left int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks`).Scan(&left); err != nil {
		t.Fatalf("count: %v", err)
	}
	if left != 0 {
		t.Fatalf("%d tasks left after sweep", left)
	}
}

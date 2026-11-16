// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/testdb"
)

// testLogger returns a silent logger for tests.
func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// verboseTestLogger returns a logger that writes to the test's log output.
func verboseTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, nil))
}

// openTestDB returns a private database with the current migrated schema.
func openTestDB(t testing.TB) *sql.DB {
	t.Helper()
	return testdb.Open(t)
}

// waitLocalTasksSettled polls a core's store until no task sits in a
// non-terminal state. Wire tests that forward work assert on the routing
// decision and return while the peer is still executing — the executor's
// run writes its per-task work dir inside the node's t.TempDir(), and the
// test's RemoveAll cleanup then unlinks a directory mid-write ("directory
// not empty"). Draining execution first keeps that race out of CI.
func waitLocalTasksSettled(t *testing.T, c *Core) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		err := c.store.db.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM tasks WHERE state NOT IN ('done','failed','cancelled','expired')`).Scan(&n)
		if err != nil {
			t.Fatalf("count tasks: %v", err)
		}
		if n == 0 {
			waitArtifactWrites(t, c)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("node %s still has %d non-terminal tasks: %s", c.nodeID, n, taskDiagnostics(context.Background(), c.store.db, ""))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// taskDiagnostics captures the queue and recent events for open rows so a
// concurrency-test failure shows where the scheduler stopped making progress.
func taskDiagnostics(ctx context.Context, db *sql.DB, parentID string) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	query := "SELECT task_id, COALESCE(parent_id,''), state, owner_node, attempt_id, COALESCE(lease_expires_at,0), COALESCE(result_json,'') FROM tasks WHERE state NOT IN ('done','failed','cancelled','expired')"
	args := []any{}
	if parentID != "" {
		query += " AND parent_id=?"
		args = append(args, parentID)
	}
	query += " ORDER BY created_at, task_id"
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Sprintf("query open tasks: %v", err)
	}
	type taskSnapshot struct {
		id, parent, state, owner, attempt, result string
		lease                                     int64
	}
	var tasks []taskSnapshot
	for rows.Next() {
		var task taskSnapshot
		if err := rows.Scan(&task.id, &task.parent, &task.state, &task.owner, &task.attempt, &task.lease, &task.result); err != nil {
			rows.Close()
			return fmt.Sprintf("scan task: %v", err)
		}
		tasks = append(tasks, task)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return fmt.Sprintf("iterate tasks: %v", err)
	}
	var out []string
	for _, task := range tasks {
		if len(task.result) > 240 {
			task.result = task.result[:240] + "…"
		}
		var events []string
		eventRows, err := db.QueryContext(ctx, "SELECT type, data_json FROM task_events WHERE task_id=? ORDER BY id DESC LIMIT 4", task.id)
		if err == nil {
			for eventRows.Next() {
				var typ, data string
				if eventRows.Scan(&typ, &data) == nil {
					if len(data) > 180 {
						data = data[:180] + "…"
					}
					events = append(events, typ+":"+data)
				}
			}
			eventRows.Close()
		} else {
			events = append(events, "query error: "+err.Error())
		}
		out = append(out, fmt.Sprintf("id=%s parent=%s state=%s owner=%s attempt=%s lease=%d result=%q events=[%s]", task.id, task.parent, task.state, task.owner, task.attempt, task.lease, task.result, strings.Join(events, "; ")))
	}
	if len(out) == 0 {
		return "no open tasks found"
	}
	return strings.Join(out, " | ")
}

// waitArtifactWrites drains the detached artifact work a result handler
// launches past the task's terminal state — adoptStageOutput's replica pull,
// adoptWorktreeOutput's extract-into-checkout. waitLocalTasksSettled only
// sees row states: the row is already done while the pull is still writing
// into this node's t.TempDir() pool, which is exactly the "TempDir RemoveAll
// cleanup: directory not empty" flake the race suite kept reporting.
func waitArtifactWrites(t *testing.T, c *Core) {
	t.Helper()
	done := make(chan struct{})
	go func() { c.WaitArtifacts(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("node %s still has artifact writes in flight", c.nodeID)
	}
}

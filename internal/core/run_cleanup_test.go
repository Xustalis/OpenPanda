// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/commander"
)

func TestAdapterPanicSettlesClaimedAttempt(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "panic-cleanup", 1)
	c.SetWorkDir(t.TempDir())
	c.SetSupervisor(nil)
	var id string
	c.router.SetAdapterRunner(func(runCtx context.Context, _, _, _ string) commander.AgentResult {
		id = commander.TaskID(runCtx)
		panic("adapter exploded")
	})
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic must still reach the outer guard")
			}
		}()
		_, _, _ = c.SubmitLocal(ctx, TaskInput{Title: "panic", Intent: "work", Requires: []string{"code:modify"}})
	}()
	if id == "" {
		t.Fatal("adapter never started")
	}
	row, err := c.store.Get(ctx, id)
	if err != nil || row.State != StateFailed {
		t.Fatalf("state=%s err=%v; panic must fail its own attempt", row.State, err)
	}
	var locks int
	if err := c.db.QueryRowContext(ctx, "SELECT count(*) FROM exec_locks WHERE task_id=?", id).Scan(&locks); err != nil || locks != 0 {
		t.Fatalf("locks=%d err=%v", locks, err)
	}
	if _, ok := c.running.Load(id); ok {
		t.Fatal("execution cancellation handle leaked")
	}
}

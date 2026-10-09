// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// newWaitStore builds a real in-memory task store for waitTask tests.
func newWaitStore(t *testing.T) *core.TaskStore {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return core.NewTaskStore(db, nil)
}

func TestWaitTaskSettles(t *testing.T) {
	store := newWaitStore(t)
	ctx := context.Background()
	task, err := store.Create(ctx, "", "", "demo", "test-node", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Settled-on-arrival: cancel first, waitTask must return immediately.
	if err := store.Cancel(ctx, task.TaskID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	final, err := waitTask(ctx, store, task.TaskID)
	if err != nil {
		t.Fatalf("waitTask: %v", err)
	}
	if final.State != core.StateCancelled {
		t.Fatalf("state = %q, want cancelled", final.State)
	}
}

func TestWaitTaskTimeoutKeepsLastState(t *testing.T) {
	store := newWaitStore(t)
	ctx := context.Background()
	task, err := store.Create(ctx, "", "", "demo", "test-node", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	wctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	final, err := waitTask(wctx, store, task.TaskID)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	// The timeout must still report the state the row was in — an empty
	// State here is what made the CLI print "still  after 3s".
	if final.State == "" {
		t.Fatal("timeout should return the last-read task row")
	}
}

func TestWaitTaskSeesAsyncSettle(t *testing.T) {
	store := newWaitStore(t)
	ctx := context.Background()
	task, err := store.Create(ctx, "", "", "demo", "test-node", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// A consumer finishing the task mid-watch: cancel lands while waitTask
	// is between polls.
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = store.Cancel(ctx, task.TaskID)
	}()
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	final, err := waitTask(wctx, store, task.TaskID)
	if err != nil {
		t.Fatalf("waitTask: %v", err)
	}
	if final.State != core.StateCancelled {
		t.Fatalf("state = %q, want cancelled", final.State)
	}
}

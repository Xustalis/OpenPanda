// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
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

// TestTaskJSONCarriesFullEvents: the terminal timeline clips a failure reason
// to the width of the window. The JSON form is the script-facing answer and
// must keep the whole event payload, or the only way to read the cause is the
// database.
func TestTaskJSONCarriesFullEvents(t *testing.T) {
	store := newWaitStore(t)
	ctx := context.Background()
	task, err := store.Create(ctx, "", "", "demo", "test-node", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const reason = `dsh: MISSING_CREDENTIAL: no credential for provider route "minimax-anthropic" — run dsh auth login`
	if err := store.RecordEvent(ctx, task.TaskID, core.EvResult, map[string]any{"failed": reason}); err != nil {
		t.Fatalf("record event: %v", err)
	}
	j := taskToJSONWithStore(store, task)
	var result *taskEventJSON
	for i := range j.Events {
		if j.Events[i].Type == core.EvResult {
			result = &j.Events[i]
			break
		}
	}
	if result == nil {
		t.Fatalf("no result event in %+v", j.Events)
	}
	var payload struct {
		Failed string `json:"failed"`
	}
	if err := json.Unmarshal(result.Data, &payload); err != nil {
		t.Fatalf("event data is not JSON: %v", err)
	}
	if payload.Failed != reason {
		t.Fatalf("failure reason = %q, want the full %q", payload.Failed, reason)
	}
}

// found: an agent failure leaves the row in StateFailed for the backoff
// window, and the supervisor then retries it. waitTask must not report that
// transient row as the task's final answer.
func TestWaitTaskDoesNotSettleOnRetryableFailure(t *testing.T) {
	store := newWaitStore(t)
	ctx := context.Background()
	task, err := store.Create(ctx, "", "", "demo", "test-node", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	owner := "test-node"
	for _, step := range []error{
		store.Queue(ctx, task.TaskID, owner),
		store.Dispatch(ctx, task.TaskID, owner, owner),
		store.Accept(ctx, task.TaskID, owner),
	} {
		if step != nil {
			t.Fatalf("prepare running: %v", step)
		}
	}
	// The shape a real agent failure records: an exec round, a supervision
	// verdict, then the failed result. The retry event follows only after
	// the backoff, so at this instant the row is failed but not finished.
	if err := store.RecordEvent(ctx, task.TaskID, core.EvExecAgentStart, map[string]any{"agent": "dsh"}); err != nil {
		t.Fatalf("exec event: %v", err)
	}
	if err := store.RecordEvent(ctx, task.TaskID, core.EvSupervisionRound, map[string]any{"verdict_status": "failed"}); err != nil {
		t.Fatalf("supervision event: %v", err)
	}
	if err := store.FailAttempt(ctx, task.TaskID, owner, task.AttemptID, "MISSING_CREDENTIAL"); err != nil {
		t.Fatalf("fail attempt: %v", err)
	}

	wctx, cancel := context.WithTimeout(ctx, 350*time.Millisecond)
	defer cancel()
	final, err := waitTask(wctx, store, task.TaskID)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waitTask returned %v (state %q); a retryable failure must keep waiting", err, final.State)
	}
	if final.State != core.StateFailed {
		t.Fatalf("last state = %q, want failed while the retry is pending", final.State)
	}
}

// TestWaitTaskReturnsPreDispatchFailure: a failure that never started an
// agent run has no retry behind it (a bad actuator spec, for example), so the
// same StateFailed is final and --wait must return it immediately.
func TestWaitTaskReturnsPreDispatchFailure(t *testing.T) {
	store := newWaitStore(t)
	ctx := context.Background()
	task, err := store.Create(ctx, "", "", "demo", "test-node", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.ForceFail(ctx, task.TaskID, `route actuator: missing parameter "title"`); err != nil {
		t.Fatalf("force fail: %v", err)
	}
	wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	final, err := waitTask(wctx, store, task.TaskID)
	if err != nil {
		t.Fatalf("waitTask: %v", err)
	}
	if final.State != core.StateFailed {
		t.Fatalf("state = %q, want failed", final.State)
	}
}

package core

import (
	"context"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/commander"
)

// TestRetryThenReview verifies a task that keeps failing is retried up to the
// loop detector's budget and then paused into review, not left in failed.
func TestRetryThenReview(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "retry-node")
	c.SetWorkDir(t.TempDir())
	c.sleep = func(time.Duration) {} // no-op backoff in tests

	attempts := 0
	c.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		attempts++
		return commander.AgentResult{OK: false, Result: "flaky", ExitCode: 1}
	})

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "flaky",
		Project:     "proj",
		ContextType: "command",
		Intent:      "flaky",
		Requires:    []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit local: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3 (1 initial + 2 retries)", attempts)
	}
	if task.State != StateReview {
		t.Fatalf("state = %s, want review", task.State)
	}
	if result.OK {
		t.Fatalf("result should be not-ok for a reviewed task")
	}
}

// TestRetryThenSuccess verifies a task that fails once then succeeds is retried
// and completes done.
func TestRetryThenSuccess(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "retry-node-ok")
	c.SetWorkDir(t.TempDir())
	c.sleep = func(time.Duration) {} // no-op backoff in tests

	attempts := 0
	c.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		attempts++
		if attempts == 1 {
			return commander.AgentResult{OK: false, Result: "flaky", ExitCode: 1}
		}
		return commander.AgentResult{OK: true, Result: "done", ExitCode: 0}
	})

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "flaky then ok",
		Project:     "proj",
		ContextType: "command",
		Intent:      "flaky then ok",
		Requires:    []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit local: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if task.State != StateDone {
		t.Fatalf("state = %s, want done", task.State)
	}
	if !result.OK {
		t.Fatalf("result not ok: %+v", result)
	}
}

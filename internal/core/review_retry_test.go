// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/commander"
)

func parkedExecutionFailure(t *testing.T, c *Core, reason string, authorized bool) Task {
	t.Helper()
	ctx := context.Background()
	task, err := c.store.Create(ctx, "", "proj", "retry failed execution", c.nodeID, []string{c.nodeID})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error {
			return c.store.SetDetail(ctx, task.TaskID, TaskDetail{Intent: "retry failed execution", Requires: []string{"code:modify"}})
		},
		func() error { return c.store.SetAuthorized(ctx, task.TaskID, authorized) },
		func() error { return c.store.Queue(ctx, task.TaskID, c.nodeID) },
		func() error { return c.store.Dispatch(ctx, task.TaskID, c.nodeID, c.nodeID) },
		func() error { return c.store.Accept(ctx, task.TaskID, c.nodeID) },
		func() error { return c.store.Fail(ctx, task.TaskID, c.nodeID, reason) },
		func() error { return c.store.Review(ctx, task.TaskID, c.nodeID, reason) },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	task, err = c.store.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestRetryReviewedExecutionFailure(t *testing.T) {
	for _, authorized := range []bool{false, true} {
		t.Run(map[bool]string{false: "preserves-no-consent", true: "preserves-consent"}[authorized], func(t *testing.T) {
			ctx := context.Background()
			c := newCoreWithAgent(t, "review-retry")
			work := t.TempDir()
			c.SetWorkDir(work)
			task := parkedExecutionFailure(t, c, "api_retry error 429: rate_limited", authorized)
			if err := c.store.SetWorkDir(ctx, task.TaskID, work); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			c.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
				calls.Add(1)
				if cwd != work {
					t.Errorf("cwd=%q want %q", cwd, work)
				}
				current, err := c.store.Get(ctx, task.TaskID)
				if err != nil || current.Authorized != authorized {
					t.Errorf("consent changed: %+v %v", current, err)
				}
				return commander.AgentResult{OK: true, Result: "done", ExitCode: 0}
			})
			info, err := c.store.ReviewFailure(ctx, task.TaskID)
			if err != nil || !info.RetryAvailable || info.Cause != "rate_limited" || info.Reason != "api_retry error 429: rate_limited" {
				t.Fatalf("info=%+v err=%v", info, err)
			}
			final, result, err := c.RetryReviewed(ctx, task.TaskID)
			if err != nil || final.State != StateDone || !result.OK || calls.Load() != 1 {
				t.Fatalf("retry: state=%s calls=%d result=%+v err=%v", final.State, calls.Load(), result, err)
			}
			if final.AttemptID == task.AttemptID {
				t.Fatal("manual retry reused failed attempt")
			}
			if _, _, err := c.RetryReviewed(ctx, task.TaskID); !errors.Is(err, ErrConflict) {
				t.Fatalf("completed task retry: %v", err)
			}
		})
	}
}

func TestRetryReviewedConcurrentClaim(t *testing.T) {
	c := newCoreWithAgent(t, "review-retry-race")
	c.SetWorkDir(t.TempDir())
	task := parkedExecutionFailure(t, c, "temporary execution failure", true)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	c.router.SetAdapterRunner(func(context.Context, string, string, string) commander.AgentResult {
		calls.Add(1)
		once.Do(func() { close(started) })
		<-release
		return commander.AgentResult{OK: true, Result: "done"}
	})
	done := make(chan error, 1)
	go func() { _, _, err := c.RetryReviewed(context.Background(), task.TaskID); done <- err }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("retry exited before reaching the runner: %v", err)
	case <-time.After(10 * time.Second):
		close(release)
		err := <-done
		t.Fatalf("retry did not reach the runner within 10s; completion after release: %v", err)
	}
	_, _, second := c.RetryReviewed(context.Background(), task.TaskID)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !errors.Is(second, ErrConflict) || calls.Load() != 1 {
		t.Fatalf("duplicate retry: calls=%d err=%v", calls.Load(), second)
	}
}

func TestRetryReviewedRejectsPolicyPark(t *testing.T) {
	c := newCoreWithAgent(t, "review-retry-policy")
	ctx := context.Background()
	task, err := c.store.Create(ctx, "", "proj", "policy gate", c.nodeID, []string{c.nodeID})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error { return c.store.Queue(ctx, task.TaskID, c.nodeID) },
		func() error { return c.store.Dispatch(ctx, task.TaskID, c.nodeID, c.nodeID) },
		func() error { return c.store.Accept(ctx, task.TaskID, c.nodeID) },
		func() error { return c.store.Pause(ctx, task.TaskID, c.nodeID, "api_retry error 429: rate_limited") },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	info, err := c.store.ReviewFailure(ctx, task.TaskID)
	if err != nil || info.RetryAvailable {
		t.Fatalf("policy park misclassified: %+v %v", info, err)
	}
	if _, _, err := c.RetryReviewed(ctx, task.TaskID); !errors.Is(err, ErrConflict) {
		t.Fatalf("policy replay allowed: %v", err)
	}
}

func TestRetryReviewedRefreshesAutomaticBudget(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "review-retry-budget")
	c.SetWorkDir(t.TempDir())
	c.sleep = func(time.Duration) {}
	var calls int
	c.router.SetAdapterRunner(func(context.Context, string, string, string) commander.AgentResult {
		calls++
		return commander.AgentResult{OK: false, Result: "transient execution failure", ExitCode: 1}
	})
	task, _, err := c.SubmitLocal(ctx, TaskInput{Title: "budget fixture", Intent: "budget fixture", Requires: []string{"code:modify"}})
	if err != nil || calls != 3 || task.State != StateReview {
		t.Fatalf("initial failures: calls=%d state=%s err=%v", calls, task.State, err)
	}
	for i := 0; i < 3; i++ {
		c.breaker.RecordFailure("agent:unrelated")
	}
	c.router.SetAdapterRunner(func(context.Context, string, string, string) commander.AgentResult {
		calls++
		if calls < 6 {
			return commander.AgentResult{OK: false, Result: "transient execution failure", ExitCode: 1}
		}
		return commander.AgentResult{OK: true, Result: "recovered"}
	})
	final, _, err := c.RetryReviewed(ctx, task.TaskID)
	if err != nil || calls != 6 || final.State != StateDone {
		t.Fatalf("manual retry lost its fresh budget: calls=%d state=%s err=%v", calls, final.State, err)
	}
	if n, err := c.store.HardRetries(ctx, task.TaskID); err != nil || n != 2 {
		t.Fatalf("new budget count=%d err=%v", n, err)
	}
	if c.breaker.State("agent:unrelated") != "open" {
		t.Fatal("retry cleared an unrelated circuit")
	}
}

func TestRetryReviewedAtomicClaim(t *testing.T) {
	c := newCoreWithAgent(t, "review-retry-cas")
	task := parkedExecutionFailure(t, c, "api_retry error 429: rate_limited", true)
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; results <- c.store.claimReviewRetry(context.Background(), task) }()
	}
	close(start)
	wins, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			wins++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("same-snapshot retry claims: wins=%d conflicts=%d", wins, conflicts)
	}
}

func TestReviewFailureDoesNotReuseEarlierAttempt(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "review-retry-stale")
	task := parkedExecutionFailure(t, c, "api_retry error 429: rate_limited", true)
	if err := c.store.RecordEvent(ctx, task.TaskID, EvRetry, nil); err != nil {
		t.Fatal(err)
	}
	info, err := c.store.ReviewFailure(ctx, task.TaskID)
	if err != nil || info.RetryAvailable {
		t.Fatalf("old failure enabled retry on a newer attempt: %+v %v", info, err)
	}
}

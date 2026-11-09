// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"strings"
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

// TestReviewRestoresRetryBudget pins the reset half of the loop detector, which
// had no call site at all (audit P2-11): the per-task failure count is what
// pauses a task into review, and it used to survive that pause forever. A person
// who looked at the parked task and sent it back therefore got a task with zero
// retries left, which parked itself again on its first failure — and every task
// id that ever failed stayed in the detector's map for the daemon's lifetime.
// Asserting a full fresh budget (max=2 ⇒ two allows, then a refusal) covers both.
func TestReviewRestoresRetryBudget(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "retry-reset-node")
	c.SetWorkDir(t.TempDir())
	c.sleep = func(time.Duration) {}

	c.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		return commander.AgentResult{OK: false, Result: "flaky", ExitCode: 1}
	})

	task, _, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "flaky",
		Project:     "proj",
		ContextType: "command",
		Intent:      "flaky",
		Requires:    []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit local: %v", err)
	}
	if task.State != StateReview {
		t.Fatalf("state = %s, want review", task.State)
	}
	for i := 1; i <= 2; i++ {
		if !c.loop.Allow(task.TaskID) {
			t.Fatalf("retry %d refused: the parked task did not get its budget back", i)
		}
	}
	if c.loop.Allow(task.TaskID) {
		t.Error("the budget is unbounded after a reset; the loop guard is gone")
	}
}

// TestSuccessClearsFailureCount is the other reset path: a task that fails and
// then succeeds must not carry its consumed count forward.
func TestSuccessClearsFailureCount(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "retry-reset-ok")
	c.SetWorkDir(t.TempDir())
	c.sleep = func(time.Duration) {}

	attempts := 0
	c.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		attempts++
		if attempts == 1 {
			return commander.AgentResult{OK: false, Result: "flaky", ExitCode: 1}
		}
		return commander.AgentResult{OK: true, Result: "done", ExitCode: 0}
	})

	task, _, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "flaky then ok",
		Project:     "proj",
		ContextType: "command",
		Intent:      "flaky then ok",
		Requires:    []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit local: %v", err)
	}
	if task.State != StateDone {
		t.Fatalf("state = %s, want done", task.State)
	}
	for i := 1; i <= 2; i++ {
		if !c.loop.Allow(task.TaskID) {
			t.Fatalf("retry %d refused: the completed task kept its spent budget", i)
		}
	}
}

// TestCheckpointRetriesUseSessionBudget verifies the split budget: a run that
// dies on its wall-clock bound WITH a live session (exit 124 + session id) is
// a resumable checkpoint, not a hard failure — it draws the larger checkpoint
// budget (maxCheckpointRetries) instead of parking after the hard-failure
// detector's two allowances, and the backoff stays at the base delay rather
// than doubling toward hour-scale sleeps.
func TestCheckpointRetriesUseSessionBudget(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "checkpoint-node")
	c.SetWorkDir(t.TempDir())

	var delays []time.Duration
	c.sleep = func(d time.Duration) { delays = append(delays, d) }

	attempts := 0
	c.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		attempts++
		return commander.AgentResult{OK: false, Result: "adapter timed out", ExitCode: 124, SessionID: "ses_123"}
	})

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "long work",
		Project:     "proj",
		ContextType: "command",
		Intent:      "long work",
		Requires:    []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit local: %v", err)
	}
	if attempts != maxCheckpointRetries+1 {
		t.Fatalf("attempts = %d, want 1 initial + %d checkpoint retries", attempts, maxCheckpointRetries)
	}
	if task.State != StateReview {
		t.Fatalf("state = %s, want review after the checkpoint budget is spent", task.State)
	}
	// Every retry event carries the checkpoint flag; the hard counter stays 0.
	// (CheckpointRetries' since-last-review window is already closed here —
	// the budget-exhaustion park IS a review — so assert with the totals.)
	if n, _ := c.store.RetryCount(ctx, task.TaskID); n != maxCheckpointRetries {
		t.Fatalf("total retries = %d, want %d", n, maxCheckpointRetries)
	}
	if n, _ := c.store.HardRetries(ctx, task.TaskID); n != 0 {
		t.Fatalf("hard retries = %d, want 0 — checkpoints must not draw the failure budget", n)
	}
	for i, d := range delays {
		if d != c.retryBackoff {
			t.Fatalf("delay[%d] = %v, want the base %v — checkpoint resumes pace flat", i, d, c.retryBackoff)
		}
	}
	_ = result
}

// TestCheckpointRetriesThenHardFailure verifies the budgets stay separate:
// checkpoint timeouts do not draw the hard-failure budget, and when the run
// later fails hard (no session), the ordinary detector applies.
func TestCheckpointRetriesThenHardFailure(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "checkpoint-then-hard")
	c.SetWorkDir(t.TempDir())
	c.sleep = func(time.Duration) {}

	attempts := 0
	c.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		attempts++
		if attempts <= 3 {
			// Three timed-out rounds — past the hard budget already — all
			// resumable checkpoints.
			return commander.AgentResult{OK: false, Result: "timed out", ExitCode: 124, SessionID: "ses_x"}
		}
		// Then a genuine failure: the session-less error draws the hard
		// budget, whose two allowances are still unspent.
		return commander.AgentResult{OK: false, Result: "flaky", ExitCode: 1}
	})

	task, _, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "long then broken",
		Project:     "proj",
		ContextType: "command",
		Intent:      "long then broken",
		Requires:    []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit local: %v", err)
	}
	// 3 checkpoints + 1 initial hard failure + 2 hard retries = 6 runs.
	if attempts != 6 {
		t.Fatalf("attempts = %d, want 3 checkpoints + 1 + 2 hard retries", attempts)
	}
	if task.State != StateReview {
		t.Fatalf("state = %s, want review", task.State)
	}
	// Same review-window caveat as above: totals say 3 checkpoint + 2 hard.
	if n, _ := c.store.RetryCount(ctx, task.TaskID); n != 5 {
		t.Fatalf("total retries = %d, want 5 (3 checkpoint + 2 hard)", n)
	}
	if n, _ := c.store.HardRetries(ctx, task.TaskID); n != 2 {
		t.Fatalf("hard retries = %d, want 2 — the hard budget was untouched by checkpoints", n)
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

// TestDelegatedRunSettlesThroughRetryLoop pins the executor-side gap found in
// production: a peer-run task whose agent stalled with a live session used to
// report failed after exactly one attempt — proceedLocalDelegate called
// execute once and the shared retry/checkpoint machinery never saw it.
// settleDelegated must checkpoint-resume the row exactly as a local failure
// would, and the payload it hands back must carry the settled outcome.
func TestDelegatedRunSettlesThroughRetryLoop(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "exec-node")
	c.SetWorkDir(t.TempDir())
	c.sleep = func(time.Duration) {}

	attempts := 0
	c.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		attempts++
		if attempts == 1 {
			return commander.AgentResult{OK: false, Result: "adapter stalled: silence timeout", ExitCode: 124, SessionID: "ses_exec"}
		}
		return commander.AgentResult{OK: true, Result: "resumed and done", ExitCode: 0}
	})

	// Executor-side row exactly as handleDelegate persists it: the delegator
	// named this node, owner = self, chain ends at this node.
	tk, err := c.store.CreateWithID(ctx, "01delegatedsettle", "", "proj", "delegated work",
		c.nodeID, []string{"origin-node", c.nodeID}, true)
	if err != nil {
		t.Fatalf("create delegated row: %v", err)
	}

	result, err := c.execute(ctx, tk.TaskID, "delegated work", []string{"code:modify"}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.OK {
		t.Fatalf("first attempt unexpectedly succeeded")
	}

	final, payload, serr := c.settleDelegated(ctx, tk.TaskID, "delegated work", []string{"code:modify"}, result)
	if serr != nil {
		t.Fatalf("settleDelegated: %v", serr)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 1 initial + 1 checkpoint resume", attempts)
	}
	if final.State != StateDone {
		t.Fatalf("state = %s, want done after checkpoint resume", final.State)
	}
	if !payload.OK || payload.State == StateFailed {
		t.Fatalf("reply payload = %+v — the delegator must see the resumed outcome, not the first stall", payload)
	}
}

// TestDelegatedRunReviewMirrorsUpstream pins the second half of the settle
// contract: when the executor's retry budget is spent the park reports
// State:review, so the delegator mirrors the park (ReviewFromRemote) and its
// operator can approve the resume — instead of a terminal failure upstream
// while the executor copy waits in a review queue nobody can reach.
func TestDelegatedRunReviewMirrorsUpstream(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "exec-node-review")
	c.SetWorkDir(t.TempDir())
	c.sleep = func(time.Duration) {}

	c.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		return commander.AgentResult{OK: false, Result: "deterministically broken", ExitCode: 1}
	})

	tk, err := c.store.CreateWithID(ctx, "01delegatedreview", "", "proj", "delegated work",
		c.nodeID, []string{"origin-node", c.nodeID}, true)
	if err != nil {
		t.Fatalf("create delegated row: %v", err)
	}

	result, err := c.execute(ctx, tk.TaskID, "delegated work", []string{"code:modify"}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	final, payload, serr := c.settleDelegated(ctx, tk.TaskID, "delegated work", []string{"code:modify"}, result)
	if serr != nil {
		t.Fatalf("settleDelegated: %v", serr)
	}
	if final.State != StateReview {
		t.Fatalf("state = %s, want review after the retry budget is spent", final.State)
	}
	if payload.State != StateReview {
		t.Fatalf("payload state = %q, want review — a bare failure parks the executor copy where the delegator cannot reach it", payload.State)
	}
	if payload.ApprovalDisposition == "" {
		t.Fatalf("payload carries no approval disposition — the delegator cannot tell what approving would do")
	}
}

// TestAdapterUnavailableFailsWithoutRetrying pins the other half of the retry
// contract: a task whose agent could not start at all is not "flaky work" and
// not "a human should judge this output". The script is missing on this node,
// so every attempt reads the same filesystem and dies identically — retrying
// burns the budget, and parking in review files a broken install as a work
// quality question. It settles failed on the first attempt, with the reason
// the spawn site wrote, and the diagnostic says which category it is.
func TestAdapterUnavailableFailsWithoutRetrying(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "adapter-unavailable-node")
	c.SetWorkDir(t.TempDir())
	c.sleep = func(time.Duration) {}

	attempts := 0
	c.router.SetAdapterRunner(func(context.Context, string, string, string) commander.AgentResult {
		attempts++
		return commander.AgentResult{OK: false, ExitCode: 1, Result: `adapter unavailable: "claude_code.py" is not at ` +
			`/opt/x/adapters/claude_code.py (adapter dir /opt/x/adapters — reinstall the adapters or set OPENPANDA_ADAPTER_DIR)`}
	})

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title: "missing adapter", Project: "proj", ContextType: "command",
		Intent: "do the work", Requires: []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit local: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 — a missing adapter cannot be retried into existence", attempts)
	}
	if task.State != StateFailed {
		t.Fatalf("state = %s, want failed (review would file an install bug as a work verdict)", task.State)
	}
	if result.OK {
		t.Fatal("result should report the failure")
	}
	if !strings.Contains(result.Stderr, "adapter unavailable") {
		t.Fatalf("stderr = %q, want the adapter diagnosis preserved", result.Stderr)
	}

	events, err := c.store.Events(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	assessed := false
	for _, e := range events {
		if e.Type != "recovery_assessment" {
			continue
		}
		var data struct {
			Assessment RecoveryAssessment `json:"assessment"`
		}
		if err := json.Unmarshal([]byte(e.DataJSON), &data); err != nil {
			t.Fatalf("decode assessment: %v", err)
		}
		if data.Assessment.Category != "adapter_unavailable" || data.Assessment.Strategy != "stop" {
			t.Fatalf("assessment = %+v, want adapter_unavailable/stop", data.Assessment)
		}
		assessed = true
	}
	if !assessed {
		t.Fatal("no recovery_assessment event recorded for the failed task")
	}
}

// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

const testStagnationReason = "agent claude_code stalled: identical output across consecutive rounds with no alternate agents"

func TestStagnationReasonAcceptsSpacedAgentNames(t *testing.T) {
	spaced := "agent claude code stalled: identical output across consecutive rounds with no alternate agents"
	if !isStagnationReason(spaced) {
		t.Fatalf("a configured agent name may contain spaces: %q", spaced)
	}
	if isStagnationReason("agent  stalled: identical output across consecutive rounds with no alternate agents") {
		t.Fatal("an empty agent name is not a named-agent stall")
	}
	if isStagnationReason("agent one\nagent two stalled: identical output across consecutive rounds with no alternate agents") {
		t.Fatal("a name carrying a line break is not a single agent")
	}
}

func TestStagnationReviewOffersExplicitContinuation(t *testing.T) {
	ctx := context.Background()
	c := NewCore(openTestDB(t), "node-1", ledger.Card{
		Device:        "node-1",
		ResourceClass: "Standard",
		Agents: map[string]ledger.Agent{
			"claude_code": {Adapter: "claude_code.py", Capabilities: []string{"coding"}, Tier: 1},
		},
		Capacity: ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())
	c.SetSharedSecret(testSharedSecret)
	c.router.SetAgentProber(func(string, ledger.Agent) bool { return true })
	c.SetSupervisor(newFakeSupervisor(t, func(int) string {
		return `{"status":"continue","reason":"missing output","followup":"finish the task"}`
	}))
	calls := 0
	c.router.SetAdapterRunner(func(context.Context, string, string, string) commander.AgentResult {
		calls++
		return commander.AgentResult{OK: true, Result: "repeated reply"}
	})
	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title: "make an output file", ContextType: "command", Intent: "create a PNG", Requires: []string{"coding"},
	})
	if err != nil || calls != 2 || task.State != StateReview || !result.NeedsFollowup || !task.NeedsFollowup() {
		t.Fatalf("run: state=%s calls=%d followup=%v err=%v", task.State, calls, result.NeedsFollowup, err)
	}
	failure, err := c.store.ReviewFailure(ctx, task.TaskID)
	if err != nil || failure.Cause != "stagnation" || failure.Reason != testStagnationReason || failure.RetryAvailable {
		t.Fatalf("review cause: %+v, err=%v", failure, err)
	}
	resumed, err := c.ContinueReviewed(ctx, task.TaskID, "Try a different way")
	if err != nil || resumed.State != StateQueued || resumed.NeedsFollowup() || calls != 2 {
		t.Fatalf("continue: state=%s calls=%d err=%v", resumed.State, calls, err)
	}
}

func TestLegacyStagnationReviewContinuation(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"needs_followup": false, "verdict": "review", "verdict_reason": testStagnationReason,
	})
	if err != nil {
		t.Fatal(err)
	}
	initial := Task{State: StateReview, ApprovalDisposition: ApprovalAcceptWork, ResultJSON: string(raw)}
	if !initial.NeedsFollowup() {
		t.Fatal("previously stored stagnation should offer continuation")
	}
	checks := map[string]Task{
		"approval":        {State: StateReview, ApprovalDisposition: ApprovalResumeExecution, ResultJSON: string(raw)},
		"ordinary review": {State: StateReview, ApprovalDisposition: ApprovalAcceptWork, ResultJSON: `{"verdict":"review","verdict_reason":"judge unavailable"}`},
		"completed":       {State: StateDone, ApprovalDisposition: ApprovalAcceptWork, ResultJSON: string(raw)},
	}
	for name, task := range checks {
		if task.NeedsFollowup() {
			t.Errorf("%s should not offer continuation", name)
		}
	}
}

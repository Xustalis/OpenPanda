// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/contract"
	"github.com/Xustalis/OpenPanda/internal/entry"
)

// runToRunning creates a task and walks it through the states a run goes
// through, so the terminal Complete write in finalizeRun is legal.
func runToRunning(t *testing.T, c *Core, taskID, title string) Task {
	t.Helper()
	ctx := context.Background()
	if _, err := c.store.CreateWithID(ctx, taskID, "", "", title, c.nodeID, nil, false); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := c.store.Queue(ctx, taskID, c.nodeID); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.Dispatch(ctx, taskID, c.nodeID, c.nodeID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := c.store.Accept(ctx, taskID, c.nodeID); err != nil {
		t.Fatalf("accept: %v", err)
	}
	task, err := c.store.Get(ctx, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	return task
}

// finalizeWith drives finalizeRun once and returns both the payload and the
// persisted row, asserting the two agree on the verification label.
func finalizeWith(t *testing.T, c *Core, task Task, plan commander.Plan, unverified bool) (Task, bus.TaskResultPayload) {
	t.Helper()
	ctx := context.Background()
	payload, err := c.finalizeRun(ctx, task.TaskID, task, plan, nil, task.AttemptID, t.TempDir(),
		commander.Result{ExitCode: 0, Stdout: "did the work", Agent: "claude"}, nil, false,
		entry.SuperviseVerdict{Status: entry.VerdictDone}, unverified,
		"", "", nil, nil)
	if err != nil {
		t.Fatalf("finalizeRun: %v", err)
	}
	row, err := c.store.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("re-read task: %v", err)
	}
	if payload.Verification != row.Verification {
		t.Fatalf("payload verification %q != row verification %q", payload.Verification, row.Verification)
	}
	return row, payload
}

// TestFinalizeRunLabelsUnverifiedWork pins the honest label for an agent run
// that nothing checked: no judge, no contract. Before this, the result said
// plain "done" and every delegator read it as verified.
func TestFinalizeRunLabelsUnverifiedWork(t *testing.T) {
	c, _, _ := projectCore(t)
	row, payload := finalizeWith(t, c, runToRunning(t, c, "verify-"+t.Name(), "verify me"),
		commander.Plan{Kind: "agent", Tier: 1}, true)

	if row.State != StateDone {
		t.Fatalf("state = %q, want done", row.State)
	}
	if row.Verification != contract.VerdictUnverified {
		t.Fatalf("verification = %q, want %q", row.Verification, contract.VerdictUnverified)
	}
	// The wire flag most consumers render as a checkmark must stay false: a
	// check that never ran may not be reported as one that passed.
	if payload.Verified {
		t.Fatal("unverified result carried the verified flag")
	}
}

// TestFinalizeRunKeepsEmptyVerificationForPlainRuns guards the other side: a
// run that was neither judged nor labelled must not gain a verification value
// out of nowhere, or every pre-existing native task starts reading as
// "unverified" instead of "no contract".
func TestFinalizeRunKeepsEmptyVerificationForPlainRuns(t *testing.T) {
	c, _, _ := projectCore(t)
	row, _ := finalizeWith(t, c, runToRunning(t, c, "plain-"+t.Name(), "plain"),
		commander.Plan{Kind: "native", Tier: 1}, false)
	if row.Verification != "" {
		t.Fatalf("verification = %q, want empty", row.Verification)
	}
}

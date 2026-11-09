// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/contract"
)

// acceptanceTask submits one task whose spec carries the given acceptance
// checks, with a fake agent that claims success without touching the tree.
func acceptanceTask(t *testing.T, checks string) (*Core, Task, bool) {
	t.Helper()
	ctx := context.Background()
	c := newCoreWithTwoAgents(t)
	c.router.SetAdapterRunner(func(context.Context, string, string, string) commander.AgentResult {
		return commander.AgentResult{OK: true, Result: "done", ExitCode: 0}
	})
	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "acceptance",
		ContextType: "command",
		Intent:      "do the work",
		SpecJSON:    checks,
		Requires:    []string{"code:modify"},
		Complexity:  0.1,
		Risk:        "low",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	// The returned row is the terminal one; re-read it so the test sees the
	// verification column the completion wrote.
	stored, gerr := c.store.Get(ctx, task.TaskID)
	if gerr != nil {
		t.Fatalf("get task: %v", gerr)
	}
	return c, stored, result.OK
}

// TestAcceptanceVerifiesCleanWork: a contract whose checks pass turns an agent
// claim into evidence — the row is verified and both phases are recorded.
func TestAcceptanceVerifiesCleanWork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("acceptance fixture uses sh")
	}
	c, task, ok := acceptanceTask(t, `{"acceptance":[{"id":"ok","run":"sh -c 'exit 0'"}]}`)
	if !ok || task.State != StateDone {
		t.Fatalf("state=%s ok=%v, want done", task.State, ok)
	}
	if task.Verification != contract.VerdictVerified {
		t.Fatalf("verification = %q, want verified", task.Verification)
	}
	rows, err := c.store.Checks(context.Background(), task.TaskID)
	if err != nil {
		t.Fatalf("checks: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want baseline + final", rows)
	}
}

// TestAcceptanceFailsDishonestClaim is the P0 exit criterion: an agent that
// says it is done while the contract fails must leave a failed task, not a
// green one.
func TestAcceptanceFailsDishonestClaim(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("acceptance fixture uses sh")
	}
	ctx := context.Background()
	c := newCoreWithTwoAgents(t)
	c.router.SetAdapterRunner(func(context.Context, string, string, string) commander.AgentResult {
		return commander.AgentResult{OK: true, Result: "all tests pass, trust me", ExitCode: 0}
	})
	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "dishonest",
		ContextType: "command",
		Intent:      "do the work",
		SpecJSON:    `{"acceptance":[{"id":"tests","run":"sh -c 'exit 3'"}]}`,
		Requires:    []string{"code:modify"},
		Complexity:  0.1,
		Risk:        "low",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.State != StateFailed {
		t.Fatalf("state = %s, want failed (result=%+v)", task.State, result)
	}
	if result.OK {
		t.Fatalf("payload OK = true, want false: %+v", result)
	}
	if !strings.Contains(result.Stderr, "acceptance failed") || !strings.Contains(result.Stderr, "tests") {
		t.Fatalf("failure must name the failed check: %q", result.Stderr)
	}
	stored, _ := c.store.Get(ctx, task.TaskID)
	if stored.Verification != contract.VerdictFailed {
		t.Fatalf("verification = %q, want failed", stored.Verification)
	}
	rows, err := c.store.Checks(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("checks: %v", err)
	}
	var baseline, final int
	for _, r := range rows {
		switch r.Phase {
		case "baseline":
			baseline++
		case "final":
			final++
		}
	}
	if baseline != 1 || final != 1 {
		t.Fatalf("rows = %+v, want one baseline and one final", rows)
	}
}

// TestAcceptanceNamesPreExistingFailure: a suite that was already red is named
// as such, both in the verdict reasons and in the failure text, so the user
// can tell "inherited" from "broke it".
func TestAcceptanceNamesPreExistingFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("acceptance fixture uses sh")
	}
	ctx := context.Background()
	c := newCoreWithTwoAgents(t)
	c.router.SetAdapterRunner(func(context.Context, string, string, string) commander.AgentResult {
		return commander.AgentResult{OK: true, Result: "done", ExitCode: 0}
	})
	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "pre-existing red",
		ContextType: "command",
		Intent:      "do the work",
		SpecJSON:    `{"acceptance":[{"id":"suite","run":"sh -c 'exit 2'"}]}`,
		Requires:    []string{"code:modify"},
		Complexity:  0.1,
		Risk:        "low",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.State != StateFailed {
		t.Fatalf("state = %s, want failed", task.State)
	}
	if !strings.Contains(result.Stderr, "already failing at baseline") {
		t.Fatalf("failure must attribute the baseline failure: %q", result.Stderr)
	}
}

// TestNoContractIsNotVerified: without a contract the task may still be done,
// but the row must not claim verification. With no supervisor either, nothing
// checked the run at all, so the label is the explicit "unverified" — an empty
// string read as "no contract" and said nothing about whether anyone looked.
func TestNoContractIsNotVerified(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithTwoAgents(t)
	c.router.SetAdapterRunner(func(context.Context, string, string, string) commander.AgentResult {
		return commander.AgentResult{OK: true, Result: "done", ExitCode: 0}
	})
	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "no contract",
		ContextType: "command",
		Intent:      "do the work",
		Requires:    []string{"code:modify"},
		Complexity:  0.1,
		Risk:        "low",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.State != StateDone || !result.OK {
		t.Fatalf("state = %s ok=%v, want done", task.State, result.OK)
	}
	if task.Verification != contract.VerdictUnverified {
		t.Fatalf("verification = %q, want %q (no contract, no judge)", task.Verification, contract.VerdictUnverified)
	}
	if result.Verified {
		t.Fatal("a run nobody checked must not carry the verified flag")
	}
	rows, err := c.store.Checks(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("checks: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
}

// TestTaskSpecEnvelopeCarriesAcceptance: the agent prompt must state the exact
// checks it will be judged against. A contract the agent cannot see is a trap,
// not a criterion.
func TestTaskSpecEnvelopeCarriesAcceptance(t *testing.T) {
	got := taskSpecEnvelope(`{"success_definition":"tests pass","acceptance":[{"id":"t","run":"make test"},{"id":"a","exists":["dist/app"]}]}`)
	for _, want := range []string{"make test", "dist/app", "run these acceptance checks"} {
		if !strings.Contains(got, want) {
			t.Fatalf("envelope missing %q:\n%s", want, got)
		}
	}
}

// TestAcceptanceSkipsIrreversibleCommands: a contract is evidence, not a
// second way to execute. A check whose command scores Tier-2 never runs, is
// recorded as skipped with the reason, and cannot verify the task.
func TestAcceptanceSkipsIrreversibleCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("acceptance fixture uses sh")
	}
	c, task, _ := acceptanceTask(t, `{"acceptance":[{"id":"danger","run":"rm -rf build"}]}`)
	rows, err := c.store.Checks(context.Background(), task.TaskID)
	if err != nil {
		t.Fatalf("checks: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no check rows recorded")
	}
	for _, r := range rows {
		if r.Status != string(contract.StatusSkipped) {
			t.Fatalf("check %s status = %q, want skipped", r.CheckID, r.Status)
		}
		if !strings.Contains(r.Reason, "irreversible") {
			t.Fatalf("check reason = %q, want it to name the refusal", r.Reason)
		}
	}
	if task.Verification == contract.VerdictVerified {
		t.Fatal("a contract whose only check never ran must not verify the task")
	}
}

// TestStripPeerAcceptance: a delegated task's checks were authored elsewhere
// and would run here, outside the tier gate — they are dropped at the wire
// boundary, and nothing else in the spec changes.
func TestStripPeerAcceptance(t *testing.T) {
	in := `{"title":"x","acceptance":[{"id":"a","run":"make test"}]}`
	out, dropped := stripPeerAcceptance(in)
	if !dropped {
		t.Fatal("acceptance not detected")
	}
	if strings.Contains(out, "acceptance") {
		t.Fatalf("acceptance survived: %s", out)
	}
	if !strings.Contains(out, `"title":"x"`) {
		t.Fatalf("other fields lost: %s", out)
	}
	if _, d := stripPeerAcceptance(`{"title":"x"}`); d {
		t.Fatal("a spec without acceptance reported a drop")
	}
	if got, d := stripPeerAcceptance(""); d || got != "" {
		t.Fatal("empty spec must pass through untouched")
	}
}

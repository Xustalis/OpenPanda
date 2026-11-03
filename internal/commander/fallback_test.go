// SPDX-License-Identifier: AGPL-3.0-or-later

package commander

import (
	"context"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// twoAgentCard is a node with two interchangeable coding agents. Their equal
// cost tiers make the ranking deterministic (ties break by name), so "alpha"
// is the primary and "beta" the alternate.
func twoAgentCard() ledger.Card {
	return ledger.Card{
		Device:        "test-node",
		ResourceClass: "Standard",
		Agents: map[string]ledger.Agent{
			"alpha": {Adapter: "alpha.py", Capabilities: []string{"coding"}, CostTier: "low"},
			"beta":  {Adapter: "beta.py", Capabilities: []string{"coding"}, CostTier: "low"},
		},
		Capacity: ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
}

func newTwoAgentRouter() *Router {
	r := NewRouter(twoAgentCard(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	r.SetAgentProber(func(string, ledger.Agent) bool { return true })
	return r
}

// TestPinnedAgentGetsNoAlternates: a task that says "agent:<name>" is a task
// about that agent. Staffing the fallback chain with every other coding agent
// turned a failed claude_code run into a green deepseek_harness pass — the
// reported agent never started, and the substitution read as success.
func TestPinnedAgentGetsNoAlternates(t *testing.T) {
	r := newTwoAgentRouter()
	plan, err := r.Route([]string{"agent:alpha"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Agent != "alpha" {
		t.Fatalf("primary = %q, want alpha", plan.Agent)
	}
	if len(plan.Alternates) != 0 {
		t.Fatalf("a pinned plan must carry no alternates, got %v", plan.Alternates)
	}

	var ran []string
	r.runAdapter = func(_ context.Context, adapter, _, _ string) AgentResult {
		ran = append(ran, adapter)
		return AgentResult{OK: false, Result: "Not logged in · Please run /login", ExitCode: 1}
	}
	res := r.Execute(context.Background(), plan, "run", "", false)
	if res.OK {
		t.Fatalf("pinned agent failure must not become success: %+v", res)
	}
	if len(ran) != 1 || ran[0] != "alpha.py" {
		t.Fatalf("only the pinned agent may run, ran=%v", ran)
	}
	if res.Agent != "alpha" {
		t.Fatalf("result agent = %q, want the pinned agent", res.Agent)
	}
}

// TestUnpinnedRouteFallsBackOnDispatchFailure: a run that never started (the
// CLI is not signed in) may try the next candidate — that is what alternates
// are for.
func TestUnpinnedRouteFallsBackOnDispatchFailure(t *testing.T) {
	r := newTwoAgentRouter()
	plan, err := r.Route([]string{"coding"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Agent != "alpha" || len(plan.Alternates) != 1 || plan.Alternates[0] != "beta" {
		t.Fatalf("plan = %+v, want alpha then beta", plan)
	}
	var ran []string
	r.runAdapter = func(_ context.Context, adapter, _, _ string) AgentResult {
		ran = append(ran, adapter)
		if adapter == "alpha.py" {
			return AgentResult{OK: false, Result: "Not logged in · Please run /login", ExitCode: 1}
		}
		return AgentResult{OK: true, Result: "done", ExitCode: 0}
	}
	res := r.Execute(context.Background(), plan, "run", "", false)
	if !res.OK || res.Agent != "beta" {
		t.Fatalf("dispatch failure should fall back to beta: %+v", res)
	}
	if len(ran) != 2 {
		t.Fatalf("ran=%v, want both candidates", ran)
	}
}

// TestTaskFailureDoesNotFallBack: the agent ran the task and reported a task
// failure. Re-running that work on a different CLI duplicates its side effects
// and misattributes the outcome, so the failure is returned as-is.
func TestTaskFailureDoesNotFallBack(t *testing.T) {
	r := newTwoAgentRouter()
	plan, err := r.Route([]string{"coding"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	var ran []string
	r.runAdapter = func(_ context.Context, adapter, _, _ string) AgentResult {
		ran = append(ran, adapter)
		return AgentResult{OK: false, Result: "3 tests failed: TestA, TestB, TestC", ExitCode: 1, Tokens: 1200}
	}
	res := r.Execute(context.Background(), plan, "run", "", false)
	if res.OK {
		t.Fatalf("task failure must stay a failure: %+v", res)
	}
	if len(ran) != 1 {
		t.Fatalf("a task-level failure must not re-run elsewhere, ran=%v", ran)
	}
	if res.Agent != "alpha" {
		t.Fatalf("result agent = %q, want the agent that actually ran", res.Agent)
	}
}

// TestAgentFallbackEligibility pins the dispatch/task split directly, including
// the empty-diagnosis case that used to produce "exec failed: " with nothing
// after it.
func TestAgentFallbackEligibility(t *testing.T) {
	cases := []struct {
		name string
		ar   AgentResult
		want bool
	}{
		{"not signed in", AgentResult{Result: "Not logged in · Please run /login", ExitCode: 1}, true},
		{"out of funds", AgentResult{Result: "provider failure: Upstream request failed: Insufficient account funds", ExitCode: 1}, true},
		{"binary missing", AgentResult{Result: "claude binary not found", ExitCode: 127}, true},
		{"unknown model", AgentResult{Result: "unrecognized_model: deepseek-v9", ExitCode: 1}, true},
		// dsh words its credential failure with underscores and a singular
		// "no credential for provider"; the old plural/past-tense patterns
		// missed it and left the task retrying the same locked-out CLI.
		{"dsh missing credential", AgentResult{Result: `dsh: MISSING_CREDENTIAL: no credential for provider route "minimax"`, ExitCode: 1}, true},
		{"no credential for provider", AgentResult{Result: "no credential for provider route", ExitCode: 1}, true},
		{"opencode service start timeout", AgentResult{Result: "Timed out waiting for the background service to start", ExitCode: 1}, true},
		{"agent could not run", AgentResult{Result: "opencode could not run in this environment", ExitCode: 1}, true},
		{"silent failure", AgentResult{ExitCode: 1}, true},
		{"task failure", AgentResult{Result: "build failed: undefined reference to main", ExitCode: 1}, false},
		{"task failure with tokens", AgentResult{Result: "rate limit exceeded in the target service test", ExitCode: 1, Tokens: 900}, false},
		{"timeout", AgentResult{Result: "adapter timed out (hard limit)", ExitCode: 124}, false},
		{"success", AgentResult{OK: true, Result: "done"}, false},
	}
	for _, tc := range cases {
		if got := agentFallbackEligible(tc.ar); got != tc.want {
			t.Errorf("%s: agentFallbackEligible = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestDispatchFailureCooldownSkipsAgent pins the cross-attempt half of the
// dispatch-failure fix: once an agent was just shown to be unrunnable, routing
// must not hand it the next attempt (or the next task) while a healthy
// candidate exists. A successful run clears the cooldown.
func TestDispatchFailureCooldownSkipsAgent(t *testing.T) {
	r := newTwoAgentRouter()
	alpha := r.card.Agents["alpha"]
	if !r.agentDispatchable("alpha", alpha) {
		t.Fatal("a fresh agent should be dispatchable")
	}
	r.markAgentDispatchFailure("alpha", "MISSING_CREDENTIAL: no credential for provider")
	if r.agentDispatchable("alpha", alpha) {
		t.Fatal("an agent in dispatch cooldown must be skipped")
	}
	if _, why := r.AgentUsableReason("alpha", alpha); !strings.Contains(why, "recent dispatch failure") {
		t.Fatalf("reason = %q, want the cooldown diagnosis", why)
	}
	if !r.agentDispatchable("beta", r.card.Agents["beta"]) {
		t.Fatal("another agent must not inherit the cooldown")
	}
	r.clearAgentDispatchFailure("alpha")
	if !r.agentDispatchable("alpha", alpha) {
		t.Fatal("clearing the cooldown must restore the agent")
	}
}

// TestRouteSkipsCoolingAgent pins the routing half: a candidate in dispatch
// cooldown is not chosen even when the injected prober would admit it, so a
// supervisor retry lands on a healthy alternate instead of the same CLI.
func TestRouteSkipsCoolingAgent(t *testing.T) {
	r := newTwoAgentRouter()
	r.markAgentDispatchFailure("alpha", "MISSING_CREDENTIAL")
	plan, err := r.Route([]string{"coding"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Agent != "beta" {
		t.Fatalf("primary = %q, want beta while alpha is cooling down", plan.Agent)
	}
	// alpha may remain as a last-resort alternate: a node with one capable
	// agent must stay routable. The point is that a healthy candidate leads.
}

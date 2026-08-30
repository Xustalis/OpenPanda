package commander

import (
	"context"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// scoreCard carries two agents that both satisfy code:modify at different
// cost tiers, plus a web-only agent that never matches code work.
func scoreCard() ledger.Card {
	return ledger.Card{
		Device:        "test-node",
		ResourceClass: "Standard",
		Agents: map[string]ledger.Agent{
			"expensive": {Adapter: "claude_code.py", Capabilities: []string{"code:modify"}, CostTier: "high"},
			"cheap":     {Adapter: "opencode.py", Capabilities: []string{"code:modify"}, CostTier: "low"},
			"web":       {Adapter: "opencode.py", Capabilities: []string{"web:search"}, CostTier: "low"},
		},
	}
}

// TestRankAgentsCostDiscount verifies cheaper agents win when capability
// matches are equal: score = match × cost discount.
func TestRankAgentsCostDiscount(t *testing.T) {
	r := NewRouter(scoreCard(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	cands := r.RankAgents([]string{"code:modify"})
	if len(cands) != 2 {
		t.Fatalf("candidates = %d, want 2", len(cands))
	}
	if cands[0].Name != "cheap" {
		t.Fatalf("top = %q, want cheap (low cost tier wins)", cands[0].Name)
	}
	if cands[0].Score <= cands[1].Score {
		t.Fatalf("scores not descending: %+v", cands)
	}
	// The web-only agent never matches code:modify.
	for _, c := range cands {
		if c.Name == "web" {
			t.Fatalf("unrelated agent ranked: %+v", c)
		}
	}
}

// TestRankAgentsPreferredBonus verifies routing.preferred_agents (+0.5)
// outweighs a one-step cost difference (0.8 vs 0.7 multiplier).
func TestRankAgentsPreferredBonus(t *testing.T) {
	card := scoreCard()
	card.Agents["expensive"] = ledger.Agent{Adapter: "claude_code.py", Capabilities: []string{"code:modify"}, CostTier: "medium_high"}
	r := NewRouter(card, NewExecutor(), config.ModelConfig{},
		config.InjectionConfig{}, config.RoutingConfig{PreferredAgents: []string{"expensive"}})
	cands := r.RankAgents([]string{"code:modify"})
	if cands[0].Name != "expensive" {
		t.Fatalf("preferred agent should outrank cheaper one: %+v", cands)
	}
}

// TestRankAgentsExactBeatsTokenMatch: an exact capability string scores
// higher (1.0) than a token-subset bridge (0.9).
func TestRankAgentsExactBeatsTokenMatch(t *testing.T) {
	card := ledger.Card{Agents: map[string]ledger.Agent{
		"a": {Adapter: "a.py", Capabilities: []string{"code:lint"}, CostTier: "medium"},
		"b": {Adapter: "b.py", Capabilities: []string{"lint"}, CostTier: "medium"},
	}}
	r := NewRouter(card, NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	cands := r.RankAgents([]string{"code:lint"})
	if len(cands) != 2 || cands[0].Name != "a" {
		t.Fatalf("exact match should rank first: %+v", cands)
	}
}

// TestRankAgentsPinnedByName: an explicit agent:<name> requirement pins that
// agent and never fans out to substitutes.
func TestRankAgentsPinnedByName(t *testing.T) {
	r := NewRouter(scoreCard(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	cands := r.RankAgents([]string{"agent:expensive", "code:modify"})
	if len(cands) != 1 || cands[0].Name != "expensive" {
		t.Fatalf("pinned agent must be the only candidate: %+v", cands)
	}
}

// TestRouteCarriesAlternates: Route surfaces the runner-up agents as the
// fallback chain on the plan.
func TestRouteCarriesAlternates(t *testing.T) {
	r := NewRouter(scoreCard(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	plan, err := r.Route([]string{"code:modify"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Agent != "cheap" || len(plan.Alternates) != 1 || plan.Alternates[0] != "expensive" {
		t.Fatalf("plan = %+v, want cheap with expensive alternate", plan)
	}
}

// TestExecAgentFallback: the primary agent's CLI is unavailable, so the
// runner-up executes instead and Result.Agent names it.
func TestExecAgentFallback(t *testing.T) {
	card := scoreCard()
	card.Agents["cheap"] = ledger.Agent{Adapter: "opencode.py", Capabilities: []string{"code:modify"}, CostTier: "low", Tier: 1}
	card.Agents["expensive"] = ledger.Agent{Adapter: "claude_code.py", Capabilities: []string{"code:modify"}, CostTier: "high", Tier: 1}
	r := NewRouter(card, NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	r.SetAgentProber(func(name string, _ ledger.Agent) bool { return name != "cheap" })
	var ran []string
	r.SetAdapterRunner(func(_ context.Context, adapter, _, _ string) AgentResult {
		ran = append(ran, adapter)
		return AgentResult{OK: true, Result: "done", ExitCode: 0}
	})

	plan, err := r.Route([]string{"code:modify"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Agent != "cheap" {
		t.Fatalf("primary = %q, want cheap", plan.Agent)
	}
	res := r.Execute(context.Background(), plan, "fix it", "", false)
	if !res.OK || res.Agent != "expensive" {
		t.Fatalf("fallback result = %+v, want ok via expensive", res)
	}
	if len(ran) != 1 || ran[0] != "claude_code.py" {
		t.Fatalf("adapter runs = %v, want exactly claude_code.py", ran)
	}
}

// TestExecAgentPerTaskToolsPolicyWins: a per-task tools policy set on the
// execution context (task spec override) reaches the adapter request, and the
// router's global policy must not overwrite it.
func TestExecAgentPerTaskToolsPolicyWins(t *testing.T) {
	card := scoreCard()
	card.Agents["cheap"] = ledger.Agent{Adapter: "opencode.py", Capabilities: []string{"code:modify"}, CostTier: "low", Tier: 1}
	// Global policy is extended; the per-task override says minimal.
	r := NewRouter(card, NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{ToolsPolicy: config.ToolsPolicyExtended})
	r.SetAgentProber(func(string, ledger.Agent) bool { return true })
	var gotPolicy string
	r.SetAdapterRunner(func(ctx context.Context, _, _, _ string) AgentResult {
		if v := ctx.Value(toolsPolicyKey{}); v != nil {
			gotPolicy = v.(string)
		}
		return AgentResult{OK: true, Result: "done", ExitCode: 0}
	})

	plan, err := r.Route([]string{"code:modify"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	ctx := WithToolsPolicy(context.Background(), config.ToolsPolicyMinimal)
	res := r.Execute(ctx, plan, "fix it", "", false)
	if !res.OK {
		t.Fatalf("execute: %+v", res)
	}
	if gotPolicy != config.ToolsPolicyMinimal {
		t.Fatalf("adapter saw tools policy %q, want the per-task override %q", gotPolicy, config.ToolsPolicyMinimal)
	}
}

// TestExecAgentGlobalPolicyAppliesWithoutOverride: with no per-task policy on
// the context, the router's global policy still reaches the adapter.
func TestExecAgentGlobalPolicyAppliesWithoutOverride(t *testing.T) {
	card := scoreCard()
	card.Agents["cheap"] = ledger.Agent{Adapter: "opencode.py", Capabilities: []string{"code:modify"}, CostTier: "low", Tier: 1}
	r := NewRouter(card, NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{ToolsPolicy: config.ToolsPolicyExtended})
	r.SetAgentProber(func(string, ledger.Agent) bool { return true })
	var gotPolicy string
	r.SetAdapterRunner(func(ctx context.Context, _, _, _ string) AgentResult {
		if v := ctx.Value(toolsPolicyKey{}); v != nil {
			gotPolicy = v.(string)
		}
		return AgentResult{OK: true, Result: "done", ExitCode: 0}
	})

	plan, err := r.Route([]string{"code:modify"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	res := r.Execute(context.Background(), plan, "fix it", "", false)
	if !res.OK {
		t.Fatalf("execute: %+v", res)
	}
	if gotPolicy != config.ToolsPolicyExtended {
		t.Fatalf("adapter saw tools policy %q, want the global %q", gotPolicy, config.ToolsPolicyExtended)
	}
}

// TestExecAgentAllUnavailable: every candidate fails the probe, so execution
// fails closed with an explicit "no usable agent" error the upper layer can
// turn into a manual plan.
func TestExecAgentAllUnavailable(t *testing.T) {
	card := scoreCard()
	card.Agents["cheap"] = ledger.Agent{Adapter: "opencode.py", Capabilities: []string{"code:modify"}, CostTier: "low", Tier: 1}
	card.Agents["expensive"] = ledger.Agent{Adapter: "claude_code.py", Capabilities: []string{"code:modify"}, CostTier: "high", Tier: 1}
	r := NewRouter(card, NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	r.SetAgentProber(func(string, ledger.Agent) bool { return false })
	r.SetAdapterRunner(func(context.Context, string, string, string) AgentResult {
		t.Fatal("adapter must never run when all probes fail")
		return AgentResult{}
	})

	plan, _ := r.Route([]string{"code:modify"})
	res := r.Execute(context.Background(), plan, "fix it", "", false)
	if res.OK {
		t.Fatalf("all-unavailable execution must fail")
	}
	if !strings.Contains(res.Stderr, "no usable agent") {
		t.Fatalf("stderr should explain no usable agent, got %q", res.Stderr)
	}
	if res.Agent != "" {
		t.Fatalf("no agent ran, Result.Agent must stay empty, got %q", res.Agent)
	}
}

// TestAgentBinaryFromInstallCheck: the probe derives the CLI from the card's
// install_check ("which claude") and fails closed on a missing binary.
func TestAgentBinaryFromInstallCheck(t *testing.T) {
	ag := ledger.Agent{InstallCheck: "which definitely-not-a-real-binary-xyz"}
	if defaultAgentProbe("x", ag) {
		t.Fatalf("probe must fail for a missing binary")
	}
	// Unknown adapter + no install_check: the probe cannot decide and lets
	// the adapter try (compatibility with custom adapters).
	if !defaultAgentProbe("x", ledger.Agent{Adapter: "custom.py"}) {
		t.Fatalf("unknown CLI should be treated as available")
	}
}

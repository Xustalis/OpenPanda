// SPDX-License-Identifier: AGPL-3.0-or-later

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

// TestRankAgentsRosterNarrowsPool: a thread's roster (WithAgents) removes the
// agents outside it from ranking entirely — including the cheaper one that
// would otherwise win on cost.
func TestRankAgentsRosterNarrowsPool(t *testing.T) {
	r := NewRouter(scoreCard(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	cands := r.WithAgents([]string{"expensive"}).RankAgents([]string{"code:modify"})
	if len(cands) != 1 || cands[0].Name != "expensive" {
		t.Fatalf("roster candidates = %+v, want only expensive", cands)
	}
	// The original router is untouched: a roster belongs to one task, not to
	// the node.
	if all := r.RankAgents([]string{"code:modify"}); len(all) != 2 {
		t.Fatalf("base router narrowed too: %+v", all)
	}
}

// TestRankAgentsRosterBeatsModelPin: an "agent:<name>" pin the model emitted
// cannot smuggle in an agent the user left out of the team.
func TestRankAgentsRosterBeatsModelPin(t *testing.T) {
	r := NewRouter(scoreCard(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	cands := r.WithAgents([]string{"cheap"}).RankAgents([]string{"agent:expensive", "code:modify"})
	if len(cands) != 1 || cands[0].Name != "cheap" {
		t.Fatalf("roster must outrank the pin: %+v", cands)
	}
}

// TestRankAgentsRosterFloor: a roster member that declares none of the task's
// abilities still runs the work — a one-person team cannot be "no route".
func TestRankAgentsRosterFloor(t *testing.T) {
	r := NewRouter(scoreCard(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	cands := r.WithAgents([]string{"web"}).RankAgents([]string{"code:modify"})
	if len(cands) != 1 || cands[0].Name != "web" {
		t.Fatalf("roster floor candidates = %+v, want web", cands)
	}
	// Outside a roster the same mismatch stays unmatched.
	if cands := r.RankAgents([]string{"code:modify"}); len(cands) != 2 {
		t.Fatalf("unrestricted ranking changed: %+v", cands)
	}
}

// TestWithAgentsAbsentRosterDegrades: a roster naming nobody this node has
// must not strand the task — the router comes back unrestricted.
func TestWithAgentsAbsentRosterDegrades(t *testing.T) {
	r := NewRouter(scoreCard(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	narrowed := r.WithAgents([]string{"remote_only_agent"})
	if cands := narrowed.RankAgents([]string{"code:modify"}); len(cands) != 2 {
		t.Fatalf("absent roster should not narrow: %+v", cands)
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

// TestExecAgentActiveFailoverOnExecutionError: the primary agent's CLI is probed as available,
// but returns an execution failure (e.g. rate-limit or crash). The router should actively
// fail over and run the alternate agent instead of giving up immediately.
func TestExecAgentActiveFailoverOnExecutionError(t *testing.T) {
	card := scoreCard()
	card.Agents["cheap"] = ledger.Agent{Adapter: "opencode.py", Capabilities: []string{"code:modify"}, CostTier: "low", Tier: 1}
	card.Agents["expensive"] = ledger.Agent{Adapter: "claude_code.py", Capabilities: []string{"code:modify"}, CostTier: "high", Tier: 1}
	r := NewRouter(card, NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	// Both CLIs are available
	r.SetAgentProber(func(string, ledger.Agent) bool { return true })
	var ran []string
	r.SetAdapterRunner(func(_ context.Context, adapter, _, _ string) AgentResult {
		ran = append(ran, adapter)
		if adapter == "opencode.py" {
			// First agent fails with error (e.g. 429 quota exhausted)
			return AgentResult{OK: false, Result: "429 rate limit exceeded", ExitCode: 1}
		}
		// Alternate agent succeeds
		return AgentResult{OK: true, Result: "completed via fallback", ExitCode: 0}
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
		t.Fatalf("active failover result = %+v, want ok via expensive", res)
	}
	// opencode.py was attempted (and retried on transient error), then failed over to claude_code.py which succeeded.
	if len(ran) < 2 || ran[0] != "opencode.py" || ran[len(ran)-1] != "claude_code.py" {
		t.Fatalf("adapter runs = %v, want opencode.py attempts followed by claude_code.py", ran)
	}
}

// TestExecAgentTimeoutWithSessionSkipsFailover: a watchdog timeout (exit 124)
// is always returned to the caller, session or not. With a session minted the
// retry loop resumes the checkpoint; without one the timeout still may have
// done half the work, so cold-starting a different CLI would duplicate the side
// effects and report them under another agent's name. The retry/resume path
// above owns that decision; execAgent does not.
func TestExecAgentTimeoutWithSessionSkipsFailover(t *testing.T) {
	card := scoreCard()
	card.Agents["cheap"] = ledger.Agent{Adapter: "opencode.py", Capabilities: []string{"code:modify"}, CostTier: "low", Tier: 1}
	card.Agents["expensive"] = ledger.Agent{Adapter: "claude_code.py", Capabilities: []string{"code:modify"}, CostTier: "high", Tier: 1}
	r := NewRouter(card, NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	r.SetAgentProber(func(string, ledger.Agent) bool { return true })

	var ran []string
	r.SetAdapterRunner(func(_ context.Context, adapter, _, _ string) AgentResult {
		ran = append(ran, adapter)
		return AgentResult{OK: false, Result: "opencode timed out",
			ExitCode: 124, SessionID: "ses_timedout"}
	})
	plan, err := r.Route([]string{"code:modify"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	res := r.Execute(context.Background(), plan, "big task", "", false)
	if res.OK || res.ExitCode != 124 {
		t.Fatalf("timeout result = %+v, want exit 124 failure", res)
	}
	if res.SessionID != "ses_timedout" {
		t.Fatalf("SessionID = %q, want the resumable handle", res.SessionID)
	}
	if len(ran) != 1 || ran[0] != "opencode.py" {
		t.Fatalf("ran = %v, want only opencode.py (no cold-start failover)", ran)
	}

	// Same timeout but no session minted: still no failover. A timeout says
	// nothing about whether the agent's model rejected the run, and the work it
	// had already done is not something a second agent can see.
	ran = nil
	r.SetAdapterRunner(func(_ context.Context, adapter, _, _ string) AgentResult {
		ran = append(ran, adapter)
		if adapter == "opencode.py" {
			return AgentResult{OK: false, Result: "opencode timed out", ExitCode: 124}
		}
		return AgentResult{OK: true, Result: "done via alternate", ExitCode: 0}
	})
	res = r.Execute(context.Background(), plan, "big task", "", false)
	if res.OK || res.Agent != "cheap" {
		t.Fatalf("sessionless timeout result = %+v, want the timeout returned as-is", res)
	}
	if len(ran) != 1 {
		t.Fatalf("ran = %v, want no second agent after a timeout", ran)
	}
}

// TestExecAgentActiveFailoverOnLongErrorOutput ensures an agent that fails with a long
// error or crash log (>= 300 bytes) does not falsely masquerade as OK, and instead
// triggers active failover to the next available agent.
func TestExecAgentActiveFailoverOnLongErrorOutput(t *testing.T) {
	card := ledger.Card{
		Device:        "test-node",
		ResourceClass: "Standard",
		Agents: map[string]ledger.Agent{
			"primary": {Adapter: "opencode.py", Capabilities: []string{"code:modify"}, CostTier: "low", Tier: 1},
			"backup":  {Adapter: "claude_code.py", Capabilities: []string{"code:modify"}, CostTier: "high", Tier: 1},
		},
	}
	r := NewRouter(card, NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	r.SetAgentProber(func(string, ledger.Agent) bool { return true })

	longCrashDump := strings.Repeat("Error: certificate verification error at TLSSocket.onConnectSecure\n", 10)
	var ran []string
	r.SetAdapterRunner(func(_ context.Context, adapter, _, _ string) AgentResult {
		ran = append(ran, adapter)
		if adapter == "opencode.py" {
			return AgentResult{OK: false, Result: longCrashDump, ExitCode: 1}
		}
		return AgentResult{OK: true, Result: "recovered via backup agent", ExitCode: 0}
	})

	plan, err := r.Route([]string{"code:modify"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	res := r.Execute(context.Background(), plan, "fix SSL", "", false)
	if !res.OK || res.Agent != "backup" {
		t.Fatalf("failover on long crash dump result = %+v, want ok via backup", res)
	}
	if len(ran) != 2 || ran[0] != "opencode.py" || ran[1] != "claude_code.py" {
		t.Fatalf("ran = %v, want [opencode.py claude_code.py]", ran)
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

// TestRankAgentsDemotesBlockedAgent pins the dispatch-health term: an agent
// the node's breaker holds open is halved in ranking (so a healthy rival wins
// the next dispatch) but stays ranked — it is a demotion, never an exclusion.
func TestRankAgentsDemotesBlockedAgent(t *testing.T) {
	r := NewRouter(scoreCard(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	if top := r.RankAgents([]string{"code:modify"}); top[0].Name != "cheap" {
		t.Fatalf("top = %q, want cheap while nothing is blocked", top[0].Name)
	}

	r.SetAgentBlocked(func(name string) bool { return name == "cheap" })
	cands := r.RankAgents([]string{"code:modify"})
	if len(cands) != 2 || cands[0].Name != "expensive" {
		t.Fatalf("candidates = %+v, want the blocked cheap agent demoted, not dropped", cands)
	}

	r.SetAgentBlocked(func(string) bool { return false })
	if top := r.RankAgents([]string{"code:modify"}); top[0].Name != "cheap" {
		t.Fatalf("top = %q, want cheap after the circuit closes", top[0].Name)
	}
}

// TestRoutePinnedAgentHasNoAlternates: a task pinned by name ("agent:cheap")
// is a task about that agent. Staffing the fallback chain with the rest of the
// card meant a failed pin was re-run on another CLI and reported green under
// the pinned name — the substitution masked exactly the failure the pin was
// there to observe. The pinned plan carries no alternates; a dispatch failure
// is returned with its reason.
func TestRoutePinnedAgentHasNoAlternates(t *testing.T) {
	card := scoreCard()
	r := NewRouter(card, NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	plan, err := r.Route([]string{"agent:cheap"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Agent != "cheap" {
		t.Fatalf("primary = %q, want cheap", plan.Agent)
	}
	if len(plan.Alternates) != 0 {
		t.Fatalf("alternates = %v, want none for a pinned plan", plan.Alternates)
	}
}

// TestFallbackPromptNamesTheAgentThatActuallyRuns: the prompt is written for
// the plan's agent and keeps naming it, so a supervision followup like
// "report your identity as grok_build" reaches whichever agent stepped in.
// Without a line saying who is actually running, the substitute answers the
// question it was asked — it reports itself as grok_build and claims commands
// grok_build never ran, which contradicts the swap note on the same result.
// The note must ride only the fallback's prompt, and must not rewrite the
// task text underneath it.
func TestFallbackPromptNamesTheAgentThatActuallyRuns(t *testing.T) {
	card := scoreCard()
	card.Agents["cheap"] = ledger.Agent{Adapter: "opencode.py", Capabilities: []string{"code:modify"}, CostTier: "low", Tier: 1}
	card.Agents["expensive"] = ledger.Agent{Adapter: "claude_code.py", Capabilities: []string{"code:modify"}, CostTier: "high", Tier: 1}
	r := NewRouter(card, NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	r.SetAgentProber(func(string, ledger.Agent) bool { return true })

	prompts := make(map[string]string, 2)
	r.SetAdapterRunner(func(_ context.Context, adapter, prompt, _ string) AgentResult {
		prompts[adapter] = prompt
		if adapter == "opencode.py" {
			return AgentResult{OK: false, Result: "Error: Not signed in.", ExitCode: 1}
		}
		return AgentResult{OK: true, Result: "ran the command", ExitCode: 0}
	})
	plan, err := r.Route([]string{"code:modify"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	const task = "run echo hello-from-grok"
	res := r.Execute(context.Background(), plan, task, "", false)
	if !res.OK || res.Agent != "expensive" {
		t.Fatalf("result = %+v, want ok via the fallback agent", res)
	}
	if first := prompts["opencode.py"]; strings.Contains(first, "代理替换") {
		t.Fatalf("the primary's prompt must not carry a substitution note: %q", first)
	}
	got := prompts["claude_code.py"]
	if !strings.Contains(got, "代理替换") || !strings.Contains(got, "不要冒充") {
		t.Fatalf("fallback prompt must name the real runner: %q", got)
	}
	if !strings.Contains(got, "Not signed in") {
		t.Fatalf("fallback prompt must carry the reason it stepped in: %q", got)
	}
	if !strings.HasSuffix(got, task) {
		t.Fatalf("the plan's prompt must survive verbatim after the note: %q", got)
	}
}

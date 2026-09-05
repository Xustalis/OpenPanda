package commander

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/pyexec"
)

func testCard() ledger.Card {
	return ledger.Card{
		Device:        "test-node",
		ResourceClass: "Standard",
		Native: []ledger.NativeAbility{
			{ID: "sys:info", Command: "uname", Args: []string{"-a"}},
		},
		Agents: map[string]ledger.Agent{
			"claude_code": {
				Adapter:      "claude_code.py",
				Capabilities: []string{"code:modify", "code:review"},
				CostTier:     "medium_high",
			},
			"opencode": {
				Adapter:      "opencode.py",
				Capabilities: []string{"web:search", "web:fetch"},
				CostTier:     "low_medium",
			},
		},
		Manual: []ledger.ManualAbility{
			{ID: "design:figma", Notify: "open figma"},
		},
		Capacity: ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
}

func TestRouteNative(t *testing.T) {
	r := NewRouter(testCard(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	plan, err := r.Route([]string{"sys:info"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Kind != "native" || plan.Command != "uname" {
		t.Fatalf("plan = %+v, want native uname", plan)
	}
}

func TestRouteAgent(t *testing.T) {
	r := NewRouter(testCard(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	plan, err := r.Route([]string{"code:modify"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Kind != "agent" || plan.Agent != "claude_code" {
		t.Fatalf("plan = %+v, want agent claude_code", plan)
	}
}

func TestRouteSecondAgent(t *testing.T) {
	r := NewRouter(testCard(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	plan, err := r.Route([]string{"web:search"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Kind != "agent" || plan.Agent != "opencode" || plan.Adapter != "opencode.py" {
		t.Fatalf("plan = %+v, want agent opencode", plan)
	}
}

func TestMatchAgentDeterministic(t *testing.T) {
	card := testCard()
	// Two agents declare the same capability; the sorted-first name must win on
	// every call. Map iteration order is randomized in Go, so a first-match over
	// the map would flip run-to-run.
	card.Agents["aaa_code"] = ledger.Agent{Adapter: "aaa", Capabilities: []string{"code:lint"}}
	card.Agents["zzz_code"] = ledger.Agent{Adapter: "zzz", Capabilities: []string{"code:lint"}}
	r := NewRouter(card, NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})

	for i := 0; i < 50; i++ {
		name, _, ok := r.MatchAgent([]string{"code:lint"})
		if !ok || name != "aaa_code" {
			t.Fatalf("MatchAgent iteration %d = %q, want aaa_code (deterministic)", i, name)
		}
	}
}

func TestRouteManual(t *testing.T) {
	r := NewRouter(testCard(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	plan, err := r.Route([]string{"design:figma"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Kind != "manual" || plan.Notify == "" {
		t.Fatalf("plan = %+v, want manual", plan)
	}
}

func TestRouteNoMatch(t *testing.T) {
	r := NewRouter(testCard(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	if _, err := r.Route([]string{"gpu:train"}); err == nil {
		t.Fatalf("expected error for unmatched ability")
	}
}

func TestRouteAgentByName(t *testing.T) {
	r := NewRouter(testCard(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	plan, err := r.Route([]string{"agent:claude_code"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Kind != "agent" || plan.Agent != "claude_code" {
		t.Fatalf("plan = %+v, want agent claude_code", plan)
	}
}

func TestRouteNormalizedMatch(t *testing.T) {
	// The model may emit "code:lint" for a card id "lint" — normalization
	// bridges the category prefix.
	card := testCard()
	card.Native = append(card.Native, ledger.NativeAbility{ID: "lint", Command: "npx", Args: []string{"eslint"}})
	r := NewRouter(card, NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	plan, err := r.Route([]string{"code:lint"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Kind != "native" || plan.Ability != "lint" {
		t.Fatalf("plan = %+v, want native lint", plan)
	}
}

func TestRouteShortFragmentDoesNotFanOut(t *testing.T) {
	r := NewRouter(testCard(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	if _, err := r.Route([]string{"io"}); err == nil {
		t.Fatalf("expected no match for degenerate 2-char fragment")
	}
}

func TestNativePriorityOverAgent(t *testing.T) {
	card := testCard()
	// Add an agent that also claims sys:info — native must win.
	card.Agents["x"] = ledger.Agent{Adapter: "x.py", Capabilities: []string{"sys:info"}}
	r := NewRouter(card, NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	plan, _ := r.Route([]string{"sys:info"})
	if plan.Kind != "native" {
		t.Fatalf("native priority violated: got %s", plan.Kind)
	}
}

func TestExecuteNative(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uname not available on windows")
	}
	r := NewRouter(testCard(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	plan, _ := r.Route([]string{"sys:info"})
	res := r.Execute(context.Background(), plan, "", "", false)
	if !res.OK {
		t.Fatalf("native exec failed: %s", res.Stderr)
	}
	if res.Stdout == "" {
		t.Fatalf("expected uname output")
	}
}

func TestExecuteTier2RequiresAuth(t *testing.T) {
	card := testCard()
	card.Native = append(card.Native, ledger.NativeAbility{
		ID: "danger:reboot", Command: "sudo", Args: []string{"reboot"}, Tier: 2,
	})
	r := NewRouter(card, NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	plan, err := r.Route([]string{"danger:reboot"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Tier != 2 {
		t.Fatalf("plan tier = %d, want 2", plan.Tier)
	}
	// Refused before execution, so the dangerous command never runs.
	res := r.Execute(context.Background(), plan, "", "", false)
	if res.OK {
		t.Fatalf("tier-2 without auth must be refused")
	}
	if !strings.Contains(res.Stderr, "authorization") {
		t.Fatalf("stderr should mention authorization, got %q", res.Stderr)
	}
}

func TestExecuteAgent(t *testing.T) {
	r := NewRouter(testCard(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	plan, err := r.Route([]string{"code:modify"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Kind != "agent" {
		t.Fatalf("plan = %s, want agent", plan.Kind)
	}
	// Inject a fake adapter so the test exercises the agent execution path
	// (execAgent -> runAdapter) without invoking a real LLM CLI. The fake
	// prober pairs with it so no real CLI needs to be installed.
	r.SetAgentProber(func(string, ledger.Agent) bool { return true })
	r.runAdapter = func(ctx context.Context, adapter, prompt, cwd string) AgentResult {
		if adapter != "claude_code.py" {
			t.Fatalf("adapter = %q, want claude_code.py", adapter)
		}
		return AgentResult{OK: true, Result: "refactored", ExitCode: 0, Tokens: 42, Cost: 0.01}
	}
	// An undeclared agent tier defaults to 1: delegating to an agent is the thing
	// this network exists to do, so it runs without a human in the loop.
	if plan.Tier != defense.TierReversible {
		t.Fatalf("undeclared agent tier = %d, want %d (auto)", plan.Tier, defense.TierReversible)
	}
	res := r.Execute(context.Background(), plan, "refactor this", "", false)
	if !res.OK || res.Stdout != "refactored" {
		t.Fatalf("agent exec without consent = %+v, want ok refactored", res)
	}
	if res.Tokens != 42 {
		t.Fatalf("tokens = %d, want 42", res.Tokens)
	}
}

// TestExecuteAgentDeclaredTier2 verifies the opt-in: a card that explicitly
// declares an agent tier 2 still refuses to run it without consent. That
// declaration is now the only way an agent reaches the approval gate, so it is
// the escape hatch for anyone who wants a human on every delegation.
func TestExecuteAgentDeclaredTier2(t *testing.T) {
	card := testCard()
	ag := card.Agents["claude_code"]
	ag.Tier = defense.TierIrreversible
	card.Agents["claude_code"] = ag
	r := NewRouter(card, NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	plan, err := r.Route([]string{"code:modify"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Tier != defense.TierIrreversible {
		t.Fatalf("declared tier = %d, want %d", plan.Tier, defense.TierIrreversible)
	}
	r.SetAgentProber(func(string, ledger.Agent) bool { return true })
	r.runAdapter = func(ctx context.Context, adapter, prompt, cwd string) AgentResult {
		return AgentResult{OK: true, Result: "refactored", ExitCode: 0}
	}
	if refused := r.Execute(context.Background(), plan, "refactor this", "", false); refused.OK {
		t.Fatal("declared tier-2 agent without consent must be refused")
	}
	if res := r.Execute(context.Background(), plan, "refactor this", "", true); !res.OK {
		t.Fatalf("declared tier-2 agent with consent = %+v, want ok", res)
	}
}

func TestRouteTierFromCommand(t *testing.T) {
	card := testCard()
	// A sudo command without an explicit tier is inferred as Tier 2.
	card.Native = append(card.Native, ledger.NativeAbility{
		ID: "danger:sudo", Command: "sudo", Args: []string{"echo", "hi"},
	})
	r := NewRouter(card, NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	plan, _ := r.Route([]string{"danger:sudo"})
	if plan.Tier != 2 {
		t.Fatalf("inferred tier = %d, want 2", plan.Tier)
	}
}

func TestFileContextHash(t *testing.T) {
	a := &FileContext{Type: "file", Repo: "/r", Scope: []string{"a.go"}}
	b := &FileContext{Type: "file", Repo: "/r", Scope: []string{"a.go"}}
	c := &FileContext{Type: "file", Repo: "/r", Scope: []string{"b.go"}}
	if a.Hash() != b.Hash() {
		t.Fatalf("identical contexts must hash equal")
	}
	if a.Hash() == c.Hash() {
		t.Fatalf("different scopes must hash differently")
	}
}

func TestAgentViabilitySelfContained(t *testing.T) {
	cleanCredentialEnv(t)
	r := NewRouter(testCard(), NewExecutor(), config.ModelConfig{},
		config.InjectionConfig{Model: config.InjectionModelNever}, config.RoutingConfig{})
	// opencode is marked SelfContainedModel: true in registry
	ag := ledger.Agent{
		Adapter:      "opencode.py",
		InstallCheck: "which opencode",
	}
	// Even with InjectionModelNever and no APIKey, a self-contained agent should be viable
	// if python and the binary are available (or mocked).
	// In testCard, let's verify AgentViable logic.
	if r.AgentViable("opencode", ag) != (defaultAgentProbe("opencode", ag) && pyexec.Available()) {
		t.Fatalf("unexpected viability result for opencode")
	}
}

func TestRankAgentsDifferentiated(t *testing.T) {
	card := testCard()
	card.Agents["opencode"] = ledger.Agent{
		Adapter:      "opencode.py",
		Capabilities: []string{"coding", "scripts"},
		CostTier:     "low",
	}
	card.Agents["codex"] = ledger.Agent{
		Adapter:      "codex.py",
		Capabilities: []string{"coding", "code_review"},
		CostTier:     "medium",
	}
	card.Agents["claude_code"] = ledger.Agent{
		Adapter:      "claude_code.py",
		Capabilities: []string{"coding", "refactoring"},
		CostTier:     "medium_high",
	}

	r := NewRouter(card, NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	cands := r.RankAgents([]string{"coding"})
	if len(cands) < 3 {
		t.Fatalf("expected 3 candidates, got %d", len(cands))
	}
	// Low cost tier should score highest (1.0), then medium (0.8), then medium_high (0.7)
	if cands[0].Name != "opencode" {
		t.Fatalf("expected rank 1 to be opencode, got %s (score %f)", cands[0].Name, cands[0].Score)
	}
	if cands[1].Name != "codex" {
		t.Fatalf("expected rank 2 to be codex, got %s (score %f)", cands[1].Name, cands[1].Score)
	}
	if cands[2].Name != "claude_code" {
		t.Fatalf("expected rank 3 to be claude_code, got %s (score %f)", cands[2].Name, cands[2].Score)
	}

	// But if specific capability "refactoring" is requested:
	refactorCands := r.RankAgents([]string{"refactoring"})
	if len(refactorCands) == 0 || refactorCands[0].Name != "claude_code" {
		t.Fatalf("expected claude_code to win for refactoring, got %+v", refactorCands)
	}
}

func TestRealEnvironmentViability(t *testing.T) {
	// A dev-machine probe, not a CI assertion: viability requires both the
	// agent CLI and a reachable model, and a CI runner has neither. The API
	// key comes from the environment — a real key must never live in the
	// repository. Set OPENPANDA_TEST_MODEL_KEY on a machine with the agent
	// CLIs installed to run the probe for real.
	apiKey := os.Getenv("OPENPANDA_TEST_MODEL_KEY")
	if apiKey == "" {
		t.Skip("OPENPANDA_TEST_MODEL_KEY not set — skipping real-environment viability probe")
	}
	userModel := config.ModelConfig{
		BaseURL: "https://api.deepseek.com/anthropic",
		APIKey:  apiKey,
		Model:   "deepseek-v4-flash",
	}
	r := NewRouter(ledger.Card{}, NewExecutor(), userModel,
		config.InjectionConfig{Model: config.InjectionModelAuto}, config.RoutingConfig{})

	probed := 0
	for _, name := range []string{"claude_code", "codex", "grok_build", "hermes", "opencode"} {
		k, ok := agents.ByName(name)
		if !ok {
			t.Fatalf("agent %s not found in registry", name)
		}
		if _, err := exec.LookPath(k.PrimaryBinary()); err != nil {
			t.Logf("Agent: %-12s skipped — CLI not installed on this host", name)
			continue
		}
		probed++
		ag := ledger.Agent{
			Adapter:      k.Adapter,
			InstallCheck: "which " + k.PrimaryBinary(),
		}
		viable := r.AgentViable(name, ag)
		t.Logf("Agent: %-12s Viable: %v", name, viable)
		if !viable {
			t.Errorf("expected %s to be viable on this machine!", name)
		}
	}
	if probed == 0 {
		t.Skip("no agent CLIs installed on this host — nothing to probe")
	}
}

func TestUserCardSchedulingOrder(t *testing.T) {
	card, err := ledger.LoadCard("/Users/xenith/Library/Application Support/openpanda/capabilities.yaml")
	if err != nil {
		t.Skipf("cannot load user capabilities.yaml: %v", err)
	}
	r := NewRouter(card, NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})

	tests := []struct {
		req       []string
		wantFirst string
	}{
		{req: []string{"refactoring"}, wantFirst: "claude_code"},
		{req: []string{"code_review"}, wantFirst: "codex"},
		{req: []string{"build"}, wantFirst: "grok_build"},
		{req: []string{"long_running"}, wantFirst: "hermes"},
		{req: []string{"scripts"}, wantFirst: "opencode"},
		{req: []string{"coding"}, wantFirst: "opencode"}, // low cost wins generic coding
	}

	for _, tt := range tests {
		cands := r.RankAgents(tt.req)
		if len(cands) == 0 {
			t.Fatalf("no candidates for %v", tt.req)
		}
		if cands[0].Name != tt.wantFirst {
			t.Errorf("for req %v: got %s (score %.2f), want %s", tt.req, cands[0].Name, cands[0].Score, tt.wantFirst)
		}
	}
}

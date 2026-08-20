package commander

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/ledger"
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
	// Undeclared agent tier defaults to 2 (fail closed, P1-15), so execution
	// without consent must be refused before the adapter is ever spawned.
	if plan.Tier != defense.TierIrreversible {
		t.Fatalf("undeclared agent tier = %d, want %d (fail closed)", plan.Tier, defense.TierIrreversible)
	}
	refused := r.Execute(context.Background(), plan, "refactor this", "", false)
	if refused.OK {
		t.Fatalf("tier-2 agent without auth must be refused")
	}
	res := r.Execute(context.Background(), plan, "refactor this", "", true)
	if !res.OK || res.Stdout != "refactored" {
		t.Fatalf("agent exec = %+v, want ok refactored", res)
	}
	if res.Tokens != 42 {
		t.Fatalf("tokens = %d, want 42", res.Tokens)
	}
}

// TestExecuteAgentDeclaredTier1 verifies the opt-out: a card that explicitly
// declares an agent tier 1 (read-only) runs without consent (P1-15).
func TestExecuteAgentDeclaredTier1(t *testing.T) {
	card := testCard()
	ag := card.Agents["claude_code"]
	ag.Tier = 1
	card.Agents["claude_code"] = ag
	r := NewRouter(card, NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	plan, err := r.Route([]string{"code:modify"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Tier != 1 {
		t.Fatalf("declared tier = %d, want 1", plan.Tier)
	}
	r.SetAgentProber(func(string, ledger.Agent) bool { return true })
	r.runAdapter = func(ctx context.Context, adapter, prompt, cwd string) AgentResult {
		return AgentResult{OK: true, Result: "refactored", ExitCode: 0}
	}
	res := r.Execute(context.Background(), plan, "refactor this", "", false)
	if !res.OK {
		t.Fatalf("tier-1 agent without auth = %+v, want ok", res)
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

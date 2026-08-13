package commander

import (
	"context"
	"runtime"
	"testing"

	"github.com/xenith/panda/internal/config"
	"github.com/xenith/panda/internal/ledger"
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
	r := NewRouter(testCard(), NewExecutor(), config.ModelConfig{})
	plan, err := r.Route([]string{"sys:info"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Kind != "native" || plan.Command != "uname" {
		t.Fatalf("plan = %+v, want native uname", plan)
	}
}

func TestRouteAgent(t *testing.T) {
	r := NewRouter(testCard(), NewExecutor(), config.ModelConfig{})
	plan, err := r.Route([]string{"code:modify"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Kind != "agent" || plan.Agent != "claude_code" {
		t.Fatalf("plan = %+v, want agent claude_code", plan)
	}
}

func TestRouteSecondAgent(t *testing.T) {
	r := NewRouter(testCard(), NewExecutor(), config.ModelConfig{})
	plan, err := r.Route([]string{"web:search"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Kind != "agent" || plan.Agent != "opencode" || plan.Adapter != "opencode.py" {
		t.Fatalf("plan = %+v, want agent opencode", plan)
	}
}

func TestRouteManual(t *testing.T) {
	r := NewRouter(testCard(), NewExecutor(), config.ModelConfig{})
	plan, err := r.Route([]string{"design:figma"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Kind != "manual" || plan.Notify == "" {
		t.Fatalf("plan = %+v, want manual", plan)
	}
}

func TestRouteNoMatch(t *testing.T) {
	r := NewRouter(testCard(), NewExecutor(), config.ModelConfig{})
	if _, err := r.Route([]string{"gpu:train"}); err == nil {
		t.Fatalf("expected error for unmatched ability")
	}
}

func TestRouteAgentByName(t *testing.T) {
	r := NewRouter(testCard(), NewExecutor(), config.ModelConfig{})
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
	r := NewRouter(card, NewExecutor(), config.ModelConfig{})
	plan, err := r.Route([]string{"code:lint"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Kind != "native" || plan.Ability != "lint" {
		t.Fatalf("plan = %+v, want native lint", plan)
	}
}

func TestRouteShortFragmentDoesNotFanOut(t *testing.T) {
	r := NewRouter(testCard(), NewExecutor(), config.ModelConfig{})
	if _, err := r.Route([]string{"io"}); err == nil {
		t.Fatalf("expected no match for degenerate 2-char fragment")
	}
}

func TestNativePriorityOverAgent(t *testing.T) {
	card := testCard()
	// Add an agent that also claims sys:info — native must win.
	card.Agents["x"] = ledger.Agent{Adapter: "x.py", Capabilities: []string{"sys:info"}}
	r := NewRouter(card, NewExecutor(), config.ModelConfig{})
	plan, _ := r.Route([]string{"sys:info"})
	if plan.Kind != "native" {
		t.Fatalf("native priority violated: got %s", plan.Kind)
	}
}

func TestExecuteNative(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uname not available on windows")
	}
	r := NewRouter(testCard(), NewExecutor(), config.ModelConfig{})
	plan, _ := r.Route([]string{"sys:info"})
	res := r.Execute(context.Background(), plan, "", "")
	if !res.OK {
		t.Fatalf("native exec failed: %s", res.Stderr)
	}
	if res.Stdout == "" {
		t.Fatalf("expected uname output")
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

package commander

import (
	"context"
	"runtime"
	"testing"

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
		},
		Manual: []ledger.ManualAbility{
			{ID: "design:figma", Notify: "open figma"},
		},
		Capacity: ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
}

func TestRouteNative(t *testing.T) {
	r := NewRouter(testCard(), NewExecutor())
	plan, err := r.Route([]string{"sys:info"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Kind != "native" || plan.Command != "uname" {
		t.Fatalf("plan = %+v, want native uname", plan)
	}
}

func TestRouteAgent(t *testing.T) {
	r := NewRouter(testCard(), NewExecutor())
	plan, err := r.Route([]string{"code:modify"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Kind != "agent" || plan.Agent != "claude_code" {
		t.Fatalf("plan = %+v, want agent claude_code", plan)
	}
}

func TestRouteManual(t *testing.T) {
	r := NewRouter(testCard(), NewExecutor())
	plan, err := r.Route([]string{"design:figma"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if plan.Kind != "manual" || plan.Notify == "" {
		t.Fatalf("plan = %+v, want manual", plan)
	}
}

func TestRouteNoMatch(t *testing.T) {
	r := NewRouter(testCard(), NewExecutor())
	if _, err := r.Route([]string{"gpu:train"}); err == nil {
		t.Fatalf("expected error for unmatched ability")
	}
}

func TestNativePriorityOverAgent(t *testing.T) {
	card := testCard()
	// Add an agent that also claims sys:info — native must win.
	card.Agents["x"] = ledger.Agent{Adapter: "x.py", Capabilities: []string{"sys:info"}}
	r := NewRouter(card, NewExecutor())
	plan, _ := r.Route([]string{"sys:info"})
	if plan.Kind != "native" {
		t.Fatalf("native priority violated: got %s", plan.Kind)
	}
}

func TestExecuteNative(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uname not available on windows")
	}
	r := NewRouter(testCard(), NewExecutor())
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

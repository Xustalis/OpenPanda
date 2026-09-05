package core

import (
	"context"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// TestInjectionAnnouncementFlow verifies the explicit injection reminder (A1):
// with injection.model=always the task output starts with the announcement
// and the task event stream carries a model_injection record the Web task
// detail can replay.
func TestInjectionAnnouncementFlow(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "inject-node")
	c.SetWorkDir(t.TempDir())
	c.SetRouterPolicy(config.InjectionConfig{Model: config.InjectionModelAlways}, config.RoutingConfig{})
	c.router.SetAdapterRunner(func(context.Context, string, string, string) commander.AgentResult {
		return commander.AgentResult{OK: true, Result: "work done", ExitCode: 0}
	})

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "injected",
		Project:     "proj",
		ContextType: "command",
		Intent:      "injected",
		Requires:    []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !strings.HasPrefix(result.Stdout, "[panda]") {
		t.Fatalf("output must start with the injection announcement, got %q", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "work done") {
		t.Fatalf("output must keep the agent result after the announcement, got %q", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "injection.model=always") {
		t.Fatalf("announcement must state the reason, got %q", result.Stdout)
	}

	events, err := c.store.Events(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	found := false
	for _, e := range events {
		if e.Type == EvModelInjection {
			found = true
			if !strings.Contains(e.DataJSON, "claude_code") {
				t.Errorf("injection event should name the agent, got %s", e.DataJSON)
			}
		}
	}
	if !found {
		t.Fatalf("task event stream must carry %s, events=%v", EvModelInjection, events)
	}
}

// TestInjectionNeverSilent: with injection.model=never nothing is announced
// and no injection event is recorded.
func TestInjectionNeverSilent(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "inject-never")
	c.SetWorkDir(t.TempDir())
	c.SetRouterPolicy(config.InjectionConfig{Model: config.InjectionModelNever}, config.RoutingConfig{})
	c.router.SetAdapterRunner(func(context.Context, string, string, string) commander.AgentResult {
		return commander.AgentResult{OK: true, Result: "work done", ExitCode: 0}
	})

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "silent",
		Project:     "proj",
		ContextType: "command",
		Intent:      "silent",
		Requires:    []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if result.Stdout != "work done" {
		t.Fatalf("output must be the bare agent result, got %q", result.Stdout)
	}
	events, _ := c.store.Events(ctx, task.TaskID)
	for _, e := range events {
		if e.Type == EvModelInjection {
			t.Fatalf("never mode must not record injection events")
		}
	}
}

// TestInjectionCredentialRescueFlow verifies that when an agent triggers credential
// rescue fallback dynamically during execution, the result carries the injection
// notice with the model name, and EvModelInjection is recorded into the task event stream.
func TestInjectionCredentialRescueFlow(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "inject-rescue")
	c.SetWorkDir(t.TempDir())
	c.SetRouterPolicy(config.InjectionConfig{Model: config.InjectionModelAuto}, config.RoutingConfig{})
	c.router.SetAdapterRunner(func(context.Context, string, string, string) commander.AgentResult {
		return commander.AgentResult{
			OK:       true,
			Result:   "rescued work done",
			ExitCode: 0,
			Injected: true,
			Model:    "deepseek-v4-flash",
		}
	})

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "rescue",
		Project:     "proj",
		ContextType: "command",
		Intent:      "rescue",
		Requires:    []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !strings.HasPrefix(result.Stdout, "[panda]") {
		t.Fatalf("output must start with the injection announcement, got %q", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "rescued work done") {
		t.Fatalf("output must keep the agent result after announcement, got %q", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "deepseek-v4-flash") {
		t.Fatalf("announcement must include the rescued model name, got %q", result.Stdout)
	}

	events, err := c.store.Events(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	found := false
	for _, e := range events {
		if e.Type == EvModelInjection {
			found = true
			if !strings.Contains(e.DataJSON, "deepseek-v4-flash") {
				t.Errorf("injection event should name the model, got %s", e.DataJSON)
			}
		}
	}
	if !found {
		t.Fatalf("task event stream must carry %s on credential rescue", EvModelInjection)
	}
}

// TestFallbackChainEvent verifies the A2 fallback: when the scored primary's
// probe fails, the runner-up executes and the event stream records the
// switch.
func TestFallbackChainEvent(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	card := ledger.Card{
		Device:        "fallback-node",
		ResourceClass: "Standard",
		Agents: map[string]ledger.Agent{
			// Same capability, different cost tiers: cheap scores higher and
			// becomes the primary; its probe fails, so mid_tier takes over.
			"cheap":    {Adapter: "opencode.py", Capabilities: []string{"code:modify"}, CostTier: "low", Tier: 1},
			"mid_tier": {Adapter: "codex.py", Capabilities: []string{"code:modify"}, CostTier: "medium", Tier: 1},
		},
		Capacity: ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
	c := NewCore(db, "fallback-node", card, 5, testLogger(), config.ModelConfig{})
	c.SetSharedSecret(testSharedSecret)
	c.SetWorkDir(t.TempDir())
	c.SetRouterPolicy(config.InjectionConfig{Model: config.InjectionModelNever}, config.RoutingConfig{})
	c.router.SetAgentProber(func(name string, _ ledger.Agent) bool { return name != "cheap" })
	var ran string
	c.router.SetAdapterRunner(func(_ context.Context, adapter, _, _ string) commander.AgentResult {
		ran = adapter
		return commander.AgentResult{OK: true, Result: "fallback done", ExitCode: 0}
	})

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "fallback",
		Project:     "proj",
		ContextType: "command",
		Intent:      "fallback",
		Requires:    []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !result.OK || ran != "codex.py" {
		t.Fatalf("fallback should run codex adapter, ran=%q result=%+v", ran, result)
	}
	events, _ := c.store.Events(ctx, task.TaskID)
	found := false
	for _, e := range events {
		if e.Type == EvAgentFallback && strings.Contains(e.DataJSON, "cheap") && strings.Contains(e.DataJSON, "mid_tier") {
			found = true
		}
	}
	if !found {
		t.Fatalf("event stream must record the fallback, events=%v", events)
	}
}

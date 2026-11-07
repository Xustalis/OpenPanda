// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
)

func hotSwapModel(name string) config.ModelConfig {
	return config.ModelConfig{
		APIType: config.APITypeOpenAI, BaseURL: "https://models.example/v1",
		APIKey: "sk-test", Model: name,
	}
}

func TestSetModelUpdatesExecutionDefaults(t *testing.T) {
	old := hotSwapModel("model-old")
	c := newCoreWithAgentModel(t, "model-switch", old)
	c.SetRouterPolicy(config.InjectionConfig{Model: config.InjectionModelAlways}, config.RoutingConfig{})
	c.AttachSupervisor(old)
	oldRouter, _, oldJudge := c.executionSnapshot()

	next := hotSwapModel("model-new")
	if err := c.SetModel(next); err != nil {
		t.Fatal(err)
	}
	router, model, judge := c.executionSnapshot()
	if model.Model != next.Model || c.ModelConfig().Model != next.Model {
		t.Fatalf("core model = %+v, want %q", model, next.Model)
	}
	dec := router.InjectionDecision("codex", "codex.py")
	if !dec.Inject || dec.Model != next.Model {
		t.Fatalf("new injection = %+v, want %q", dec, next.Model)
	}
	if judge == nil || judge.ModelName() != next.Model {
		t.Fatalf("new judge = %v, want %q", judge, next.Model)
	}
	if oldJudge == nil || oldJudge.ModelName() != old.Model || oldRouter.InjectionDecision("codex", "codex.py").Model != old.Model {
		t.Fatal("model switch mutated an in-flight snapshot")
	}

	// A rejected model must not partially publish a new router or judge.
	bad := hotSwapModel("bad")
	bad.BaseURL = "://invalid"
	if err := c.SetModel(bad); err == nil {
		t.Fatal("invalid endpoint accepted")
	}
	if got, _, currentJudge := c.executionSnapshot(); got != router || currentJudge != judge {
		t.Fatal("failed switch replaced the live snapshot")
	}

	if err := c.SetModel(config.ModelConfig{}); err != nil {
		t.Fatal(err)
	}
	router, model, judge = c.executionSnapshot()
	if model.APIKey != "" || model.Model != "" || judge != nil {
		t.Fatal("clear retained the default model or judge")
	}
	if dec := router.InjectionDecision("codex", "codex.py"); dec.Inject {
		t.Fatal("clear retained default injection credentials")
	}
}

func TestSetModelPreservesInjectionPolicyAndOverrides(t *testing.T) {
	c := newCoreWithAgentModel(t, "model-policy", hotSwapModel("old"))
	pinned := hotSwapModel("agent-pin")
	c.SetRouterPolicy(config.InjectionConfig{
		Model:  config.InjectionModelAlways,
		Agents: map[string]config.ModelConfig{"codex": pinned},
	}, config.RoutingConfig{})
	if err := c.SetModel(hotSwapModel("new")); err != nil {
		t.Fatal(err)
	}
	if dec := c.currentRouter().InjectionDecision("codex", "codex.py"); !dec.Inject || dec.Model != pinned.Model {
		t.Fatalf("model switch lost explicit agent override: %+v", dec)
	}
	c.SetRouterPolicy(config.InjectionConfig{Model: config.InjectionModelNever}, config.RoutingConfig{})
	if err := c.SetModel(hotSwapModel("newer")); err != nil {
		t.Fatal(err)
	}
	if dec := c.currentRouter().InjectionDecision("codex", "codex.py"); dec.Inject {
		t.Fatalf("model switch lost never policy: %+v", dec)
	}
}

func TestModelClearKeepsRunningAttemptJudge(t *testing.T) {
	c := newSuperviseCore(t, "clear-in-flight", 1)
	var calls atomic.Int32
	c.SetSupervisor(newFakeSupervisor(t, func(int) string {
		calls.Add(1)
		return `{"status":"review","reason":"needs human review"}`
	}))
	c.router.SetAdapterRunner(func(context.Context, string, string, string) commander.AgentResult {
		if err := c.SetModel(config.ModelConfig{}); err != nil {
			t.Error(err)
		}
		return commander.AgentResult{OK: true, ExitCode: 0, Result: "work completed"}
	})
	task, result, err := c.SubmitLocal(context.Background(), TaskInput{
		Title: "clear during run", Intent: "inspect this task", ContextType: "command",
		Requires: []string{"code:modify"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || task.State != StateReview || result.State != StateReview {
		t.Fatalf("in-flight judge was dropped: calls=%d task=%s result=%s", calls.Load(), task.State, result.State)
	}
}

func TestSetModelConcurrentExecutionSnapshots(t *testing.T) {
	c := newCoreWithAgentModel(t, "model-race", hotSwapModel("model-0"))
	c.SetRouterPolicy(config.InjectionConfig{Model: config.InjectionModelAlways}, config.RoutingConfig{})
	if err := c.SetModel(hotSwapModel("model-0")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if worker == 0 {
					if err := c.SetModel(hotSwapModel("model-1")); err != nil {
						t.Error(err)
					}
					continue
				}
				router, model, judge := c.executionSnapshot()
				dec := router.InjectionDecision("codex", "codex.py")
				if !dec.Inject || dec.Model != model.Model || judge == nil || judge.ModelName() != model.Model {
					t.Errorf("torn execution snapshot: model=%s decision=%s", model.Model, dec.Model)
				}
				if !strings.HasPrefix(c.ModelConfig().Model, "model-") {
					t.Error("unexpected model config")
				}
			}
		}(worker)
	}
	wg.Wait()
}

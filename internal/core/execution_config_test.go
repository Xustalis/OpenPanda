// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

func TestExecutionPolicySnapshotAndChildModel(t *testing.T) {
	c := newCoreWithAgentModel(t, "agent-policy", config.ModelConfig{})
	policy := c.RuntimeConfig()
	policy.Injection.Model = config.InjectionModelNever
	policy.Routing = config.RoutingConfig{MaxConcurrent: 2,
		AgentModels:    map[string][]string{"claude_code": {"parent"}},
		SubagentModels: map[string][]string{"claude_code": {"child"}},
	}
	if err := c.ApplyRuntimeConfig(policy); err != nil {
		t.Fatal(err)
	}
	old := c.currentRouter()
	policy.Routing.ToolsPolicy = config.ToolsPolicyExtended
	c.SetRouterPolicy(policy.Injection, policy.Routing)
	if old == c.currentRouter() || old.ToolsPolicy() != config.ToolsPolicyMinimal || c.currentRouter().ToolsPolicy() != config.ToolsPolicyExtended {
		t.Fatal("policy update mutated an in-flight router")
	}
	c.currentRouter().SetAgentProber(func(string, ledger.Agent) bool { return true })
	var models []string
	c.currentRouter().SetAdapterProcessRunner(func(ctx context.Context, adapter, prompt, cwd string, env []string) commander.AgentResult {
		// Observe the actual request the generic template and script would receive.
		child := false
		for _, value := range env {
			if value == `OPENPANDA_AGENT_MODELS=["child"]` {
				child = true
			}
		}
		if child {
			models = append(models, "child")
		} else {
			models = append(models, "parent")
		}
		return commander.AgentResult{OK: true, Result: "Completed the requested inspection.", ExitCode: 0}
	})
	ctx := context.Background()
	for _, depth := range []int{0, 1} {
		in := TaskInput{Title: "model inspection", Intent: "inspect the code", Requires: []string{"code:modify"}, Authorized: true}
		task, _, _, err := c.createTask(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.store.SetSpawnDepth(ctx, task.TaskID, depth); err != nil {
			t.Fatal(err)
		}
		if _, err := c.execute(ctx, task.TaskID, in.Intent, in.Requires, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(models) != 2 || models[0] != "parent" || models[1] != "child" {
		t.Fatalf("actual executions got models %v", models)
	}
}

func TestExecutionPolicyConcurrentSnapshots(t *testing.T) {
	c := newCoreWithAgentModel(t, "policy-race", config.ModelConfig{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			for range 50 {
				r, _, _ := c.executionSnapshot()
				_ = r.ToolsPolicy()
				_ = r.InjectionDecision("claude_code", "claude_code.py")
				_ = c.delegateLimits()
			}
		})
	}
	for i := range 50 {
		c.SetRouterPolicy(config.InjectionConfig{Model: config.InjectionModelNever}, config.RoutingConfig{MaxConcurrent: i%4 + 1})
		c.SetDelegationLimits(config.DelegationConfig{FanOut: i%3 + 1})
	}
	wg.Wait()
}

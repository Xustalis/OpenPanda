// SPDX-License-Identifier: AGPL-3.0-or-later

package commander

import (
	"context"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
)

func TestAgentConcurrencyLimitsAndHotIncrease(t *testing.T) {
	g := NewAgentConcurrency()
	routing := config.RoutingConfig{MaxConcurrent: 3, AgentConcurrency: map[string]int{"codex.py": 1, "other-coder": 2}}
	g.Configure(8, routing, config.DelegationConfig{MaxConcurrent: 1})
	root, err := g.acquire(context.Background(), "coder", "codex.py", false)
	if err != nil {
		t.Fatal(err)
	}
	child, err := g.acquire(context.Background(), "claude", "claude_code.py", true)
	if err != nil {
		t.Fatal(err)
	}
	// Adapter-key caps aggregate aliases, and child caps aggregate parents.
	for _, tc := range []struct {
		name, adapter string
		child         bool
	}{
		{"other-coder", "codex.py", false}, {"pi", "pi.py", true},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err := g.acquire(ctx, tc.name, tc.adapter, tc.child)
		cancel()
		if err == nil {
			t.Fatalf("limit bypassed by %s", tc.name)
		}
	}
	child()
	root()
	routing.MaxConcurrent = 1
	g.Configure(8, routing, config.DelegationConfig{})
	first, _ := g.acquire(context.Background(), "a", "a.py", false)
	defer first()
	acquired := make(chan func(), 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { release, _ := g.acquire(ctx, "b", "b.py", false); acquired <- release }()
	routing.MaxConcurrent = 2
	g.Configure(8, routing, config.DelegationConfig{})
	select {
	case release := <-acquired:
		if release == nil {
			t.Fatal("increase did not wake waiting adapter")
		}
		release()
	case <-ctx.Done():
		t.Fatal("increase did not wake waiting adapter")
	}
}

func TestSubagentModelSelectionReachesAdapter(t *testing.T) {
	r := NewRouter(testCard(), NewExecutor(), testModel,
		config.InjectionConfig{Model: config.InjectionModelNever}, config.RoutingConfig{
			AgentModels:    map[string][]string{"codex": {"root-model"}, "pi.py": {"pi-root"}},
			SubagentModels: map[string][]string{"codex": {"child-model"}, "pi.py": {}},
		})
	var seen []string
	r.SetAdapterProcessRunner(func(ctx context.Context, adapter, prompt, cwd string, env []string) AgentResult {
		seen = env
		return AgentResult{OK: true, Result: "ok"}
	})
	for _, tc := range []struct {
		name, adapter string
		child         bool
		want          string
	}{
		{"codex", "codex.py", false, `["root-model"]`},
		{"codex", "codex.py", true, `["child-model"]`},
		{"renamed-pi", "pi.py", false, `["pi-root"]`},
		{"renamed-pi", "pi.py", true, ""},
	} {
		ctx := WithAgentName(context.Background(), tc.name)
		if tc.child {
			ctx = WithSubagent(ctx)
		}
		seen = nil
		if res := r.runAdapterDefault(ctx, tc.adapter, "prompt", ""); !res.OK {
			t.Fatal(res)
		}
		if tc.want == "" {
			if hasEnvPrefix(seen, "OPENPANDA_AGENT_MODELS=") {
				t.Fatal("explicit empty child override failed")
			}
		} else {
			assertEnv(t, seen, map[string]string{"OPENPANDA_AGENT_MODELS": tc.want})
		}
		if hasEnvPrefix(seen, "OPENAI_BASE_URL=") {
			t.Fatal("native choice unexpectedly injected a provider")
		}
	}
}

func TestConcurrencySharedAcrossPolicySnapshots(t *testing.T) {
	r := NewRouter(testCard(), NewExecutor(), testModel, config.InjectionConfig{Model: config.InjectionModelNever}, config.RoutingConfig{MaxConcurrent: 1})
	r.SetAdapterProcessRunner(func(context.Context, string, string, string, []string) AgentResult { return AgentResult{OK: true} })
	release, err := r.concurrency.acquire(context.Background(), "codex", "codex.py", false)
	if err != nil {
		t.Fatal(err)
	}
	next := r.WithConfig(r.Card(), testModel, config.InjectionConfig{Model: config.InjectionModelNever}, config.RoutingConfig{MaxConcurrent: 1})
	if next.concurrency != r.concurrency {
		t.Fatal("policy change reset concurrency accounting")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	res := next.runAdapterDefault(ctx, "codex.py", "prompt", "")
	cancel()
	if res.OK {
		t.Fatal("snapshot bypassed the old active call")
	}
	release()
	if res := next.runAdapterDefault(context.Background(), "codex.py", "prompt", ""); !res.OK {
		t.Fatal(res)
	}
}

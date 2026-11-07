// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAgentExecutionPersistsSingleRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "# keep top\nnode:\n  name: policy\nrouting:\n  tools_policy: extended # keep tools\n  preferred_agents: [codex]\n  max_concurrent: 2 # keep cap\nmodel:\n  provider: deepseek\n  model: entry\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	r := RoutingConfig{MaxConcurrent: 6, AgentModels: map[string][]string{"codex": {"root"}}, SubagentModels: map[string][]string{"codex": {"child"}}, AgentConcurrency: map[string]int{"codex": 2}}
	d := DelegationConfig{MaxConcurrent: 3, MaxChildren: 12, FanOut: 4, MaxDepth: 2}.NormalizedDelegation()
	if err := UpdateAgentExecution(path, r, d); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Routing.AgentModels, r.AgentModels) || !reflect.DeepEqual(got.Routing.SubagentModels, r.SubagentModels) || got.Routing.MaxConcurrent != 6 || got.Routing.AgentConcurrency["codex"] != 2 || !reflect.DeepEqual(got.Delegation, d) {
		t.Fatalf("policy lost: %+v %+v", got.Routing, got.Delegation)
	}
	if got.Routing.ToolsPolicy != ToolsPolicyExtended || got.Model.Model != "entry" || len(got.Routing.PreferredAgents) != 1 {
		t.Fatal("unrelated config was overwritten")
	}
	raw, _ := os.ReadFile(path)
	for _, comment := range []string{"keep top", "keep tools", "keep cap"} {
		if !strings.Contains(string(raw), comment) {
			t.Fatalf("lost comment %s", comment)
		}
	}
}

func TestAgentExecutionValidation(t *testing.T) {
	for _, r := range []RoutingConfig{
		{MaxConcurrent: -1}, {AgentConcurrency: map[string]int{"codex": 0}},
		{SubagentModels: map[string][]string{"": {"m"}}},
		{AgentModels: map[string][]string{"codex": {"bad\nmodel"}}},
	} {
		if err := ValidateAgentExecution(r, DelegationConfig{}); err == nil {
			t.Fatalf("invalid config accepted: %+v", r)
		}
	}
	if err := ValidateAgentExecution(RoutingConfig{}, DelegationConfig{MaxConcurrent: -1}); err == nil {
		t.Fatal("negative child limit accepted")
	}
}

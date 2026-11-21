// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// restrictedTestRouter carries one adapter with a read-only tool face and one
// without, which is the whole distinction plan mode turns on.
func restrictedTestRouter() *commander.Router {
	card := ledger.Card{
		Device: "plan-mode-test",
		Agents: map[string]ledger.Agent{
			"hermes":      {Adapter: "hermes.py", Tier: 1},
			"claude_code": {Adapter: "claude_code.py", Tier: 1},
		},
	}
	return commander.NewRouter(card, commander.NewExecutor(), config.ModelConfig{},
		config.InjectionConfig{}, config.RoutingConfig{})
}

func TestPlanModeAgentPromotesARestrictedAlternate(t *testing.T) {
	router := restrictedTestRouter()
	plan := commander.Plan{
		Kind:       "agent",
		Agent:      "hermes",
		Ability:    "hermes",
		Adapter:    "hermes.py",
		Tier:       1,
		Alternates: []string{"claude_code"},
	}
	got, err := planModeAgent(router, plan)
	if err != nil {
		t.Fatalf("planModeAgent: %v", err)
	}
	if got.Agent != "claude_code" || got.Adapter != "claude_code.py" {
		t.Fatalf("plan = %+v, want claude_code promoted", got)
	}
	// The old primary stays on the chain: it is still a valid agent for a
	// later attempt if the promoted one is unavailable for some other reason.
	if len(got.Alternates) != 1 || got.Alternates[0] != "hermes" {
		t.Fatalf("alternates = %v, want the old primary kept", got.Alternates)
	}
}

func TestPlanModeAgentKeepsACapablePrimary(t *testing.T) {
	router := restrictedTestRouter()
	plan := commander.Plan{Kind: "agent", Agent: "claude_code", Adapter: "claude_code.py", Tier: 1}
	got, err := planModeAgent(router, plan)
	if err != nil {
		t.Fatalf("planModeAgent: %v", err)
	}
	if got.Agent != "claude_code" || len(got.Alternates) != 0 {
		t.Fatalf("plan = %+v, want the primary untouched", got)
	}
}

func TestPlanModeAgentRefusesWhenNothingCanRunReadOnly(t *testing.T) {
	router := restrictedTestRouter()
	plan := commander.Plan{
		Kind:    "agent",
		Agent:   "hermes",
		Adapter: "hermes.py",
		Tier:    1,
	}
	_, err := planModeAgent(router, plan)
	if err == nil {
		t.Fatal("plan mode with no read-only agent must fail, not run full-power")
	}
	// The message has to name the fix, because the commander's own refusal
	// ("authorize the task") cannot: the conversation is what demands
	// read-only, so consent changes nothing.
	for _, want := range []string{"read-only", "another agent", "permission mode"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

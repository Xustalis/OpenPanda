package core

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// TestAgentPromptSelfToolsHint pins the gating of the self-management hint:
// it rides only when the router runs the extended tools policy AND
// routing.panda_tools hasn't been switched off. A nil router (native-only
// card, minimal setups) and the default minimal policy both stay clean.
func TestAgentPromptSelfToolsHint(t *testing.T) {
	const marker = "OpenPanda self-tools"
	extendedRouter := func(routing config.RoutingConfig) *commander.Router {
		return commander.NewRouter(ledger.Card{Agents: map[string]ledger.Agent{"x": {}}},
			commander.NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, routing)
	}

	// No router at all: nothing to hint at.
	c := &Core{}
	prompt, _ := buildAgentPrompt(c, "do it", "", "t", "", i18n.English)
	if strings.Contains(prompt, marker) {
		t.Fatalf("router-less core must not emit the self-tools hint")
	}

	// Minimal policy: extended hint stays out even with a router.
	off := false
	cases := []struct {
		name    string
		routing config.RoutingConfig
		want    bool
	}{
		{"minimal policy", config.RoutingConfig{ToolsPolicy: "minimal"}, false},
		{"extended default", config.RoutingConfig{ToolsPolicy: "extended"}, true},
		{"extended + panda_tools=false", config.RoutingConfig{ToolsPolicy: "extended", PandaTools: &off}, false},
	}
	for _, tc := range cases {
		c := &Core{router: extendedRouter(tc.routing)}
		prompt, _ := buildAgentPrompt(c, "do it", "", "t", "", i18n.English)
		if got := strings.Contains(prompt, marker); got != tc.want {
			t.Fatalf("%s: hint presence = %v, want %v", tc.name, got, tc.want)
		}
	}
}

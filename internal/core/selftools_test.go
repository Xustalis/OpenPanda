// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// TestSelfManagementSection pins the gating of the node's own command
// reference:
//
//   - no router (native-only card, minimal setups) → nothing to say;
//   - routing.panda_tools=false → the operator closed the self surface;
//   - restricted run (remote intent without consent) → read-only tool face,
//     so the write commands must not be advertised;
//   - every other router → the reference rides, including under the minimal
//     tools policy: minimal's whitelist already permits Bash, so knowing
//     `panda` exists grants nothing, and withholding it is what made agents
//     probe the host for the binary before starting the web console.
func TestSelfManagementSection(t *testing.T) {
	const marker = "OpenPanda self-tools"
	routerFor := func(tools string, pandaTools *bool) *commander.Router {
		return commander.NewRouter(ledger.Card{Agents: map[string]ledger.Agent{"x": {}}},
			commander.NewExecutor(), config.ModelConfig{}, config.InjectionConfig{},
			config.RoutingConfig{ToolsPolicy: tools, PandaTools: pandaTools})
	}

	if got := selfManagementSection(&Core{}, false, i18n.English); got != "" {
		t.Fatalf("router-less core must not emit the reference, got %q", got)
	}

	off := false
	cases := []struct {
		name       string
		router     *commander.Router
		restricted bool
		want       bool
	}{
		{"minimal policy", routerFor(config.ToolsPolicyMinimal, nil), false, true},
		{"extended policy", routerFor(config.ToolsPolicyExtended, nil), false, true},
		{"panda_tools=false", routerFor(config.ToolsPolicyExtended, &off), false, false},
		{"restricted run", routerFor(config.ToolsPolicyExtended, nil), true, false},
	}
	for _, tc := range cases {
		c := &Core{router: tc.router}
		got := selfManagementSection(c, tc.restricted, i18n.English)
		if has := strings.Contains(got, marker); has != tc.want {
			t.Fatalf("%s: reference presence = %v, want %v", tc.name, has, tc.want)
		}
		if tc.want && !strings.Contains(got, "panda web [--daemon]") {
			t.Fatalf("%s: reference omits the web console command", tc.name)
		}
	}
}

// TestAgentPromptCarriesNodeCommandReference runs the real execution path —
// task row, queue, dispatch, run, prompt assembly — with the adapter runner
// stubbed to capture what the agent would receive. It is the wiring guard the
// unit test above cannot be: if the selfManagementSection call moves or a
// caller rebuilds the prompt without it, the agent is back to exploring the
// host for a panda it does not know exists.
//
// The usage lines are asserted in their English form on purpose: they are
// CLI syntax, not prose, and every prompt locale carries the same commands.
func TestAgentPromptCarriesNodeCommandReference(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	card := ledger.Card{
		Device:        "selfcmd",
		ResourceClass: "Standard",
		Agents: map[string]ledger.Agent{
			"fake": {Adapter: "fake.py", Capabilities: []string{"code:modify"}, Tier: 1},
		},
		Capacity: ledger.Capacity{CPUCores: 4, RAMGB: 8, MaxConcurrent: 2},
	}
	c := NewCore(db, "selfcmd", card, 5, testLogger(), config.ModelConfig{})
	c.router.SetAgentProber(func(string, ledger.Agent) bool { return true })
	c.SetWorkDir(t.TempDir())
	c.SetSharedSecret(testSharedSecret)
	c.SetSupervisor(newFakeSupervisor(t, func(int) string {
		return `{"status":"done","reason":"ok","followup":""}`
	}))
	var got string
	c.router.SetAdapterRunner(func(_ context.Context, _, prompt, _ string) commander.AgentResult {
		got = prompt
		return commander.AgentResult{OK: true, Result: "done", ExitCode: 0}
	})

	const intent = "启动 OpenPanda Web 控制台，确认它监听的地址"
	tk := createTask(t, c.store, "", "self command", c.nodeID)
	if err := c.store.Queue(ctx, tk.TaskID, c.nodeID); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.Dispatch(ctx, tk.TaskID, c.nodeID, c.nodeID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if _, err := c.run(ctx, tk.TaskID, intent, []string{"agent:fake"}, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got == "" {
		t.Fatal("adapter runner never received a prompt")
	}
	for _, want := range []string{
		intent,
		"panda web [--daemon]",
		"panda daemon [--config F]",
		"panda status",
		"panda help",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("agent prompt omits %q", want)
		}
	}
	if strings.Contains(got, "cli.help.") || strings.Contains(got, "prompt.selfcmd.") {
		t.Errorf("agent prompt leaks an i18n key:\n%s", got)
	}
}

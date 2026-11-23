// SPDX-License-Identifier: AGPL-3.0-or-later
package commander

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// Explicit opt-in: this sends one short request to the user's native provider.
func TestLiveRestrictedClaudeNativeProvider(t *testing.T) {
	if os.Getenv("OPENPANDA_TEST_LIVE_NATIVE_CLAUDE") != "1" {
		t.Skip("requires configured native Claude provider")
	}
	dir, err := filepath.Abs("../../adapters")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(adapterDirEnv, dir)
	r := injectionRouter(config.ModelConfig{}, config.InjectionModelNever)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ctx, pool := WithSessionPool(WithRestricted(ctx))
	defer pool.CloseAll()
	res := r.runAdapterDefault(WithSessionMode(ctx), "claude_code.py", "Reply with just PANDA_NATIVE_OK. Do not use tools.", t.TempDir())
	if !res.OK || res.Injected || !strings.Contains(res.Result, "PANDA_NATIVE_OK") {
		t.Fatalf("native inference failed: ok=%v exit=%d marker=%v login_refusal=%v", res.OK, res.ExitCode, strings.Contains(res.Result, "PANDA_NATIVE_OK"), IsAuthenticationSetupRequired(res.Result))
	}
	t.Logf("native response received: tokens=%d session=%t injected=%t", res.Tokens, res.SessionID != "", res.Injected)
}

// Run against a node whose dsh profile has an unusable native provider and
// whose configured Panda model supports the dsh injection dialect.
func TestLiveDshConfiguredModelRescue(t *testing.T) {
	path := os.Getenv("OPENPANDA_TEST_LIVE_DSH_RESCUE_CONFIG")
	if path == "" {
		t.Skip("requires explicit node configuration")
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal("unable to load node configuration")
	}
	dir, err := filepath.Abs("../../adapters")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(adapterDirEnv, dir)
	r := NewRouter(testCard(), NewExecutor(), cfg.Model, cfg.Injection, cfg.Routing)
	ctx, cancel := context.WithTimeout(WithRestricted(context.Background()), 90*time.Second)
	defer cancel()
	var profiles []bool
	ctx = WithExecutionProfiles(ctx, func(p AdapterExecutionProfile) { profiles = append(profiles, p.ModelInjection) })
	res := r.runAdapterDefault(WithAgentName(ctx, "deepseek_harness"), "deepseek_harness.py", "Reply with just PANDA_RESCUE_OK. Do not use tools.", t.TempDir())
	if !res.OK || !res.Injected || len(profiles) != 2 || profiles[0] || !profiles[1] || !strings.Contains(res.Result, "PANDA_RESCUE_OK") {
		t.Fatalf("dsh native/rescue outcome: launches=%v ok=%v exit=%d credential_refusal=%t", profiles, res.OK, res.ExitCode, strings.Contains(res.RescueError, "MISSING_CREDENTIAL"))
	}
	t.Logf("native then injected response received: launches=%v model=%s", profiles, res.Model)
}

func TestLiveNativeHarnessSwitch(t *testing.T) {
	if os.Getenv("OPENPANDA_TEST_LIVE_NATIVE_SWITCH") != "1" {
		t.Skip("requires a failing native dsh profile and working native Claude")
	}
	dir, err := filepath.Abs("../../adapters")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(adapterDirEnv, dir)
	card := ledger.Card{Agents: map[string]ledger.Agent{"deepseek_harness": {Adapter: "deepseek_harness.py"}, "claude_code": {Adapter: "claude_code.py"}}}
	r := NewRouter(card, NewExecutor(), config.ModelConfig{}, config.InjectionConfig{Model: config.InjectionModelNever}, config.RoutingConfig{})
	ctx, cancel := context.WithTimeout(WithRestricted(context.Background()), 90*time.Second)
	defer cancel()
	var launches []string
	ctx = WithExecutionProfiles(ctx, func(p AdapterExecutionProfile) { launches = append(launches, p.Agent) })
	var switched bool
	ctx = WithProgress(ctx, func(note, kind string) {
		if kind == "agent_switch" && strings.Contains(note, "deepseek_harness") && strings.Contains(note, "claude_code") {
			switched = true
		}
	})
	res := r.Execute(ctx, Plan{Kind: "agent", Agent: "deepseek_harness", Alternates: []string{"claude_code"}, Tier: 1}, "Reply with just PANDA_SWITCH_OK. Do not use tools.", t.TempDir(), false)
	if !res.OK || res.Injected || res.Agent != "claude_code" || res.FallbackFrom != "deepseek_harness" || !switched || strings.Join(launches, ",") != "deepseek_harness,claude_code" || !strings.Contains(res.Stdout, "PANDA_SWITCH_OK") {
		t.Fatalf("launches=%v switch=%t final=%s ok=%t", launches, switched, res.Agent, res.OK)
	}
	t.Logf("launches=%v switch=%t final=%s injected=%t", launches, switched, res.Agent, res.Injected)
}

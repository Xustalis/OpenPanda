// SPDX-License-Identifier: AGPL-3.0-or-later
package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/plan"
	"github.com/Xustalis/OpenPanda/internal/pyexec"
)

// Real adapter subprocesses and queue, with deterministic provider refusals.
// This also checks that a successful rescue unlocks the next stage and an
// exhausted rescue never launches its dependent.
func TestPlanHarnessRecoveryAndDependencySkip(t *testing.T) {
	if !pyexec.Available() {
		t.Skip("adapter transport requires Python")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	t.Setenv("OPENPANDA_ADAPTER_DIR", dir)
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("CODEX_HOME", dir)
	for _, key := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL"} {
		t.Setenv(key, "")
	}
	for name, body := range map[string]string{
		"settings.json": `{"env":{"ANTHROPIC_AUTH_TOKEN":"cc-switch-fixture","ANTHROPIC_MODEL":"native-fixture"}}`,
		"config.toml":   `model = "native-codex"`,
		"claude_code.py": `import json, os, sys
req = json.loads(sys.stdin.readline())
injected = os.getenv('OPENPANDA_INJECTED_MODEL') == '1'
assert req['restricted']
if not injected:
    assert os.getenv('ANTHROPIC_AUTH_TOKEN') == 'cc-switch-fixture'
    assert os.getenv('ANTHROPIC_MODEL') == 'native-fixture'
print(json.dumps(dict(type='event', ev='session', id='startup-only')), file=sys.stderr, flush=True)
ok = injected and os.path.basename(req['cwd']) == 'recover'
print(json.dumps(dict(ok=ok, result='RESCUED' if ok else 'Not logged in · Please run /login', exit_code=0 if ok else 1, session_id='startup-only')), flush=True)
`,
		"codex.py": `import json, sys
json.loads(sys.stdin.readline())
print(json.dumps(dict(ok=False, exit_code=127, result='codex binary not found')), flush=True)
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	card := ledger.Card{Device: "plan-recovery", ResourceClass: "Standard", Agents: map[string]ledger.Agent{
		"claude_code": {Adapter: "claude_code.py", Capabilities: []string{"coding"}, Tier: 1, CostTier: "low"},
		"codex":       {Adapter: "codex.py", Capabilities: []string{"coding"}, Tier: 1, CostTier: "high"},
	}, Capacity: ledger.Capacity{CPUCores: 4, RAMGB: 8, MaxConcurrent: 2}}
	c := NewCore(openTestDB(t), card.Device, card, 5, testLogger(), config.ModelConfig{APIType: config.APITypeAnthropic, APIKey: "panda-fixture", Model: "rescue-fixture", BaseURL: "https://rescue.invalid"})
	c.SetRouterPolicy(config.InjectionConfig{Model: config.InjectionModelAuto}, config.RoutingConfig{PreferredAgents: []string{"claude_code"}})
	c.router.SetAgentProber(func(string, ledger.Agent) bool { return true })
	c.router.SetEndpointProber(func(commander.ProbeSpec) commander.ProbeVerdict { return commander.ProbeVerdict{OK: true} })
	c.SetWorkDir(t.TempDir())
	withArtifactPool(t, c)
	c.SetSupervisor(newFakeSupervisor(t, func(int) string { return `{"status":"done","reason":"fixture verified"}` }))
	if err := c.Register(ctx); err != nil {
		t.Fatal(err)
	}
	p := plan.Plan{Goal: "exercise credential recovery", Stages: []plan.Stage{
		{ID: "recover", Intent: "recover", Requires: []string{"coding"}, SpecJSON: `{"permission_mode":"plan"}`},
		{ID: "exhaust", Intent: "exhaust", Needs: []string{"recover"}, Requires: []string{"agent:claude_code"}, SpecJSON: `{"permission_mode":"plan"}`},
		{ID: "deliver", Intent: "deliver", Needs: []string{"exhaust"}, Requires: []string{"coding"}},
	}}
	id, err := c.StartPlan(ctx, p, DefaultQueueSpec())
	if err != nil {
		t.Fatal(err)
	}
	c.StartQueueScheduler(ctx)
	rows, err := c.store.PlanStages(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	recovered := stageByID(t, rows, "recover")
	exhausted := stageByID(t, rows, "exhaust")
	deliver := stageByID(t, rows, "deliver")
	waitState(t, ctx, c.store, deliver.TaskID, StateCancelled, 20*time.Second)
	for _, tc := range []struct {
		task  Task
		state string
		want  []string
	}{
		{recovered, StateDone, []string{"claude_code:native:failed", "codex:native:failed", "claude_code:injected:ok"}},
		{exhausted, StateFailed, []string{"claude_code:native:failed", "claude_code:injected:failed"}},
		{deliver, StateCancelled, nil},
	} {
		current, err := c.store.Get(ctx, tc.task.TaskID)
		if err != nil || current.State != tc.state {
			t.Fatalf("%s state=%s err=%v", tc.task.StageID, current.State, err)
		}
		events, err := c.store.Events(ctx, tc.task.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		launches := map[string]string{}
		var got []string
		for _, e := range events {
			var data struct {
				ID      string `json:"harness_attempt_id"`
				Agent   string
				OK      bool
				Reason  string
				Profile commander.AdapterExecutionProfile
			}
			if err := json.Unmarshal([]byte(e.DataJSON), &data); err != nil {
				t.Fatal(err)
			}
			switch e.Type {
			case EvExecAgentStart:
				if tc.state == StateCancelled {
					t.Fatal("blocked stage started")
				}
			case "adapter_execution_profile":
				if _, seen := launches[data.ID]; seen || data.ID == "" {
					t.Fatalf("invalid launch id %q", data.ID)
				}
				mode := "native"
				if data.Profile.ModelInjection {
					mode = "injected"
				}
				launches[data.ID] = data.Profile.Agent + ":" + mode
			case "adapter_session_mint", EvAgentEvent:
				if _, found := launches[data.ID]; !found {
					t.Fatalf("%s has no matching harness attempt %q", e.Type, data.ID)
				}
			case "harness_attempt_result":
				name, ok := launches[data.ID]
				if !ok || !strings.HasPrefix(name, data.Agent+":") {
					t.Fatal("outcome lacks corresponding launch", data.ID)
				}
				status := "ok"
				if !data.OK {
					status = "failed"
					if data.Reason == "" {
						t.Fatal("missing failure reason")
					}
				}
				got = append(got, name+":"+status)
			}
		}
		if len(launches) != len(got) || !slices.Equal(got, tc.want) {
			t.Fatalf("%s outcomes=%v want=%v", tc.task.StageID, got, tc.want)
		}
		if tc.state == StateCancelled && !strings.Contains(current.ResultJSON, `"blocked_by":"exhaust"`) {
			t.Fatal(current.ResultJSON)
		}
		t.Logf("%s state=%s attempts=%v", tc.task.StageID, current.State, got)
	}
}

// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/scout"
)

func TestTypedInvestigationUsesReadOnlyEvidenceContract(t *testing.T) {
	for _, kind := range []entry.TaskKind{entry.TaskInvestigation, entry.TaskCodeQuestion} {
		b, _ := json.Marshal(entry.TaskSpecDetail{Kind: kind, PermissionMode: defense.ModeBypass})
		if taskPermissionMode(string(b)) != defense.ModePlan {
			t.Fatalf("%s widened permissions", kind)
		}
		policy := taskSpecEnvelope(string(b))
		for _, want := range []string{string(kind), "actual implementation", "counterexamples", "does not require builds"} {
			if !strings.Contains(policy, want) {
				t.Errorf("contract missing %q: %s", want, policy)
			}
		}
	}
	if taskPermissionMode(`{"task_kind":"edit","permission_mode":"plan"}`) != defense.ModePlan {
		t.Fatal("edit kind widened explicit plan")
	}
	if taskSpecEnvelope(`{}`) != "" {
		t.Fatal("legacy empty contract changed")
	}
	if got := taskSpecEnvelope(`{"task_kind":"operations"}`); !strings.Contains(got, "postcondition") {
		t.Fatalf("operations contract: %s", got)
	}
}

func TestAttemptEnvironmentReachesPromptWithoutCachedGitIdentity(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("private body must not be injected here"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := scout.CaptureEnvironment(context.Background(), dir)
	s.Git = scout.GitSnapshot{Known: true, Commit: "attempt-commit", Branch: "baseline", DirtyKnown: true, Dirty: true}
	p, _ := buildAgentPromptWithEnvironment(&Core{}, "inspect only", "", "investigation", dir, 12000, &s, i18n.English)
	for _, want := range []string{"attempt environment baseline", "attempt-commit", "pre-existing changes", "root rule: AGENTS.md", "[workspace map"} {
		if !strings.Contains(p, want) {
			t.Errorf("missing %q: %s", want, p)
		}
	}
	if strings.Contains(p, "private body must not be injected here") {
		t.Fatal("snapshot loaded rule body")
	}
}

func TestInvestigationPersistsEnvironmentAndDefersAllAcceptance(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithTwoAgents(t)
	dir := t.TempDir()
	c.SetWorkDir(dir)
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("root rules"), 0o600); err != nil {
		t.Fatal(err)
	}
	var prompt string
	c.router.SetAdapterRunner(func(ctx context.Context, adapter, p, cwd string) commander.AgentResult {
		prompt = p
		if !commander.Restricted(ctx) {
			t.Error("typed investigation escaped read-only adapter boundary")
		}
		return commander.AgentResult{OK: true, Result: "Static findings with limited scope; no runtime checks performed.", ExitCode: 0}
	})
	task, _, err := c.SubmitLocal(ctx, TaskInput{Title: "inspect", WorkDir: dir, ContextType: "file", Intent: "inspect implementation", Requires: []string{"agent:claude_code"},
		SpecJSON: `{"task_kind":"investigation","permission_mode":"bypass","acceptance":[{"id":"forbidden","kind":"command","run":"definitely-not-a-real-executable"}]}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "actual implementation") || !strings.Contains(prompt, "attempt environment baseline") || !strings.Contains(prompt, "deferred") {
		t.Fatalf("missing contract/environment: %s", prompt)
	}
	if strings.Contains(prompt, "OpenPanda self-tools") {
		t.Fatal("read-only investigation advertised node mutation commands")
	}
	checks, err := c.store.Checks(ctx, task.TaskID)
	if err != nil || len(checks) != 0 {
		t.Fatalf("read-only task ran acceptance: %v %v", checks, err)
	}
	events, err := c.store.Events(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Type == EvExecAgentStart {
			var start struct {
				Restricted bool `json:"restricted"`
				Authorized bool `json:"authorized"`
			}
			if err := json.Unmarshal([]byte(e.DataJSON), &start); err != nil {
				t.Fatal(err)
			}
			if !start.Restricted || start.Authorized {
				t.Fatalf("misleading read-only start event: %s", e.DataJSON)
			}
		}
		if e.Type != "execution_environment" {
			continue
		}

		var event struct {
			AttemptID   string                    `json:"attempt_id"`
			Kind        entry.TaskKind            `json:"task_kind"`
			Permission  string                    `json:"permission_mode"`
			Environment scout.EnvironmentSnapshot `json:"environment"`
		}
		if err := json.Unmarshal([]byte(e.DataJSON), &event); err != nil {
			t.Fatal(err)
		}
		if event.Kind != entry.TaskInvestigation || event.Permission != defense.ModePlan || !event.Environment.Available || event.Environment.Root != dir || len(event.Environment.RootRules) != 1 || event.AttemptID == "" {
			t.Fatalf("bad persisted environment: %s", e.DataJSON)
		}
		found = true
	}
	if !found {
		t.Fatal("environment not persisted")
	}
}

// The profile must survive the real commander -> Core storage path. A fake
// adapter process is used; neither native CLI nor provider is contacted.
func TestAdapterExecutionProfilePersistsAtCoreBoundary(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	adapters := t.TempDir()
	t.Setenv("OPENPANDA_ADAPTER_DIR", adapters)
	stub := `import json,sys
req=json.loads(sys.stdin.read())
print(json.dumps({"ok":True,"result":"static source findings; runtime checks not run","exit_code":0}))
`
	if err := os.WriteFile(filepath.Join(adapters, "claude_code.py"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	c := newSuperviseCore(t, "profile-boundary", 1)
	c.SetRouterPolicy(config.InjectionConfig{Model: config.InjectionModelNever}, config.RoutingConfig{})
	c.router.SetAgentProber(func(string, ledger.Agent) bool { return true })
	task, result, err := c.SubmitLocal(ctx, TaskInput{Title: "inspect", WorkDir: dir, ContextType: "file", Intent: "inspect only", Requires: []string{"agent:claude_code"}, SpecJSON: `{"task_kind":"investigation"}`})
	if err != nil || !result.OK {
		t.Fatalf("execution: task=%+v result=%+v err=%v", task, result, err)
	}
	events, err := c.store.Events(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Type != "adapter_execution_profile" {
			continue
		}
		var data struct {
			AttemptID string                            `json:"attempt_id"`
			Profile   commander.AdapterExecutionProfile `json:"profile"`
		}
		if err := json.Unmarshal([]byte(e.DataJSON), &data); err != nil {
			t.Fatal(err)
		}
		p := data.Profile
		if data.AttemptID == "" || p.CWD != dir || p.Adapter != "claude_code.py" || !p.Restricted || p.Authorized || p.ModelInjection || p.NativeModel != "unknown" {
			t.Fatalf("bad persisted profile: %s", e.DataJSON)
		}
		found = true
	}
	if !found {
		t.Fatal("real adapter launch profile not persisted")
	}
}

func TestTypedReadOnlyNativeIgnoresPersistedAuthorization(t *testing.T) {
	c := newCoreWithNative(t, "read-only-native", ledger.NativeAbility{ID: "sys:write", Command: "sh", Args: []string{"-c", "printf changed > forbidden.txt"}, Tier: 1})
	root := t.TempDir()
	_, _, _ = c.SubmitLocal(context.Background(), TaskInput{Title: "inspect", WorkDir: root, Intent: "inspect only", Requires: []string{"sys:write"}, Authorized: true, SpecJSON: `{"task_kind":"investigation","permission_mode":"bypass"}`})
	if _, err := os.Stat(filepath.Join(root, "forbidden.txt")); !os.IsNotExist(err) {
		t.Fatalf("read-only native command ran with persisted consent: %v", err)
	}
}

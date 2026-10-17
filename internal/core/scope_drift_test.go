// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

func TestTaskScope(t *testing.T) {
	tests := []struct {
		name     string
		specJSON string
		want     string
	}{
		{"empty", "", ""},
		{"present", `{"scope":"src/components","target":"x"}`, "src/components"},
		{"absent field", `{"target":"x"}`, ""},
		{"malformed", `not-json`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := taskScope(tt.specJSON); got != tt.want {
				t.Errorf("taskScope(%q) = %q, want %q", tt.specJSON, got, tt.want)
			}
		})
	}
}

// newCoreWithAgent builds a Core whose card advertises one agent ability, so a
// task can route to the agent execution path without a real LLM CLI.
func newCoreWithAgent(t *testing.T, id string) *Core {
	return newCoreWithAgentModel(t, id, config.ModelConfig{})
}

// newCoreWithAgentModel is newCoreWithAgent with an explicit model config —
// injection tests need a configured model because an empty one is (correctly)
// never injected.
func newCoreWithAgentModel(t *testing.T, id string, model config.ModelConfig) *Core {
	t.Helper()
	db := openTestDB(t)
	card := ledger.Card{
		Device:        id,
		ResourceClass: "Standard",
		Agents: map[string]ledger.Agent{
			// Tier 1 declared explicitly: these tests exercise scope-drift, not
			// tier authorization (P1-15 defaults undeclared agents to Tier 2).
			"claude_code": {Adapter: "claude_code.py", Capabilities: []string{"code:modify"}, Tier: 1},
		},
		Capacity: ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
	c := NewCore(db, id, card, 5, testLogger(), model)
	// Tests in this suite fake the adapter runner, so pair it with an
	// always-available prober: no real agent CLI needs to be installed.
	c.router.SetAgentProber(func(string, ledger.Agent) bool { return true })
	// A temp work dir keeps per-task dirs out of internal/core/tasks/ for
	// callers that don't pin their own; callers that do override after this.
	c.SetWorkDir(t.TempDir())
	c.SetSharedSecret(testSharedSecret)
	return c
}

// TestScopeDriftPausesAgent verifies that an agent that changes a file outside
// its declared scope is intercepted and paused into review for human analysis,
// not marked done or retried (design §14.2 signal A).
func TestScopeDriftPausesAgent(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "drift-node")
	work := t.TempDir()
	c.SetWorkDir(work)

	c.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		_ = os.WriteFile(filepath.Join(cwd, "out-of-scope.txt"), []byte("x"), 0o644)
		return commander.AgentResult{OK: true, Result: "done", ExitCode: 0}
	})

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "write out of scope",
		Project:     "proj",
		ContextType: "file",
		Intent:      "write a file",
		SpecJSON:    `{"scope":"allowed","target":"x"}`,
		Requires:    []string{"code:modify"},
		Complexity:  0.1,
		Risk:        "low",
	})
	if err != nil {
		t.Fatalf("submit local: %v", err)
	}
	if task.State != StateReview {
		t.Fatalf("state = %s, want review (scope drift pauses for analysis)", task.State)
	}
	if !strings.Contains(result.Stderr, "scope drift") {
		t.Fatalf("stderr = %q, want scope drift message", result.Stderr)
	}
}

// TestScopeDriftParentAnchoredScopeCompletes pins the anchor fix: a scope the
// entry model wrote from the project root ("proj/sub" while workDir is
// ".../proj") names the same tree, so in-scope changes must complete, not
// drift — the guaranteed false intercept that parked finished work in review.
func TestScopeDriftParentAnchoredScopeCompletes(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "drift-node-anchor")
	work := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	c.SetWorkDir(work)

	c.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		_ = os.MkdirAll(filepath.Join(cwd, "sub"), 0o755)
		_ = os.WriteFile(filepath.Join(cwd, "sub", "page.html"), []byte("x"), 0o644)
		return commander.AgentResult{OK: true, Result: "done", ExitCode: 0}
	})

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "parent-anchored scope",
		Project:     "proj",
		ContextType: "file",
		Intent:      "write page",
		SpecJSON:    `{"scope":"proj/sub","target":"x"}`,
		Requires:    []string{"code:modify"},
		Complexity:  0.1,
		Risk:        "low",
	})
	if err != nil {
		t.Fatalf("submit local: %v", err)
	}
	if task.State != StateDone {
		t.Fatalf("state = %s, want done (in-scope writes must not drift); result=%+v", task.State, result)
	}
}

// TestScopeDriftParkedIsApprovable pins the disposition contract: a drift park
// is AcceptWork, so approving a finished-but-flagged task reaches done instead
// of erroring out (the old NeedsChangedInput dead end).
func TestScopeDriftParkedIsApprovable(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "drift-node-approve")
	work := t.TempDir()
	c.SetWorkDir(work)

	c.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		_ = os.WriteFile(filepath.Join(cwd, "out-of-scope.txt"), []byte("x"), 0o644)
		return commander.AgentResult{OK: true, Result: "done", ExitCode: 0}
	})

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "write out of scope",
		Project:     "proj",
		ContextType: "file",
		Intent:      "write a file",
		SpecJSON:    `{"scope":"allowed","target":"x"}`,
		Requires:    []string{"code:modify"},
		Complexity:  0.1,
		Risk:        "low",
	})
	if err != nil {
		t.Fatalf("submit local: %v", err)
	}
	if task.State != StateReview {
		t.Fatalf("state = %s, want review", task.State)
	}
	if result.ApprovalDisposition != string(ApprovalAcceptWork) {
		t.Fatalf("disposition = %q, want %q", result.ApprovalDisposition, ApprovalAcceptWork)
	}

	final, _, err := c.ResumeApproved(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("resume approved: %v", err)
	}
	if final.State != StateDone {
		t.Fatalf("state after approve = %s, want done", final.State)
	}
}

// TestScopeDriftIgnoresHostState verifies that the node's own bookkeeping paths
// (its SQLite dir, the agent CLI's own config) do not count as agent drift: the
// host writes them as a side effect of running a task, so a task that touches
// only host-state files completes even when it declares a narrow scope.
func TestScopeDriftIgnoresHostState(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "drift-node-host")
	work := t.TempDir()
	c.SetWorkDir(work)
	// Host state lives inside the derived run dir — a project task runs in
	// the project work dir, so the bookkeeping paths it ignores sit there.
	runDir := filepath.Join(work, "projects", "proj")
	c.SetHostStatePaths([]string{
		filepath.Join(runDir, "data"),
		filepath.Join(runDir, ".claude"),
	})

	c.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		_ = os.MkdirAll(filepath.Join(cwd, "data"), 0o755)
		_ = os.WriteFile(filepath.Join(cwd, "data", "openpanda.db-wal"), []byte("x"), 0o644)
		_ = os.MkdirAll(filepath.Join(cwd, ".claude"), 0o755)
		_ = os.WriteFile(filepath.Join(cwd, ".claude", "settings.local.json"), []byte("x"), 0o644)
		return commander.AgentResult{OK: true, Result: "done", ExitCode: 0}
	})

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "host state writes",
		Project:     "proj",
		ContextType: "file",
		Intent:      "touch host state",
		SpecJSON:    `{"scope":"allowed","target":"x"}`,
		Requires:    []string{"code:modify"},
		Complexity:  0.1,
		Risk:        "low",
	})
	if err != nil {
		t.Fatalf("submit local: %v", err)
	}
	if task.State != StateDone {
		t.Fatalf("state = %s, want done (host-state writes are not drift)", task.State)
	}
	if !result.OK {
		t.Fatalf("result not ok: %+v", result)
	}
}

// TestAgentWithinScopeCompletes verifies the happy path: an agent that edits
// only within its declared scope completes normally.
func TestAgentWithinScopeCompletes(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "drift-node-ok")
	work := t.TempDir()
	c.SetWorkDir(work)

	c.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		_ = os.MkdirAll(filepath.Join(cwd, "allowed"), 0o755)
		_ = os.WriteFile(filepath.Join(cwd, "allowed", "Navbar.vue"), []byte("x"), 0o644)
		return commander.AgentResult{OK: true, Result: "done", ExitCode: 0}
	})

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "edit in scope",
		Project:     "proj",
		ContextType: "file",
		Intent:      "edit Navbar",
		SpecJSON:    `{"scope":"allowed","target":"x"}`,
		Requires:    []string{"code:modify"},
		Complexity:  0.1,
		Risk:        "low",
	})
	if err != nil {
		t.Fatalf("submit local: %v", err)
	}
	if task.State != StateDone {
		t.Fatalf("state = %s, want done", task.State)
	}
	if !result.OK {
		t.Fatalf("result not ok: %+v", result)
	}
}

// TestFilterHostDriftKeepsAgentChangesUnderDataRoot pins the installed-layout
// fix: the default config puts the database in the work root, so the data
// directory and the snapshot root are the same path. A blind prefix match on
// the host list then hid every change the agent made — files_changed came
// back empty, scope drift could not fire, and correct work retried into
// review. A host path equal to or containing the workDir must not filter;
// precise entries inside it still must.
func TestFilterHostDriftKeepsAgentChangesUnderDataRoot(t *testing.T) {
	work := t.TempDir()
	c := NewCore(openTestDB(t), "drift-node-layout", ledger.Card{}, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())
	c.SetHostStatePaths([]string{
		work, // data dir == work root: the default install layout
		filepath.Join(work, "openpanda.db"),
		filepath.Join(work, "openpanda.db-wal"),
		filepath.Join(work, "openpanda.db-shm"),
		filepath.Join(work, "sessions"),
		filepath.Join(work, "locks"),
		filepath.Join(work, "memory"),
		"", // an unset optional path must not resolve to the process cwd
	})

	drift := []string{
		"openpanda.db-wal",      // live sidecar: host write, filtered
		"sessions/abc.json",     // session snapshot: filtered
		"locks/vm-x.lock",       // flock: filtered
		"memory/daily/today.md", // memory engine write: filtered
		"plans/p1/s1/out.txt",   // stage output: agent work
		"tasks/t1/main.go",      // attached worktree: agent work
		"answer.txt",            // root-level write: agent work
		"projects/p/main.go",    // project worktree file: agent work
		"projects/p/MEMORY.md",  // project memory inside the worktree: task-visible
	}
	got := c.filterHostDrift(work, drift)
	want := map[string]bool{
		"plans/p1/s1/out.txt":  true,
		"tasks/t1/main.go":     true,
		"answer.txt":           true,
		"projects/p/main.go":   true,
		"projects/p/MEMORY.md": true,
	}
	if len(got) != len(want) {
		t.Fatalf("filtered drift = %v, want %v", got, want)
	}
	for _, p := range got {
		if !want[p] {
			t.Fatalf("unexpected survivor %q in %v", p, got)
		}
	}
}

// TestFilterHostDriftProjectWorkDir pins the same bug from the other side:
// the snapshot root is a project work dir, whose ancestor entries (the data
// dir, the projects root) must not mask the agent's files — projects/<name>
// doubles as projectWorkDir on the default layout.
func TestFilterHostDriftProjectWorkDir(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "projects", "p")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	c := NewCore(openTestDB(t), "drift-node-proj", ledger.Card{}, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())
	c.SetHostStatePaths([]string{
		root,
		filepath.Join(root, "openpanda.db-wal"),
		filepath.Join(root, "projects"),
	})
	got := c.filterHostDrift(work, []string{"main.go", "sub/x.go", "MEMORY.md"})
	if len(got) != 3 {
		t.Fatalf("project workdir drift = %v, want all three paths visible", got)
	}
}

// TestFilterHostDriftNestedDataDir keeps the other layout honest: a data dir
// strictly inside the work root still filters everything under it — the
// ancestor guard protects containers, not subtrees.
func TestFilterHostDriftNestedDataDir(t *testing.T) {
	work := t.TempDir()
	data := filepath.Join(work, "data")
	c := NewCore(openTestDB(t), "drift-node-nested", ledger.Card{}, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())
	c.SetHostStatePaths([]string{data})
	got := c.filterHostDrift(work, []string{"data/openpanda.db-wal", "src/app.go"})
	if len(got) != 1 || got[0] != "src/app.go" {
		t.Fatalf("nested data dir drift = %v, want just src/app.go", got)
	}
}

package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xenith/openpanda/internal/commander"
	"github.com/xenith/openpanda/internal/config"
	"github.com/xenith/openpanda/internal/ledger"
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
	c := NewCore(db, id, card, 5, testLogger(), config.ModelConfig{})
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
		_ = os.WriteFile(filepath.Join(work, "out-of-scope.txt"), []byte("x"), 0o644)
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

// TestScopeDriftIgnoresHostState verifies that the node's own bookkeeping paths
// (its SQLite dir, the agent CLI's own config) do not count as agent drift: the
// host writes them as a side effect of running a task, so a task that touches
// only host-state files completes even when it declares a narrow scope.
func TestScopeDriftIgnoresHostState(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "drift-node-host")
	work := t.TempDir()
	c.SetWorkDir(work)
	c.SetHostStatePaths([]string{
		filepath.Join(work, "data"),
		filepath.Join(work, ".claude"),
	})

	c.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		_ = os.MkdirAll(filepath.Join(work, "data"), 0o755)
		_ = os.WriteFile(filepath.Join(work, "data", "openpanda.db-wal"), []byte("x"), 0o644)
		_ = os.MkdirAll(filepath.Join(work, ".claude"), 0o755)
		_ = os.WriteFile(filepath.Join(work, ".claude", "settings.local.json"), []byte("x"), 0o644)
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
		_ = os.MkdirAll(filepath.Join(work, "allowed"), 0o755)
		_ = os.WriteFile(filepath.Join(work, "allowed", "Navbar.vue"), []byte("x"), 0o644)
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

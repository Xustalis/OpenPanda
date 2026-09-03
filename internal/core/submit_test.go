package core

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// TestSubmitLocalRunsNative verifies the local entry loop end-to-end: create a
// task with detail, execute it via a native ability, and observe done + result.
func TestSubmitLocalRunsNative(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithNative(t, "local-native", "", ledger.NativeAbility{
		ID: "sys:echo", Command: "echo", Args: []string{"hello"},
	})

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "echo hello",
		Project:     "proj",
		ContextType: "command",
		Intent:      "echo hello",
		SpecJSON:    `{"scope":"stdout","target":"hello"}`,
		Requires:    []string{"sys:echo"},
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
	if result.Stdout != "hello\n" {
		t.Fatalf("stdout = %q, want %q", result.Stdout, "hello\n")
	}

	// Detail must round-trip through the store.
	got, err := c.store.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ContextType != "command" || got.Intent != "echo hello" || got.Complexity != 0.1 || got.Risk != "low" {
		t.Fatalf("detail mismatch: %+v", got)
	}
	if got.SpecJSON != `{"scope":"stdout","target":"hello"}` {
		t.Fatalf("spec = %q", got.SpecJSON)
	}
}

// TestSubmitLocalNoCapability verifies a local task with no matching ability
// is failed (terminal), not left stuck in the queue.
func TestSubmitLocalNoCapability(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "local-none", "")

	task, _, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "unroutable",
		ContextType: "command",
		Intent:      "unroutable",
		Requires:    []string{"sys:missing"},
	})
	if err == nil {
		t.Fatalf("expected error for unroutable task")
	}
	got, gerr := c.store.Get(ctx, task.TaskID)
	if gerr != nil {
		t.Fatalf("get: %v", gerr)
	}
	if got.State != StateFailed {
		t.Fatalf("state = %s, want failed", got.State)
	}
}

// TestSetDetailRoundTrip verifies SetDetail writes and Get reads all six fields.
func TestSetDetailRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := createTask(t, s, "", "detail", "root")

	if err := s.SetDetail(ctx, tk.TaskID, TaskDetail{
		ContextType: "file", Intent: "refactor", SpecJSON: `{"scope":"a.go"}`,
		Complexity: 0.7, Risk: "high", ResourceJSON: `{"cpu":4}`,
	}); err != nil {
		t.Fatalf("set detail: %v", err)
	}

	got, err := s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ContextType != "file" || got.Intent != "refactor" || got.Complexity != 0.7 ||
		got.Risk != "high" || got.ResourceJSON != `{"cpu":4}` || got.SpecJSON != `{"scope":"a.go"}` {
		t.Fatalf("detail mismatch: %+v", got)
	}
}

// TestSubmitKeepsFileTaskLocalWhenCapable verifies that when a task has ContextType "file"
// and no distributed project (Project == ""), Submit executes it locally if the local node
// matches the required ability, preventing it from being forwarded to a foreign node without files.
func TestSubmitKeepsFileTaskLocalWhenCapable(t *testing.T) {
	ctx := context.Background()
	localEcho := ledger.NativeAbility{ID: "code:edit", Command: "echo", Args: []string{"local done"}}
	c := newCoreWithNative(t, "local-node", "", localEcho)

	// Register a peer in ledger that has much higher capacity, which would otherwise win the score
	peerCard := ledger.Card{
		Device:        "remote-supercomputer",
		ResourceClass: "Full",
		Native: []ledger.NativeAbility{
			{ID: "code:edit", Command: "echo", Args: []string{"remote done"}},
		},
		Capacity: ledger.Capacity{CPUCores: 128, RAMGB: 512, MaxConcurrent: 50},
	}
	if err := ledger.Register(c.db, peerCard, "remote-supercomputer", 1); err != nil {
		t.Fatalf("register peer: %v", err)
	}
	capJSON, _ := json.Marshal(peerCard.Capacity)
	if err := ledger.Heartbeat(c.db, "remote-supercomputer", "online", string(capJSON)); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	task, result, err := c.Submit(ctx, TaskInput{
		Title:       "edit local code",
		ContextType: "file",
		Project:     "",
		Intent:      "edit something locally",
		Requires:    []string{"code:edit"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.State != StateDone {
		t.Fatalf("state = %s, want done", task.State)
	}
	if result.Stdout != "local done\n" {
		t.Fatalf("expected local execution, got stdout: %q", result.Stdout)
	}
}


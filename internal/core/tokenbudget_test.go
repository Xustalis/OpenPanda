// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// docs/design/routing-and-scheduling.md token budget: the persisted quota is the mesh-wide remainder — 0 means
// legacy/unbounded, positive is spendable, -1 is the exhaustion sentinel a
// wire hop must refuse to launder.
func TestSpendTokensSemantics(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := createTask(t, s, "", "budgeted", "node")

	// A fresh row is unbounded: spending leaves it at 0 forever.
	if left, err := s.SpendTokens(ctx, tk.TaskID, 100); err != nil || left != 0 {
		t.Fatalf("unbounded spend = %d, %v", left, err)
	}
	if err := s.SetTokenBudget(ctx, tk.TaskID, 1000); err != nil {
		t.Fatalf("set budget: %v", err)
	}
	if left, err := s.SpendTokens(ctx, tk.TaskID, 300); err != nil || left != 700 {
		t.Fatalf("spend 300 of 1000 = %d, %v", left, err)
	}
	// Crossing zero lands on the -1 sentinel, never back on "unbounded".
	if left, err := s.SpendTokens(ctx, tk.TaskID, 900); err != nil || left != -1 {
		t.Fatalf("overspend = %d, %v, want -1", left, err)
	}
	// Exhaustion is sticky: more spend cannot resurrect a remainder.
	if left, err := s.SpendTokens(ctx, tk.TaskID, 50); err != nil || left != -1 {
		t.Fatalf("post-exhaustion spend = %d, %v, want -1", left, err)
	}
	// n<=0 reads without spending.
	if left, err := s.SpendTokens(ctx, tk.TaskID, 0); err != nil || left != -1 {
		t.Fatalf("read = %d, %v", left, err)
	}
	// The row round-trips through Get.
	task, err := s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if task.TokenBudget != -1 {
		t.Fatalf("task.TokenBudget = %d, want -1", task.TokenBudget)
	}
}

// The wire resolver treats the stored row as authoritative: local spend is
// what the next hop sees, and an exhausted row refuses the forward.
func TestTokenBudgetWire(t *testing.T) {
	c := &Core{store: newTestStore(t)}
	ctx := context.Background()
	tk := createTask(t, c.store, "", "wired", "node")

	// No budget anywhere: unbounded propagates as 0.
	if v, err := c.tokenBudgetWire(ctx, tk.TaskID, 0); err != nil || v != 0 {
		t.Fatalf("unbounded wire = %d, %v", v, err)
	}
	// Stored remainder wins over whatever the payload claimed — the row is
	// what local spend already deducted.
	if err := c.store.SetTokenBudget(ctx, tk.TaskID, 400); err != nil {
		t.Fatal(err)
	}
	if v, err := c.tokenBudgetWire(ctx, tk.TaskID, 9999); err != nil || v != 400 {
		t.Fatalf("stored wire = %d, %v, want 400", v, err)
	}
	// Exhausted: the hop must be refused or the next node spends a quota the
	// mesh already declared gone.
	if err := c.store.SetTokenBudget(ctx, tk.TaskID, -1); err != nil {
		t.Fatal(err)
	}
	if _, err := c.tokenBudgetWire(ctx, tk.TaskID, 500); err == nil {
		t.Fatal("exhausted budget allowed to forward")
	}
}

// TestCreateTaskStampsTokenBudget: a locally submitted top-level task must
// not stay unbounded — docs/design/routing-and-scheduling.md safety floor stamps the configured default at
// creation, and an explicit disable leaves the row untouched. Children are
// skipped because plan.go assigns them the parent's remainder.
func TestCreateTaskStampsTokenBudget(t *testing.T) {
	ctx := context.Background()
	c := &Core{store: newTestStore(t), nodeID: "stamp-node", logger: testLogger()}
	c.SetTimeouts(config.TimeoutsConfig{})

	tk, _, _, err := c.createTask(ctx, TaskInput{Title: "bounded", Intent: "x"})
	if err != nil {
		t.Fatalf("createTask: %v", err)
	}
	row, err := c.store.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.TokenBudget != config.DefaultTaskTokenBudget {
		t.Fatalf("token_budget = %d, want default %d", row.TokenBudget, config.DefaultTaskTokenBudget)
	}

	// Explicitly disabled (task_token_budget: -1): new rows stay unbounded.
	c.SetTimeouts(config.TimeoutsConfig{TaskTokenBudget: -1})
	tk2, _, _, err := c.createTask(ctx, TaskInput{Title: "unbounded", Intent: "x"})
	if err != nil {
		t.Fatalf("createTask disabled: %v", err)
	}
	row2, err := c.store.Get(ctx, tk2.TaskID)
	if err != nil {
		t.Fatalf("get2: %v", err)
	}
	if row2.TokenBudget != 0 {
		t.Fatalf("disabled token_budget = %d, want 0 (unbounded)", row2.TokenBudget)
	}

	// Re-enable: a child task is NOT stamped — it inherits the parent's
	// remainder via plan.go instead.
	c.SetTimeouts(config.TimeoutsConfig{})
	tk3, _, _, err := c.createTask(ctx, TaskInput{Title: "child", Intent: "x", ParentID: tk.TaskID})
	if err != nil {
		t.Fatalf("createTask child: %v", err)
	}
	row3, err := c.store.Get(ctx, tk3.TaskID)
	if err != nil {
		t.Fatalf("get3: %v", err)
	}
	if row3.TokenBudget != 0 {
		t.Fatalf("child token_budget = %d, want 0 (inherits parent)", row3.TokenBudget)
	}
}

// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/plan"
)

// An empty source directory holds nothing to withhold, so a plan over it is an
// ordinary request: it starts, and its stage input is the (empty) snapshot. The
// refusal is reserved for a tree that has content nobody could read.
func TestPlanStartsOnEmptySourceSnapshot(t *testing.T) {
	c := newCoreWithNative(t, "empty-source", ledger.NativeAbility{ID: "dev:noop", Command: "true", Tier: 1})
	withArtifactPool(t, c)
	q := DefaultQueueSpec()
	q.WorkDir = t.TempDir()
	ctx := context.Background()
	planID, err := c.StartPlan(ctx, plan.Plan{
		Goal: "empty", Stages: []plan.Stage{{ID: "one", Intent: "scaffold the project"}},
	}, q)
	if err != nil || planID == "" {
		t.Fatalf("empty snapshot refused: id=%q err=%v", planID, err)
	}
	stages, err := c.store.PlanStages(ctx, planID)
	if err != nil || len(stages) != 1 {
		t.Fatalf("stages=%d err=%v", len(stages), err)
	}
	if len(stages[0].Inputs) == 0 || stages[0].Inputs[0].Stage != worktreeArtifactStage {
		t.Fatalf("empty source input missing: %+v", stages[0].Inputs)
	}
}

// A partial snapshot is what the lenient source pack produces for any tree that
// holds an unreadable path, and a single task over that same tree records
// context_degraded and runs. The plan used to refuse it outright, so one locked
// directory made a project unstartable as a plan while remaining runnable as a
// task. The stage now carries the same degradation note the task path writes.
func TestPlanStartsOnPartialSourceSnapshot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 000 is not a reliable read barrier on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can read a 0000 directory, so the tree would not be partial")
	}
	c := newCoreWithNative(t, "partial-source", ledger.NativeAbility{ID: "dev:noop", Command: "true", Tier: 1})
	withArtifactPool(t, c)
	root := writeRepoTree(t)
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "secret.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	q := DefaultQueueSpec()
	q.WorkDir = root
	planID, err := c.StartPlan(context.Background(), plan.Plan{
		Goal: "partial", Stages: []plan.Stage{
			{ID: "one", Intent: "inspect the source"},
			{ID: "two", Intent: "continue", Needs: []string{"one"}},
		},
	}, q)
	if err != nil {
		t.Fatalf("partial snapshot refused the plan: %v", err)
	}
	stages, err := c.store.PlanStages(context.Background(), planID)
	if err != nil || len(stages) != 2 {
		t.Fatalf("stages=%d err=%v", len(stages), err)
	}
	for _, st := range stages {
		if len(st.Inputs) == 0 || st.Inputs[0].Stage != worktreeArtifactStage {
			t.Fatalf("stage %s lost the source input: %+v", st.StageID, st.Inputs)
		}
		events, err := c.store.Events(context.Background(), st.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		degraded := false
		for _, ev := range events {
			if ev.Type == EvContextDegraded {
				degraded = true
			}
		}
		if !degraded {
			t.Fatalf("stage %s shipped a partial tree with no context_degraded note", st.StageID)
		}
	}
}

// The refusal must still be a refusal when nothing at all could be read: a tree
// whose every entry is unreadable is an empty snapshot with extra steps.
func TestPlanRefusesFullyUnreadableSnapshot(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("chmod 000 is not a read barrier here")
	}
	c := newCoreWithNative(t, "unreadable-source", ledger.NativeAbility{ID: "dev:noop", Command: "true", Tier: 1})
	withArtifactPool(t, c)
	root := t.TempDir()
	for _, name := range []string{"a", "b"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	}
	q := DefaultQueueSpec()
	q.WorkDir = root
	id, err := c.StartPlan(context.Background(), plan.Plan{
		Goal: "unreadable", Stages: []plan.Stage{{ID: "one", Intent: "inspect the source"}},
	}, q)
	if err == nil || id != "" {
		t.Fatalf("a snapshot with no readable entry was accepted: id=%s", id)
	}
}

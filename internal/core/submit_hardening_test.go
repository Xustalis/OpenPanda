// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestSubmitNoCapabilityTerminalizesRow: a routing decline must settle the row
// it just created. The caller already saw the failure; leaving the row in
// submitted let RequeueStaleSubmitted re-queue it later, turning a routing
// failure into a task that runs behind the user's back.
func TestSubmitNoCapabilityTerminalizesRow(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "nocap-node")
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}

	tk, _, err := c.Submit(ctx, TaskInput{
		Title:       "impossible",
		Project:     "proj",
		ContextType: "command",
		Intent:      "read a gpio pin",
		Requires:    []string{"gpio:read"},
	})
	if err == nil {
		t.Fatal("Submit should fail when no node matches the capability")
	}
	row, gerr := c.store.Get(ctx, tk.TaskID)
	if gerr != nil {
		t.Fatalf("get row: %v", gerr)
	}
	if row.State != StateFailed {
		t.Fatalf("row = %s, want failed (the caller already saw the routing error)", row.State)
	}
}

// TestAdvancePlanFailsStageWithoutInputArtifact pins the guard against the
// silent-empty-input release: a done predecessor that produced no artifact (an
// old row, an executor that reported none) must cancel its dependent with the
// real reason instead of releasing it to run against an empty directory.
func TestAdvancePlanFailsStageWithoutInputArtifact(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "advance-inputs")
	mk := func(title string) Task {
		t.Helper()
		tk, _, _, err := c.createTask(ctx, TaskInput{
			Title: title, Project: "proj", ContextType: "command", Intent: title,
		})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		return tk
	}
	build := mk("build")
	train := mk("train")
	const planID = "plan-inputs"
	if err := c.store.SetStage(ctx, build.TaskID, planID, "build", nil); err != nil {
		t.Fatalf("stage build: %v", err)
	}
	if err := c.store.SetStage(ctx, train.TaskID, planID, "train", []string{"build"}); err != nil {
		t.Fatalf("stage train: %v", err)
	}
	// build completes with no OutputArtifact — exactly the divergence the
	// guard exists for (a pack that failed used to skip silently).
	if err := c.store.CompleteFromRemote(ctx, build.TaskID, c.nodeID, map[string]any{"ok": true}); err != nil {
		t.Fatalf("complete build: %v", err)
	}

	err := c.AdvancePlan(ctx, planID)
	if err == nil || !strings.Contains(err.Error(), "without an output artifact") {
		t.Fatalf("AdvancePlan err = %v, want the missing-artifact reason", err)
	}
	got, gerr := c.store.Get(ctx, train.TaskID)
	if gerr != nil {
		t.Fatalf("get train: %v", gerr)
	}
	if got.State != StateFailed {
		t.Fatalf("train = %s, want failed (unresolvable input must not run empty)", got.State)
	}
}

// makeFailedRow creates a self-owned failed task row for backoff tests.
func makeFailedRow(t *testing.T, c *Core, title string) Task {
	t.Helper()
	ctx := context.Background()
	tk := createTask(t, c.store, "", title, c.nodeID)
	if err := c.store.Queue(ctx, tk.TaskID, c.nodeID); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.ClaimLocal(ctx, tk.TaskID, c.nodeID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := c.store.ForceFail(ctx, tk.TaskID, "boom"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	return tk
}

// TestWaitBackoffWakesOnCancel: a cancelled context must cut the retry backoff
// short instead of sleeping out the full delay (up to 32x base) first.
func TestWaitBackoffWakesOnCancel(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "backoff-cancel")
	tk := makeFailedRow(t, c, "backoff cancel")

	runCtx, cancel := context.WithCancel(ctx)
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if c.waitBackoff(runCtx, tk.TaskID, time.Hour) {
		t.Fatal("waitBackoff = true after cancellation, want false")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("waitBackoff took %v, want it cut well short of the delay", elapsed)
	}
}

// TestWaitBackoffWakesOnStateChange: a row that leaves failed mid-wait (a
// human cancel, an approval, a requeue) must end the wait promptly so the
// loop's next read can settle the task.
func TestWaitBackoffWakesOnStateChange(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "backoff-state")
	tk := makeFailedRow(t, c, "backoff state")
	// The retry loop writes the recovery assessment before entering backoff.
	if err := c.store.RecordEvent(ctx, tk.TaskID, EvRecoveryAssessment, map[string]any{"assessment": RecoveryAssessment{Strategy: RecoveryBoundedRetry}}); err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(150 * time.Millisecond)
		if err := c.store.Cancel(ctx, tk.TaskID); err != nil {
			t.Errorf("cancel: %v", err)
		}
	}()
	start := time.Now()
	if c.waitBackoff(ctx, tk.TaskID, 6*time.Second) {
		t.Fatal("waitBackoff = true after the row left failed, want false")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("waitBackoff took %v, want it cut well short of the delay", elapsed)
	}
}

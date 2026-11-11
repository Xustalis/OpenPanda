// SPDX-License-Identifier: AGPL-3.0-or-later
package core

import (
	"context"
	"encoding/json"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/plan"
	"testing"
)

func resumePlanFixture(t *testing.T) (*Core, Task, Task) {
	t.Helper()
	c := newCoreWithNative(t, "resume-graph", ledger.NativeAbility{ID: "sys:write", Command: "sh", Args: []string{"-c", "printf verified > result.txt"}, Tier: 1})
	withArtifactPool(t, c)
	c.SetWorkDir(t.TempDir())
	ctx := context.Background()
	id, err := c.StartPlan(ctx, plan.Plan{Goal: "resume", Stages: []plan.Stage{{ID: "work", Intent: "write", Requires: []string{"sys:write"}}, {ID: "check", Intent: "check", Needs: []string{"work"}, Requires: []string{"sys:write"}}}}, DefaultQueueSpec())
	if err != nil {
		t.Fatal(err)
	}
	rows, err := c.store.PlanStages(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return c, stageByID(t, rows, "work"), stageByID(t, rows, "check")
}

func TestApprovedStageReleasesSuccessorWithoutMonitor(t *testing.T) {
	c, work, next := resumePlanFixture(t)
	ctx := context.Background()
	if err := c.store.Dispatch(ctx, work.TaskID, c.nodeID, c.nodeID); err != nil {
		t.Fatal(err)
	}
	if err := c.store.Fail(ctx, work.TaskID, c.nodeID, "authorization needed"); err != nil {
		t.Fatal(err)
	}
	if err := c.store.ReviewWithDisposition(ctx, work.TaskID, c.nodeID, "resume", ApprovalResumeExecution); err != nil {
		t.Fatal(err)
	}
	done, _, err := c.ResumeApproved(ctx, work.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if done.State != StateDone {
		t.Fatalf("work=%s", done.State)
	}
	child, err := c.store.Get(ctx, next.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if child.State != StateQueued {
		t.Fatalf("successor=%s; needs completion hook", child.State)
	}
}

func TestPlanSweepDoesNotCancelRecoveringDependency(t *testing.T) {
	c, work, next := resumePlanFixture(t)
	ctx := context.Background()
	if err := c.store.Dispatch(ctx, work.TaskID, c.nodeID, c.nodeID); err != nil {
		t.Fatal(err)
	}
	if err := c.store.RecordEvent(ctx, work.TaskID, EvExecAgentStart, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := c.store.Fail(ctx, work.TaskID, c.nodeID, "timeout"); err != nil {
		t.Fatal(err)
	}
	if err := c.store.RecordEvent(ctx, work.TaskID, EvRecoveryAssessment, map[string]any{"assessment": RecoveryAssessment{Strategy: RecoverySessionResume}}); err != nil {
		t.Fatal(err)
	}
	if err := c.AdvancePlan(ctx, work.PlanID); err != nil {
		t.Fatal(err)
	}
	child, err := c.store.Get(ctx, next.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if child.State != StateSubmitted {
		t.Fatalf("recovering dependency cancelled successor: %s", child.State)
	}
	if err := c.store.RecordEvent(ctx, work.TaskID, EvRecoveryAssessment, map[string]any{"assessment": RecoveryAssessment{Strategy: RecoveryStop}}); err != nil {
		t.Fatal(err)
	}
	if err := c.AdvancePlan(ctx, work.PlanID); err != nil {
		t.Fatal(err)
	}
	child, _ = c.store.Get(ctx, next.TaskID)
	if child.State != StateCancelled {
		t.Fatalf("terminal failure did not propagate: %s", child.State)
	}
}

func TestReviewStageEventDoesNotClaimCompletion(t *testing.T) {
	c, work, _ := resumePlanFixture(t)
	work.State = StateReview
	c.advanceStagePlan(context.Background(), work)
	events, err := c.store.Events(context.Background(), work.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range events {
		if ev.Type == EvPlanStageChanged {
			var data map[string]any
			if err := json.Unmarshal([]byte(ev.DataJSON), &data); err != nil {
				t.Fatal(err)
			}
			if data["transition"] == "completed" {
				t.Fatal("review was labeled completed")
			}
			if data["transition"] == "review" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("review event missing")
	}
}

// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/plan"
)

func activeReviewFixture(t *testing.T) (*Core, Task, Task, Task) {
	t.Helper()
	ctx := context.Background()
	c := newCore(t, "plan-actions")
	graph, err := plan.WithSynthesis(plan.Plan{Goal: "confirm the new schema", Stages: []plan.Stage{{ID: "source", Intent: "inspect"}, {ID: "target", Intent: "implement"}}}, "", "Deliver", "")
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.StartPlan(ctx, graph, DefaultQueueSpec())
	if err != nil {
		t.Fatal(err)
	}
	rows, err := c.store.PlanStages(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	source, target, final := stageByID(t, rows, "source"), stageByID(t, rows, "target"), stageByID(t, rows, "panda-final")
	for _, task := range []Task{source, target} {
		if err := c.store.Dispatch(ctx, task.TaskID, c.nodeID, c.nodeID); err != nil {
			t.Fatal(err)
		}
		if err := c.store.Accept(ctx, task.TaskID, c.nodeID, task.AttemptID); err != nil {
			t.Fatal(err)
		}
	}
	return c, source, target, final
}

func TestLivePlanClosesOnlyRedundantBranch(t *testing.T) {
	ctx := context.Background()
	c, source, target, final := activeReviewFixture(t)
	close := entry.PlanAction{Kind: "close", TargetStage: "target", Reason: "source independently covered this check"}
	for range 2 {
		if err := c.applyPlanAction(ctx, source, 1, 0, close); err != nil {
			t.Fatal(err)
		}
	}
	st, err := c.store.Get(ctx, target.TaskID)
	if err != nil || st.State != StateCancelled || !strings.Contains(st.ResultJSON, "plan_withdrawn") {
		t.Fatalf("withdrawn=%+v err=%v", st, err)
	}
	st, err = c.store.Get(ctx, final.TaskID)
	if err != nil || !slices.Equal(st.Needs, []string{"source"}) || st.State != StateSubmitted {
		t.Fatalf("final=%+v err=%v", st, err)
	}
	if err := c.store.releasePlanStage(ctx, final, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale final released: %v", err)
	}
	if err := c.store.applyTransition(ctx, final.TaskID, StateSubmitted, StateFailed, final.OwnerNode, final.AttemptID, EvResult, nil, nil, true, final.StateVersion); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale dependency failed final: %v", err)
	}
	audit, err := c.store.PlanAudit(ctx, source.PlanID)
	if err != nil || len(audit.Decisions) != 1 || audit.Decisions[0].Outcome != "closed" {
		t.Fatalf("audit=%+v err=%v", audit, err)
	}
}

func TestLatePlanCorrectionRequeuesAndCannotCrossPlan(t *testing.T) {
	ctx := context.Background()
	c, source, target, final := activeReviewFixture(t)
	act := entry.PlanAction{Kind: "adjust", TargetStage: final.StageID, Reason: "changed", Instruction: "check the updated schema"}
	if err := c.applyPlanAction(ctx, source, 1, 0, act); err == nil {
		t.Fatal("final stage was redirected")
	}
	act.TargetStage = target.StageID
	for range 2 {
		if err := c.applyPlanAction(ctx, source, 1, 0, act); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.applyPlanAction(ctx, source, 1, 1, entry.PlanAction{Kind: "open", Instruction: "bad", Reason: "test", Needs: []string{"another-plan"}}); err == nil {
		t.Fatal("unknown dependency admitted")
	}
	requeued, err := c.store.completeReviewedStage(ctx, target, c.nodeID, target.AttemptID, map[string]bool{"ok": true})
	if err != nil || !requeued {
		t.Fatalf("stage requeued=%t err=%v", requeued, err)
	}
	next, err := c.store.Get(ctx, target.TaskID)
	if err != nil || next.State != StateQueued || next.AttemptID == target.AttemptID {
		t.Fatalf("stage=%+v err=%v", next, err)
	}
	if got, label := c.takeRoundSteer(ctx, target.TaskID, msgSeen{}); got != act.Instruction || label != "[Panda plan adjustment]" {
		t.Fatalf("pending steer %q %q", got, label)
	}
}

func TestPlanFollowupInheritsSealedRoundAndWaitingAmendment(t *testing.T) {
	ctx := context.Background()
	c, source, target, final := activeReviewFixture(t)
	if err := c.store.BeginAskRound(ctx, "round", "session", "en"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.db.Exec(`INSERT INTO ask_round_tasks(task_id,round_id) VALUES(?,'round')`, source.TaskID); err != nil {
		t.Fatal(err)
	}
	if err := c.store.CloseAskRound(ctx, "round", "closed"); err != nil {
		t.Fatal(err)
	}
	open := entry.PlanAction{Kind: "open", Instruction: "verify schema", Reason: "schema changed", Needs: []string{target.StageID}}
	for range 2 {
		if err := c.applyPlanAction(ctx, source, 1, 0, open); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := c.store.PlanStages(ctx, source.PlanID)
	if err != nil || len(rows) != 4 {
		t.Fatalf("stages=%d err=%v", len(rows), err)
	}
	var follow Task
	for _, t := range rows {
		if strings.HasPrefix(t.StageID, "followup-") {
			follow = t
		}
	}
	if !slices.Equal(follow.Needs, []string{source.StageID, target.StageID}) || follow.AgentSessionID != "" || follow.SpecJSON != source.SpecJSON {
		t.Fatalf("follow-up has incorrect inputs or execution contract: %+v", follow)
	}
	var member string
	if err := c.db.QueryRow(`SELECT round_id FROM ask_round_tasks WHERE task_id=?`, follow.TaskID).Scan(&member); err != nil || member != "round" {
		t.Fatalf("round=%q err=%v", member, err)
	}
	amend := entry.PlanAction{Kind: "adjust", TargetStage: follow.StageID, Reason: "inspect found another type", Instruction: "verify numeric type"}
	if err := c.applyPlanAction(ctx, source, 1, 1, amend); err != nil {
		t.Fatal(err)
	}
	next, err := c.store.Get(ctx, follow.TaskID)
	if err != nil || !strings.Contains(next.Intent, amend.Instruction) || next.State != StateSubmitted {
		t.Fatalf("waiting stage assignment=%+v err=%v", next, err)
	}
	if err := c.applyPlanAction(ctx, source, 1, 2, entry.PlanAction{Kind: "close", TargetStage: target.StageID, Reason: "redundant"}); err == nil {
		t.Fatal("closed an input of pending verification")
	}
	last, err := c.store.Get(ctx, final.TaskID)
	if err != nil || !slices.Contains(last.Needs, follow.StageID) {
		t.Fatalf("final no longer waits for follow-up: %+v err=%v", last, err)
	}
}

func TestPlanAdjustmentOfRemoteBranchOpensDependentSession(t *testing.T) {
	ctx := context.Background()
	c, source, target, _ := activeReviewFixture(t)
	if _, err := c.db.Exec(`UPDATE tasks SET owner_node=? WHERE task_id=?`, "peer", target.TaskID); err != nil {
		t.Fatal(err)
	}
	action := entry.PlanAction{Kind: "adjust", TargetStage: target.StageID, Reason: "response changed", Instruction: "verify revised response"}
	if err := c.applyPlanAction(ctx, source, 1, 0, action); err != nil {
		t.Fatal(err)
	}
	audit, err := c.store.PlanAudit(ctx, source.PlanID)
	if err != nil || len(audit.Decisions) != 1 || audit.Decisions[0].Outcome != "opened" {
		t.Fatalf("audit=%+v err=%v", audit, err)
	}
	stages, err := c.store.PlanStages(ctx, source.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	next := stageByID(t, stages, audit.Decisions[0].Target)
	if !slices.Equal(next.Needs, []string{source.StageID, target.StageID}) || next.Intent != action.Instruction {
		t.Fatalf("dependent session=%+v", next)
	}
	if err := c.applyPlanAction(ctx, source, 1, 1, entry.PlanAction{Kind: "close", TargetStage: target.StageID, Reason: "redundant"}); err == nil {
		t.Fatal("sent a plan-wide cancellation to a remote executor")
	}
}

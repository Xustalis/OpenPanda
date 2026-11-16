// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/objectives"
	"strings"
	"testing"
)

func TestObjectiveVerdictNeverCompletesMissingMAXEvidence(t *testing.T) {
	policy := &objectives.Policy{Version: 1, Revision: 1, Dimensions: []objectives.Dimension{{Dimension: "quality", Weight: 1, Priority: objectives.Max, Requirement: "Verify delivery", Reason: "Owner request", Criteria: []string{"Regression passes"}}}}
	raw, _ := json.Marshal(entry.TaskSpecDetail{Objectives: policy, PlanFinal: true})
	task := Task{TaskID: "root", PlanID: "plan", SpecJSON: string(raw)}
	if v := objectiveVerdict(task, entry.SuperviseVerdict{Status: entry.VerdictDone}); v.Status != entry.VerdictReview {
		t.Fatalf("missing evidence accepted: %+v", v)
	}
	v := entry.SuperviseVerdict{Status: entry.VerdictDone, ObjectiveAssessments: []objectives.Assessment{{Dimension: "quality", Criterion: 0, Status: "passed", Evidence: "Recorded regression output"}}}
	if got := objectiveVerdict(task, v); got.Status != entry.VerdictDone {
		t.Fatal(got)
	}
	v.ObjectiveAssessments[0].Status = "deferred"
	if got := objectiveVerdict(task, v); got.Status != entry.VerdictReview {
		t.Fatal(got)
	}
	task.ParentID = "parent"
	if got := objectiveVerdict(task, v); got.Status != entry.VerdictDone {
		t.Fatal(got)
	}
	if !strings.Contains(taskSpecEnvelope(task.SpecJSON), "MAX") || !strings.Contains(objectiveTaskContext(task), "intermediate") {
		t.Fatal("worker context lost objective")
	}
	inherited, err := inheritTaskObjectives(task.SpecJSON, `{"permission_mode":"plan"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inherited, "permission_mode") || !strings.Contains(inherited, "MAX") {
		t.Fatal(inherited)
	}
}

func objectiveFixturePolicy() *objectives.Policy {
	return &objectives.Policy{Version: 1, Revision: 2, Dimensions: []objectives.Dimension{{Dimension: "quality", Weight: 1, Priority: objectives.Max, Requirement: "Verify delivery", Reason: "User requirement", Criteria: []string{"Regression passes"}}}}
}

func objectiveFixtureTask(t *testing.T, c *Core, task Task) Task {
	t.Helper()
	raw, err := json.Marshal(entry.TaskSpecDetail{Objectives: objectiveFixturePolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.store.SetDetail(context.Background(), task.TaskID, TaskDetail{SpecJSON: string(raw)}); err != nil {
		t.Fatal(err)
	}
	got, err := c.store.Get(context.Background(), task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestObjectiveFinalizePersistsIndependentEvidence(t *testing.T) {
	for _, status := range []string{entry.VerdictDone, entry.VerdictContinue} {
		t.Run(status, func(t *testing.T) {
			c := newCore(t, "objective-finalize")
			task := objectiveFixtureTask(t, c, runToRunning(t, c, "objective-finalize-"+status, "verify"))
			assessments := []objectives.Assessment{{Dimension: "quality", Criterion: 0, Status: "passed", Evidence: "Recorded independent test exit 0"}}
			payload, err := c.finalizeRun(context.Background(), task.TaskID, task, commander.Plan{Kind: "agent", Tier: 1}, nil, task.AttemptID, t.TempDir(), commander.Result{OK: true, ExitCode: 0, Stdout: "output"}, nil, nil, false, entry.SuperviseVerdict{Status: status, Reason: "checked", Followup: "remaining work", ObjectiveAssessments: assessments}, false, "", "", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(payload.ObjectiveAssessments) != 1 {
				t.Fatalf("wire lost evidence: %+v", payload)
			}
			got, err := c.store.Get(context.Background(), task.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			var stored bus.TaskResultPayload
			if err := json.Unmarshal([]byte(got.ResultJSON), &stored); err != nil {
				t.Fatal(err)
			}
			if len(stored.ObjectiveAssessments) != 1 {
				t.Fatalf("stored result lost evidence: %s", got.ResultJSON)
			}
			if status == entry.VerdictDone && got.State != StateDone {
				t.Fatal(got.State)
			}
			if status == entry.VerdictContinue && got.State != StateReview {
				t.Fatal(got.State)
			}
		})
	}
}

func TestObjectiveEvidenceCannotBeBypassedByApproval(t *testing.T) {
	c := newCore(t, "objective-approve")
	ctx := context.Background()
	task := objectiveFixtureTask(t, c, runToRunning(t, c, "objective-approve", "verify"))
	if err := c.store.CompleteAttempt(ctx, task.TaskID, c.nodeID, task.AttemptID, bus.TaskResultPayload{OK: true}); err == nil {
		t.Fatal("completion accepted missing evidence")
	}
	if err := c.store.PauseWithResultAttempt(ctx, task.TaskID, c.nodeID, task.AttemptID, bus.TaskResultPayload{OK: true}); err != nil {
		t.Fatal(err)
	}
	if err := c.store.Approve(ctx, task.TaskID); err == nil {
		t.Fatal("approval accepted missing evidence")
	}
	got, err := c.store.Get(ctx, task.TaskID)
	if err != nil || got.State != StateReview {
		t.Fatalf("state=%s err=%v", got.State, err)
	}
}

func TestObjectiveRemoteResultRequiresAndPreservesEvidence(t *testing.T) {
	for _, hasEvidence := range []bool{false, true} {
		t.Run(fmt.Sprint(hasEvidence), func(t *testing.T) {
			c := newCore(t, "objective-origin")
			task := objectiveFixtureTask(t, c, setupDispatchedTask(t, c, "objective-remote", "objective-worker"))
			payload := bus.TaskResultPayload{TaskID: task.TaskID, AttemptID: task.AttemptID, State: StateDone, OK: true}
			if hasEvidence {
				payload.ObjectiveAssessments = []objectives.Assessment{{Dimension: "quality", Criterion: 0, Status: "passed", Evidence: "Worker independent test evidence"}}
			}
			env, err := bus.NewEnvelope(bus.MsgTaskResult, "objective-worker", "objective-result", payload)
			if err != nil {
				t.Fatal(err)
			}
			c.handleResult(context.Background(), env)
			got, err := c.store.Get(context.Background(), task.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			want := StateReview
			if hasEvidence {
				want = StateDone
			}
			if got.State != want {
				t.Fatalf("state=%s want=%s", got.State, want)
			}
			var stored bus.TaskResultPayload
			if err := json.Unmarshal([]byte(got.ResultJSON), &stored); err != nil {
				t.Fatal(err)
			}
			if hasEvidence && len(stored.ObjectiveAssessments) != 1 {
				t.Fatalf("lost evidence: %s", got.ResultJSON)
			}
		})
	}
}

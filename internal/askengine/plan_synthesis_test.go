// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"encoding/json"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/contract"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
)

func TestClassifiedPlanAddsSynthesisAndKeepsStageContract(t *testing.T) {
	e, store, ctx := newStageTestEngine(t)
	e.queueTasks = true
	spec := &entry.PlanSpec{Goal: "integrate two results", Stages: []entry.PlanStageSpec{
		{ID: "a", Intent: "collect a", ToolsPolicy: "extended", MaxTurns: 42, Acceptance: []contract.Check{{ID: "file", Exists: []string{"a.txt"}}}},
		{ID: "b", Intent: "collect b"},
	}}
	res, err := e.startClassifiedPlan(ctx, spec, AskScope{}, false, StreamCallbacks{})
	if err != nil || !res.OK {
		t.Fatalf("plan: %+v %v", res, err)
	}
	rows, err := store.PlanStages(ctx, res.PlanID)
	if err != nil || len(rows) != 3 {
		t.Fatalf("missing synthesis: %+v %v", rows, err)
	}
	for _, row := range rows {
		var detail entry.TaskSpecDetail
		if err := json.Unmarshal([]byte(row.SpecJSON), &detail); err != nil {
			t.Fatal(err)
		}
		if !detail.StageReports {
			t.Fatal("stage output report not enabled")
		}
		if row.StageID == "a" && (len(detail.Acceptance) != 1 || detail.MaxTurns != 42 || detail.ToolsPolicy != "extended") {
			t.Fatalf("lost original contract: %+v", detail)
		}
		if detail.PlanFinal && (len(row.Needs) != 2 || row.State != core.StateSubmitted) {
			t.Fatalf("final released before branches: %+v", row)
		}
	}
}

func TestPlanVerdictPublishesOnlySettledFinalAnswer(t *testing.T) {
	e, store, ctx := newStageTestEngine(t)
	rows := []core.Task{{StageID: "a", State: core.StateDone}, {StageID: "final", State: core.StateDone, SpecJSON: `{"plan_final":true}`, ResultJSON: `{"stdout":"integrated evidence and answer"}`}}
	res := &Result{OK: true}
	e.planBoardVerdict(ctx, store, res, rows)
	if res.Answer != "integrated evidence and answer" {
		t.Fatalf("missing answer: %+v", res)
	}
	rows[0].State = core.StateRunning
	res = &Result{OK: true}
	e.planBoardVerdict(ctx, store, res, rows)
	if res.Answer != "" {
		t.Fatal("published before all stages settled")
	}
}

func TestPlanDeliveryChecksKeepBothRequirements(t *testing.T) {
	user, err := contract.Validate([]contract.Check{{ID: "verify", Exists: []string{"delivered.txt"}}})
	if err != nil {
		t.Fatal(err)
	}
	stage, err := contract.Validate([]contract.Check{{ID: "verify", Exists: []string{"integrated.txt"}}, {ID: "same", Run: "go test ./..."}})
	if err != nil {
		t.Fatal(err)
	}
	user = append(user, stage[1])
	checks, err := planDeliveryChecks(stage, user)
	if err != nil || len(checks) != 3 || checks[0].ID != "verify" || checks[0].Exists[0] != "delivered.txt" || checks[2].ID == "verify" || checks[2].Exists[0] != "integrated.txt" {
		t.Fatalf("lost or duplicated a delivery requirement: %+v %v", checks, err)
	}
}

// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"encoding/json"
	"github.com/Xustalis/OpenPanda/internal/objectives"
	"testing"
)

func testObjectivePolicy() *objectives.Policy {
	return &objectives.Policy{Version: 1, Revision: 1, Dimensions: []objectives.Dimension{{Dimension: "quality", Weight: 1, Priority: objectives.Max, Requirement: "Must pass required checks", Reason: "User explicitly required validated delivery", Criteria: []string{"Relevant regression verified"}}}}
}

func TestParseTaskAndPlanObjectives(t *testing.T) {
	task := &TaskSpec{Title: "deliver", ContextType: "file", Requires: Requires{Abilities: []string{"coding"}}, Spec: TaskSpecDetail{Objectives: testObjectivePolicy()}}
	raw, _ := json.Marshal(map[string]any{"kind": "task", "task": task})
	out, err := ParseOutput(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if out.Task.Spec.Objectives.Dimensions[0].Priority != objectives.Max {
		t.Fatal("task lost MAX")
	}
	plan := &PlanSpec{Goal: "deliver", Objectives: testObjectivePolicy(), Stages: []PlanStageSpec{{ID: "build", Intent: "build"}}}
	if err := ValidatePlanSpec(plan); err != nil {
		t.Fatal(err)
	}
	weak := testObjectivePolicy()
	weak.Dimensions[0].Priority = objectives.Normal
	plan.Stages[0].Objectives = weak
	if ValidatePlanSpec(plan) == nil {
		t.Fatal("plan accepted weakened MAX")
	}
}

func TestParseSupervisorObjectiveAssessments(t *testing.T) {
	v, err := parseSuperviseVerdict(`{"status":"done","reason":"verified","objective_assessments":[{"dimension":"quality","criterion":0,"status":"passed","evidence":"recorded test output"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := objectives.VerifyAssessments(testObjectivePolicy(), v.ObjectiveAssessments, false); err != nil {
		t.Fatal(err)
	}
}

func TestGeneratedObjectivesRequiredButLegacyTasksRemainReadable(t *testing.T) {
	legacy := &TaskSpec{Title: "old", ContextType: "file", Requires: Requires{Abilities: []string{"coding"}}}
	if err := ValidateTaskSpec(legacy); err != nil {
		t.Fatal("legacy task no longer readable:", err)
	}
	if err := ValidateGeneratedObjectives(Output{Kind: KindTask, Task: legacy}); err == nil {
		t.Fatal("new task silently lost objectives")
	}
	if err := ValidateGeneratedObjectives(Output{Kind: KindPlan, Plan: &PlanSpec{Goal: "new"}}); err == nil {
		t.Fatal("new plan silently lost objectives")
	}
	legacy.Spec.Objectives = testObjectivePolicy()
	if err := ValidateGeneratedObjectives(Output{Kind: KindTask, Task: legacy}); err != nil {
		t.Fatal(err)
	}
}

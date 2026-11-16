// SPDX-License-Identifier: AGPL-3.0-or-later

package plan

import (
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/objectives"
	"testing"
)

func TestPlanObjectivesPersistToEveryStageAndSynthesis(t *testing.T) {
	policy := &objectives.Policy{Version: 1, Revision: 1, Dimensions: []objectives.Dimension{{Dimension: "quality", Weight: 1, Priority: objectives.Max, Requirement: "Deliver verified work", Reason: "Owner requirement", Criteria: []string{"Regression and end-to-end pass"}}}}
	p, err := FromSpec(entry.PlanSpec{Goal: "deliver", Objectives: policy, Stages: []entry.PlanStageSpec{{ID: "code", Intent: "implement"}, {ID: "test", Intent: "verify"}}})
	if err != nil {
		t.Fatal(err)
	}
	p, err = WithSynthesis(p, "", "Integrate", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Stages) != 3 {
		t.Fatalf("stages %d", len(p.Stages))
	}
	for _, s := range p.Stages {
		got, err := objectives.FromSpecJSON(s.SpecJSON)
		if err != nil || !objectives.HasMax(got) {
			t.Fatalf("%s lost MAX: %s %v", s.ID, s.SpecJSON, err)
		}
	}
	policy.Dimensions[0].Criteria[0] = "changed after conversion"
	got, _ := objectives.FromSpecJSON(p.Stages[0].SpecJSON)
	if got.Dimensions[0].Criteria[0] == policy.Dimensions[0].Criteria[0] {
		t.Fatal("stage contract aliased source")
	}
}

func TestYAMLPlanObjectivesInherited(t *testing.T) {
	p, err := Parse([]byte(`goal: verified delivery
objectives:
  version: 1
  revision: 1
  dimensions:
    - dimension: quality
      weight: 1
      priority: MAX
      requirement: Verified owner deliverable
      reason: Owner requested acceptance
      criteria: [Regression passes]
stages:
  - id: work
    intent: Implement and verify
`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := objectives.FromSpecJSON(p.Stages[0].SpecJSON)
	if err != nil || !objectives.HasMax(got) {
		t.Fatalf("lost authored objectives: %+v %v", p, err)
	}
}

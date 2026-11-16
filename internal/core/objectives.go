// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"encoding/json"
	"fmt"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/objectives"
	"github.com/Xustalis/OpenPanda/internal/plan"
)

func inheritTaskObjectives(parent, child string) (string, error) {
	return objectives.InheritSpecJSON(parent, child)
}

func planStageObjectives(p plan.Plan, child string) (string, error) {
	if p.Objectives == nil {
		return inheritTaskObjectives("", child)
	}
	raw, err := json.Marshal(map[string]any{"objectives": p.Objectives})
	if err != nil {
		return "", err
	}
	return inheritTaskObjectives(string(raw), child)
}

func taskHasMaxObjectives(t Task) bool {
	p, err := objectives.FromSpecJSON(t.SpecJSON)
	return err == nil && objectives.HasMax(p)
}

func objectiveIntermediate(t Task) bool {
	var detail struct {
		PlanFinal bool `json:"plan_final"`
	}
	_ = json.Unmarshal([]byte(t.SpecJSON), &detail)
	return t.ParentID != "" || (t.PlanID != "" && !detail.PlanFinal)
}

func objectiveTaskContext(t Task) string {
	policy, err := objectives.FromSpecJSON(t.SpecJSON)
	if err != nil || !objectives.HasMax(policy) {
		return ""
	}
	if objectiveIntermediate(t) {
		return "\n[Objective responsibility: intermediate] Prove all criteria in this task's scope. Global criteria outside its scope remain pending for the parent/final integrator; identify the responsible integration stage and report their evidence honestly.\n"
	}
	return "\n[Objective responsibility: final delivery] Every MAX criterion needs independent passed evidence; deferred or unknown criteria cannot complete this goal.\n"
}

func objectiveVerdict(t Task, v entry.SuperviseVerdict) entry.SuperviseVerdict {
	if v.Status != entry.VerdictDone {
		return v
	}
	policy, err := objectives.FromSpecJSON(t.SpecJSON)
	if err == nil {
		err = objectives.VerifyAssessments(policy, v.ObjectiveAssessments, objectiveIntermediate(t))
	}
	if err != nil {
		v.Status = entry.VerdictReview
		v.Reason = err.Error()
		v.Followup = "Provide independent evidence for the unchanged MAX criteria, or resolve the stated capability/budget conflict. Do not silently lower the objectives."
	}
	return v
}

// objectiveResultEvidence validates the model-produced contract and recorded
// reviewer evidence. It never maps natural language to objectives.
func objectiveResultEvidence(t Task, result any) error {
	policy, err := objectives.FromSpecJSON(t.SpecJSON)
	if err != nil || !objectives.HasMax(policy) {
		return err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("objectives: encode acceptance evidence: %w", err)
	}
	var payload bus.TaskResultPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("objectives: decode acceptance evidence: %w", err)
	}
	return objectives.VerifyAssessments(policy, payload.ObjectiveAssessments, objectiveIntermediate(t))
}

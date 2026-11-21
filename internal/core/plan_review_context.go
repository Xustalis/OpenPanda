// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"

	"github.com/Xustalis/OpenPanda/internal/entry"
)

// Only the origin has the whole graph. A delegated copy can carry a chain or
// a plan ID supplied by a peer, neither of which grants orchestration rights.
func (c *Core) planReviewOptions(ctx context.Context, task Task, opts entry.SuperviseOptions) entry.SuperviseOptions {
	if task.PlanID == "" || task.StageID == "" || task.Remote || task.ParentID != "" || !objectiveIntermediate(task) || !c.orchestratesAny([]Task{task}) {
		return opts
	}
	stages, err := c.store.PlanStages(ctx, task.PlanID)
	if err != nil {
		c.logger.Warn("read plan graph for supervisor", "task", task.TaskID, "err", err)
		return opts
	}
	type stage struct {
		ID         string   `json:"id"`
		State      string   `json:"state"`
		Assignment string   `json:"assignment"`
		Needs      []string `json:"needs,omitempty"`
		Final      bool     `json:"final,omitempty"`
	}
	graph := struct {
		Source string  `json:"source_stage"`
		Goal   string  `json:"goal,omitempty"`
		Stages []stage `json:"stages"`
	}{Source: task.StageID}
	hasFinal := false
	for _, t := range stages {
		if t.Remote || t.ParentID != "" {
			continue
		}
		final := !objectiveIntermediate(t)
		hasFinal = hasFinal || final
		graph.Stages = append(graph.Stages, stage{ID: t.StageID, State: t.State, Assignment: boundedEvidenceText(t.Intent, 240), Needs: t.Needs, Final: final})
	}
	if !hasFinal {
		return opts
	} // hand-authored graphs may have no synthesis contract
	var goal string
	if err := c.db.QueryRowContext(ctx, `SELECT json_extract(data_json,'$.plan_goal') FROM task_events WHERE task_id=? AND type=? AND json_type(data_json,'$.plan_goal')='text' ORDER BY id LIMIT 1`, task.TaskID, EvClassifyResult).Scan(&goal); err == nil {
		graph.Goal = boundedEvidenceText(goal, 1000)
	}
	raw, err := json.Marshal(graph)
	if err == nil {
		opts.PlanContext = string(raw)
	}
	return opts
}

func (c *Core) applyReviewedPlanActions(ctx context.Context, task Task, round int, verdict entry.SuperviseVerdict) entry.SuperviseVerdict {
	if verdict.Status == entry.VerdictDone || verdict.Status == entry.VerdictContinue {
		for i, action := range verdict.PlanActions {
			if err := c.applyPlanAction(ctx, task, round, i, action); err != nil {
				c.EvTrace(context.WithoutCancel(ctx), task.TaskID, EvPlanIntervention, map[string]any{
					"plan_id": task.PlanID, "source_stage": task.StageID, "target_stage": boundedEvidenceText(action.TargetStage, 128),
					"kind": boundedEvidenceText(action.Kind, 32), "instruction": boundedEvidenceText(action.Instruction, 2048), "reason": boundedEvidenceText(action.Reason, 512),
					"outcome": "rejected", "detail": boundedEvidenceText(err.Error(), 512), "round": round,
				})
				verdict.Status = entry.VerdictReview
				verdict.Reason = "Plan adjustment requires review: " + boundedEvidenceText(err.Error(), 512)
				break
			}
		}
	}
	return verdict
}

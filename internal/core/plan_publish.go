// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"database/sql"
	"encoding/json"
)

// All source IO and context packing precede this transaction. No stage is
// visible to PendingPlans until every dependency and input is durable.
func (s *TaskStore) createPlanStages(ctx context.Context, stages []Task, goal string, partial []string, sourceDir string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for i := range stages {
			t := &stages[i]
			chain, _ := json.Marshal(t.Chain)
			requires, _ := json.Marshal(t.Requires)
			keys, _ := json.Marshal(t.ResourceKeys)
			needs, _ := json.Marshal(t.Needs)
			inputs, _ := json.Marshal(t.Inputs)
			now := s.now()
			t.CreatedAt, t.UpdatedAt = now, now
			_, err := tx.ExecContext(ctx, `INSERT INTO tasks
   (task_id,parent_id,project,title,state,owner_node,attempt_id,state_version,chain_json,remote,created_at,updated_at,
    intent,spec_json,context_type,context_hash,resource_json,requires_json,authorized,token_budget,
    priority,session_id,resource_keys_json,scheduled,plan_id,stage_id,needs_json,input_artifacts_json)
    VALUES (?,'',?,?,?,?,?,0,?,0,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?,?,?)`,
				t.TaskID, t.Project, t.Title, StateSubmitted, t.OwnerNode, t.AttemptID, string(chain), now, now,
				t.Intent, t.SpecJSON, t.ContextType, t.ContextHash, t.ResourceJSON, string(requires), t.Authorized, t.TokenBudget,
				t.Priority, t.SessionID, string(keys), t.PlanID, t.StageID, string(needs), string(inputs))
			if err != nil {
				return err
			}
			if session, err := s.attachAskRoundTx(ctx, tx, t.TaskID); err != nil {
				return err
			} else if session != "" {
				t.SessionID = session
			}
			if err := s.recordEventTx(ctx, tx, t.TaskID, EvSubmit, map[string]any{"parent": "", "owner": t.OwnerNode}); err != nil {
				return err
			}
			if len(partial) > 0 {
				if err := s.recordEventTx(ctx, tx, t.TaskID, EvContextDegraded, map[string]any{"stage": "plan_source_partial", "dir": sourceDir, "skipped": partial}); err != nil {
					return err
				}
			}
			if err := s.recordEventTx(ctx, tx, t.TaskID, EvClassifyResult, map[string]any{"kind": "plan", "note": goal, "plan_goal": goal, "stages_count": len(stages), "stage_id": t.StageID}); err != nil {
				return err
			}
		}
		return nil
	})
}

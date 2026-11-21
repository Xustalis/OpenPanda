// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
)

// PlanAudit is the origin's durable record of changes to a running graph.
// State and verification describe the target at the time this view was read.
type PlanAudit struct {
	Decisions []PlanDecision `json:"decisions"`
	Omitted   int            `json:"earlier_decisions_omitted,omitempty"`
}

type PlanDecision struct {
	DecisionID      string `json:"decision_id,omitempty"`
	Round           int    `json:"round,omitempty"`
	RequestedTarget string `json:"requested_target,omitempty"`
	ReadEventID     int64  `json:"read_event_id,omitempty"`
	EventID         int64  `json:"event_id"`
	Source          string `json:"source_stage"`
	Target          string `json:"target_stage,omitempty"`
	Kind            string `json:"kind"`
	Outcome         string `json:"outcome"`
	Reason          string `json:"reason,omitempty"`
	Instruction     string `json:"instruction,omitempty"`
	Detail          string `json:"detail,omitempty"`
	State           string `json:"target_state,omitempty"`
	Verification    string `json:"target_verification,omitempty"`
}

func (s *TaskStore) PlanAudit(ctx context.Context, planID string) (PlanAudit, error) {
	return readPlanAudit(ctx, s.db, planID)
}

// An ordinary cancellation preserves the row's previous result. Use the origin's
// cancellation event to distinguish a withdrawn branch from an operator stop.
const withdrawnPlanStageSQL = `CASE WHEN t.state='cancelled' AND t.plan_id!='' AND t.remote=0 THEN COALESCE((
 SELECT json_extract(e.data_json,'$.plan_withdrawn')=1 AND json_type(e.data_json,'$.decision_id')='text'
 FROM task_events e WHERE e.task_id=t.task_id AND e.type='cancel' ORDER BY e.id DESC LIMIT 1),0) ELSE 0 END`

func (s *TaskStore) PlanStageWithdrawn(ctx context.Context, id string) (bool, error) {
	var withdrawn bool
	err := s.db.QueryRowContext(ctx, `SELECT `+withdrawnPlanStageSQL+` FROM tasks t WHERE t.task_id=?`, id).Scan(&withdrawn)
	return withdrawn, err
}

func readPlanAudit(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, planID string) (PlanAudit, error) {
	const limit = 32
	var audit PlanAudit
	rows, err := db.QueryContext(ctx, `SELECT e.id, src.stage_id, e.data_json, COALESCE(dst.state,''), COALESCE(dst.verification,''), COALESCE((SELECT MAX(r.id) FROM task_events r WHERE r.task_id=dst.task_id AND r.type=? AND json_extract(r.data_json,'$.decision_id')=json_extract(e.data_json,'$.decision_id')),0), COUNT(*) OVER ()
 FROM tasks src JOIN task_events e ON e.task_id=src.task_id
 LEFT JOIN tasks dst ON dst.plan_id=src.plan_id AND dst.stage_id=json_extract(e.data_json,'$.target_stage') AND dst.parent_id='' AND dst.remote=0
 WHERE src.plan_id=? AND src.parent_id='' AND src.remote=0 AND e.type=? ORDER BY e.id DESC LIMIT ?`, EvPlanAdjustmentRead, planID, EvPlanIntervention, limit)
	if err != nil {
		return audit, err
	}
	defer rows.Close()
	for rows.Next() {
		var d PlanDecision
		var id, readID int64
		var source, raw, state, verification string
		var total int
		if err := rows.Scan(&id, &source, &raw, &state, &verification, &readID, &total); err != nil {
			return audit, err
		}
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			return audit, err
		}
		d.EventID, d.Source, d.State, d.Verification = id, source, state, verification
		d.ReadEventID = readID
		d.Reason = boundedEvidenceText(d.Reason, 240)
		d.Instruction = boundedEvidenceText(d.Instruction, 300)
		d.Detail = boundedEvidenceText(d.Detail, 160)
		audit.Decisions = append(audit.Decisions, d)
		audit.Omitted = max(0, total-limit)
	}
	slices.Reverse(audit.Decisions)
	return audit, rows.Err()
}

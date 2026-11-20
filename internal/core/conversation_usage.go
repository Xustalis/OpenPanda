// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// Task completion may become visible before the wire handler records usage.
// In the metric insert's transaction, charge any already-settled goal ledgers;
// a concurrent settlement either sees the inserted metric or precedes this
// adjustment. Unsettled ledgers continue to use their ordinary recursive sum.
func chargeSettledGoalMetric(ctx context.Context, tx *sql.Tx, taskID string, tokens int64) error {
	rows, err := tx.QueryContext(ctx, `WITH RECURSIVE ancestors(id,parent_id) AS (
 SELECT task_id,parent_id FROM tasks WHERE task_id=?
 UNION SELECT t.task_id,t.parent_id FROM tasks t JOIN ancestors a ON t.task_id=a.parent_id
 ) UPDATE conversation_goal_rounds SET task_tokens=task_tokens+?
 WHERE settled=1 AND round_id IN (SELECT a.round_id FROM ask_round_tasks a JOIN ancestors p ON p.id=a.task_id)
 RETURNING goal_id`, taskID, tokens)
	if err != nil {
		return err
	}
	deltas := map[string]int64{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		deltas[id] += tokens
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for id, delta := range deltas {
		var sid string
		err = tx.QueryRowContext(ctx, `UPDATE conversation_goals SET tokens=tokens+?,
 status=CASE WHEN status='active' AND token_budget IS NOT NULL AND tokens+?>=token_budget THEN 'budget_limited' ELSE status END,updated_at=?
 WHERE id=? RETURNING session_id`, delta, delta, time.Now().UnixMilli(), id).Scan(&sid)
		if err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{"id": id, "tokens": delta})
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO conversation_events(session_id,kind,payload,created_at) VALUES(?,'goal_settled',?,?)", sid, string(payload), time.Now().UnixMilli()); err != nil {
			return err
		}
	}
	return nil
}

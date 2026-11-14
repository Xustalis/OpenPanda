// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
)

func (s *TaskStore) blockReviewDelivery(ctx context.Context, t Task, result bus.TaskResultPayload) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `UPDATE tasks SET approval_disposition=?,result_json=?,state_version=state_version+1,updated_at=? WHERE task_id=? AND attempt_id=? AND state='review'`, ApprovalNeedsChangedInput, string(raw), s.now(), t.TaskID, t.AttemptID)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return ErrConflict
		}
		return s.recordEventTx(ctx, tx, t.TaskID, EvProgress, map[string]any{"note": result.Stderr, "delivery": "blocked"})
	})
}

// Consent to merge is not evidence that the executor delivered the tree.
func (c *Core) remoteIsolatedExecutor(ctx context.Context, t Task) bool {
	target, err := c.store.DispatchTarget(ctx, t.TaskID)
	return err == nil && target != "" && !scheduler.SameRuntimeIdentity(target, c.nodeID)
}

func (s *TaskStore) acceptPendingDelivery(ctx context.Context, t Task) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `UPDATE tasks SET delivery_accept_attempt=attempt_id,updated_at=? WHERE task_id=? AND attempt_id=? AND state='review' AND approval_disposition=?`, s.now(), t.TaskID, t.AttemptID, ApprovalAcceptWork)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return ErrConflict
		}
		return s.recordEventTx(ctx, tx, t.TaskID, EvProgress, map[string]any{"note": "accepted; waiting for executor delivery", "delivery": "pending"})
	})
}

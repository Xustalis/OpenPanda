// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/taskproviders/tsync"
	"github.com/Xustalis/OpenPanda/internal/util"
)

func (s *Service) lockSubject(ctx context.Context, tx *sql.Tx, f flowRun) error {
	r, err := tx.ExecContext(ctx, `UPDATE task_connections SET updated_at=updated_at WHERE connection_id=? AND account_id=? AND auth_epoch=? AND generation=? AND state='connected'`, f.ConnectionID, f.AccountID, f.AuthEpoch, f.Generation)
	if err == nil {
		err = requireOne(r)
	}
	return err
}

// DecideDelivery records one immutable, revision-bound human decision. Repeating
// it reuses its original proof and event; a contrary decision requires a new
// delivery revision. Returning work never claims to undo a physical action.
func (s *Service) DecideDelivery(ctx context.Context, id string, revision uint64, decision, reason string) error {
	if decision != "accept" && decision != "return" || len(reason) > 16384 {
		return taskproviders.ErrUnsupported
	}
	d, err := s.delivery(ctx, id, revision)
	if err != nil {
		return err
	}
	f, err := s.flow(ctx, d.Delivery.RequestID)
	if err != nil {
		return err
	}
	if f.Policy.Mode != taskproviders.AfterExecution {
		return taskproviders.ErrUnsupported
	}
	c, err := s.consentAuthority(ctx, f, "delivery:report-decision")
	if err != nil {
		return err
	}
	// Resolve retries before generating another nonce/signature.
	var old, oldRaw string
	var renewOld string
	err = s.db.QueryRowContext(ctx, `SELECT decision,proof_json FROM work_delivery_decisions WHERE delivery_id=? AND revision=? AND account_id=?`, id, revision, c.AccountID).Scan(&old, &oldRaw)
	if err == nil {
		if old != decision || !decisionReasonMatches(oldRaw, reason) {
			return taskproviders.ErrConflict
		}
		var prior tsync.DeliveryDecision
		if json.Unmarshal([]byte(oldRaw), &prior) != nil || prior.ConsentProof == nil {
			return taskproviders.ErrConflict
		}
		expires, parseErr := time.Parse(time.RFC3339Nano, prior.ConsentProof.Payload.ExpiresAt)
		var rejected int
		if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_sync_outbox WHERE event_id=? AND state='conflict' AND error_code='decision_rejected'`, prior.DecisionID).Scan(&rejected); err != nil {
			return err
		}
		if parseErr != nil || expires.After(s.now()) || rejected != 1 {
			return nil
		}
		renewOld = oldRaw
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	nonce, err := util.UUIDv7()
	if err != nil {
		return err
	}
	proof, err := s.signConsent(ctx, c, consentPayload(f, c, d, decision, nonce, s.now()))
	if err != nil {
		return err
	}
	v := tsync.DeliveryDecision{DecisionID: nonce, Revision: revision, Decision: decision, ConsentProof: &proof}
	if reason != "" {
		v.Reason = &reason
	}
	raw, err := tsync.JSONBytes(v)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.lockSubject(ctx, tx, f); err != nil {
		return err
	}
	var head uint64
	if err = tx.QueryRowContext(ctx, `SELECT MAX(revision) FROM work_deliveries WHERE delivery_id=?`, id).Scan(&head); err != nil {
		return err
	}
	if head != revision {
		return taskproviders.ErrConflict
	}
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM task_flow_runs WHERE request_id=?`, f.ID).Scan(&state); err != nil {
		return err
	}
	if state == "paused" || state == "cancelled" || state == "superseded" {
		return taskproviders.ErrConflict
	}
	var unresolved int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM final_actions WHERE request_id=? AND state NOT IN ('verified','rejected','failed')`, f.ID).Scan(&unresolved); err != nil {
		return err
	}
	if unresolved > 0 {
		return errors.New("final action must be resolved before acceptance")
	}
	r, err := tx.ExecContext(ctx, `INSERT INTO work_delivery_decisions(decision_id,delivery_id,revision,payload_hash,account_id,decision,proof_json,created_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(delivery_id,revision,account_id) DO NOTHING`, nonce, id, revision, digest(v), c.AccountID, decision, string(raw), s.now().UnixMilli())
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		if err = tx.QueryRowContext(ctx, `SELECT decision,proof_json FROM work_delivery_decisions WHERE delivery_id=? AND revision=? AND account_id=?`, id, revision, c.AccountID).Scan(&old, &oldRaw); err != nil {
			return err
		}
		if old != decision || !decisionReasonMatches(oldRaw, reason) {
			return taskproviders.ErrConflict
		}
		if renewOld != "" && renewOld == oldRaw {
			var prior tsync.DeliveryDecision
			if err = json.Unmarshal([]byte(oldRaw), &prior); err != nil {
				return err
			}
			var rejected int
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_sync_outbox WHERE event_id=? AND state='conflict' AND error_code='decision_rejected'`, prior.DecisionID).Scan(&rejected); err != nil {
				return err
			}
			if rejected != 1 {
				return taskproviders.ErrConflict
			}
			if err = s.replaceEvent(ctx, tx, f.ID, prior.DecisionID, "", "decision", oldRaw, nonce, "decision", raw); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `UPDATE work_delivery_decisions SET decision_id=?,payload_hash=?,proof_json=?,created_at=? WHERE delivery_id=? AND revision=? AND account_id=? AND proof_json=?`, nonce, digest(v), string(raw), s.now().UnixMilli(), id, revision, c.AccountID, oldRaw)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `UPDATE task_flow_runs SET error_code='decision_pending_sync' WHERE request_id=?`, f.ID)
			if err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	if err = s.enqueueOutbox(ctx, tx, f, "decision", nonce, v); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE task_flow_runs SET error_code='decision_pending_sync',updated_at=? WHERE request_id=?`, s.now().UnixMilli(), f.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) RejectAction(ctx context.Context, id, fingerprint string) error {
	a, err := s.action(ctx, id)
	if err != nil {
		return err
	}
	f, err := s.flow(ctx, a.RequestID)
	if err != nil {
		return err
	}
	if a.Fingerprint != fingerprint {
		return taskproviders.ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.lockSubject(ctx, tx, f); err != nil {
		return err
	}
	r, err := tx.ExecContext(ctx, `UPDATE final_actions SET state='rejected',proof_json='{}',updated_at=? WHERE action_id=? AND fingerprint=? AND state IN ('prepared','approved','rejected')`, s.now().UnixMilli(), id, fingerprint)
	if err == nil {
		err = requireOne(r)
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE approval_records SET expires_at=0 WHERE action_id=?`, id)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE task_flow_runs SET state='paused',error_code='submit_rejected',deadline=0,lease_owner='',updated_at=? WHERE request_id=?`, s.now().UnixMilli(), f.ID)
	if err != nil {
		return err
	}
	if err = s.enqueuePause(ctx, tx, f); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) PauseWork(ctx context.Context, request string) error {
	f, err := s.flow(ctx, request)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.lockSubject(ctx, tx, f); err != nil {
		return err
	}
	if f.State == "succeeded" || f.State == "cancelled" || f.State == "superseded" {
		return taskproviders.ErrConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE task_flow_runs SET state='paused',error_code='user_paused',deadline=0,lease_owner='',updated_at=? WHERE request_id=? AND state NOT IN ('succeeded','cancelled','superseded')`, s.now().UnixMilli(), f.ID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE approval_records SET expires_at=0 WHERE action_id IN (SELECT action_id FROM final_actions WHERE request_id=?)`, f.ID)
	if err != nil {
		return err
	}
	if err = s.enqueuePause(ctx, tx, f); err != nil {
		return err
	}
	return tx.Commit()
}

type releaseEvent struct {
	EventID     string      `json:"eventId"`
	ExecutionID string      `json:"executionId"`
	RequestID   string      `json:"requestId"`
	Fence       tsync.Fence `json:"fence"`
	Reason      string      `json:"reason"`
}

func (s *Service) enqueuePause(ctx context.Context, tx *sql.Tx, f flowRun) error {
	if f.ExecutionID == "" {
		return nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_sync_outbox WHERE request_id=? AND kind='release'`, f.ID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	id, err := util.UUIDv7()
	if err != nil {
		return err
	}
	v := releaseEvent{EventID: id, ExecutionID: f.ExecutionID, RequestID: f.ID, Fence: f.Record.Fence, Reason: "paused"}
	if err = s.enqueueOutbox(ctx, tx, f, "release", id, v); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE task_flow_runs SET error_code='pause_pending_sync' WHERE request_id=?`, f.ID)
	return err
}

// flushPendingResults is independent of preparation and action dispatch. It can
// run on a sync-only owner and will never resume local work or spend model tokens.
func (s *Service) flushPendingResults(ctx context.Context, owner string) {
	_ = s.reconcileLocalReceipts(ctx)
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT request_id FROM task_sync_outbox WHERE state IN ('pending','sending') AND next_at<=? UNION SELECT f.request_id FROM task_flow_runs f JOIN final_actions a ON a.request_id=f.request_id JOIN action_receipts r ON r.action_id=a.action_id WHERE f.state='paused' AND f.execution_id!='' AND NOT EXISTS(SELECT 1 FROM task_sync_outbox o WHERE o.request_id=f.request_id AND o.kind='receipt_evidence' AND o.revision=a.action_id) LIMIT 32`, s.now().UnixMilli())
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	_ = rows.Close()
	for _, id := range ids {
		call, cancel := context.WithTimeout(ctx, 30*time.Second)
		_ = s.flushOutbox(call, owner, id)
		cancel()
	}
}

func decisionReasonMatches(raw, reason string) bool {
	var v tsync.DeliveryDecision
	if json.Unmarshal([]byte(raw), &v) != nil {
		return false
	}
	return v.Reason == nil && reason == "" || v.Reason != nil && *v.Reason == reason
}

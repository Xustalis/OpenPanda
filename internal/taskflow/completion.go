// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Xustalis/OpenPanda/internal/taskflow/finalaction"
	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/taskproviders/tsync"
	"github.com/Xustalis/OpenPanda/internal/util"
)

// completionAuthority deliberately permits waiting/reconciliation statuses only
// after preparation was durably recorded. They never authorize another model run.
func completionAuthority(f flowRun, r tsync.ExecutionRecord) bool {
	if r.Status == taskproviders.Queued || r.Status == taskproviders.Running {
		return true
	}
	return f.Prepared != "" && (r.Status == taskproviders.AwaitingSubmit || r.Status == taskproviders.AwaitingAcceptance || r.Status == taskproviders.NeedsReconciliation)
}

func (m *managedExecution) queueAction(ctx context.Context, f flowRun, c Connection, a Action, status string, receipt *finalaction.Receipt, localState string) error {
	s := m.runtime.service
	event, err := util.UUIDv7()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	v := tsync.FinalAction{ActionID: a.ID, EventID: event, RequestID: f.ID, ExecutionID: f.ExecutionID, Fence: m.record.Fence, DeliveryID: a.DeliveryID, DeliveryRevision: a.Revision, Status: status, Fingerprint: a.Fingerprint, ConsentProof: a.Proof,
		Verification: tsync.Verification{Status: "pending", Method: "file-publication", Summary: "Awaiting verified file receipt.", EvidenceHashes: []string{}}}
	if receipt != nil {
		v.ReceiptHash = &receipt.SHA256
		v.Verification = tsync.Verification{Status: "passed", Method: receipt.Method, Summary: "Published content and action provenance verified.", EvidenceHashes: []string{receipt.SHA256}}
	}
	return s.write(ctx, m.owner, c, func(tx *sql.Tx) error {
		// Evidence after an uncertain action remains durable even if authority
		// expired. It may only be synchronized under the current account/fence.
		if status == "prepared" || status == "dispatching" {
			if err := s.requireActiveRun(ctx, tx, f.ID, m.owner); err != nil {
				return err
			}
			if err := s.automationSnapshotTx(ctx, tx, c, f.Policy, f.Snapshot); err != nil {
				return err
			}
			if time.Until(m.deadline) <= 0 {
				return errors.New("execution lease expired")
			}
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_sync_outbox WHERE request_id=? AND kind='final_action' AND json_extract(body_json,'$.actionId')=? AND json_extract(body_json,'$.status')=?`, f.ID, a.ID, status).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		if localState != "" {
			r, err := tx.ExecContext(ctx, `UPDATE final_actions SET state=?,dispatch_id=CASE WHEN ?='dispatching' THEN ? ELSE dispatch_id END,updated_at=? WHERE action_id=? AND fingerprint=? AND state=?`, localState, localState, event, s.now().UnixMilli(), a.ID, a.Fingerprint, a.State)
			if err == nil {
				err = requireOne(r)
			}
			if err != nil {
				return err
			}
		}
		if receipt != nil {
			raw, err := json.Marshal(receipt)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO action_receipts(action_id,dispatch_id,receipt_json,verified,created_at) VALUES(?,?,?,1,?) ON CONFLICT(action_id) DO NOTHING`, a.ID, event, string(raw), s.now().UnixMilli())
			if err != nil {
				return err
			}
		}
		return s.enqueueOutbox(ctx, tx, f, "final_action", event, v)
	})
}

// reconcileLocalReceipts observes local evidence even when policy or cloud
// authority has expired. It cannot enqueue an action or advance a remote run.
func (s *Service) reconcileLocalReceipts(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT action_id FROM final_actions WHERE state IN ('dispatching','unknown') ORDER BY updated_at LIMIT 64`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		a, e := s.action(ctx, id)
		if e != nil {
			return e
		}
		receipt, e := finalaction.Reconcile(ctx, a.Intent)
		if e == nil {
			if e = s.saveReceipt(a, receipt, "reconciled"); e != nil {
				return e
			}
		}
	}
	return ctx.Err()
}

// saveReceipt preserves late facts even after cancellation or lost authority.
// It never changes a run, queue, approval, or remote execution state.
func (s *Service) saveReceipt(a Action, receipt finalaction.Receipt, status string) error {
	if receipt.ActionID != a.ID || receipt.SHA256 != a.Intent.SHA256 || receipt.Filename != a.Intent.Filename || receipt.Size != int64(len(a.Intent.Content)) || (status != "succeeded" && status != "reconciled") {
		return taskproviders.ErrConflict
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `UPDATE final_actions SET state='verified',updated_at=? WHERE action_id=? AND fingerprint=? AND state IN ('dispatching','unknown','verified')`, s.now().UnixMilli(), a.ID, a.Fingerprint)
	if err == nil {
		err = requireOne(r)
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO action_receipts(action_id,dispatch_id,receipt_json,verified,created_at,report_status) SELECT action_id,dispatch_id,?,1,?,? FROM final_actions WHERE action_id=? ON CONFLICT(action_id) DO NOTHING`, string(raw), s.now().UnixMilli(), status, a.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (m *managedExecution) finishPrepared(ctx context.Context) error {
	s := m.runtime.service
	f, c, err := m.checkpoint(ctx, true)
	if err != nil {
		return err
	}
	d, err := m.ensureDelivery(ctx, f, c)
	if err != nil {
		return err
	}
	if err = s.flushOutbox(ctx, m.owner, f.ID); err != nil {
		return err
	}
	if !f.Policy.FinalAction {
		reason := "awaiting_submit"
		if f.Policy.Mode == taskproviders.AfterExecution {
			reason = "awaiting_acceptance"
		}
		return m.release(reason)
	}
	var actionID string
	if err = s.db.QueryRowContext(ctx, `SELECT action_id FROM final_actions WHERE delivery_id=? AND delivery_revision=?`, d.Delivery.DeliveryID, d.Delivery.Revision).Scan(&actionID); err != nil {
		return err
	}
	a, err := s.action(ctx, actionID)
	if err != nil {
		return err
	}
	if a.State == "prepared" {
		if f.Policy.Mode == taskproviders.BeforeSubmit {
			return m.release("awaiting_submit")
		}
		if err = s.approveAction(ctx, f, a, a.Fingerprint, true); err != nil {
			return err
		}
		a, err = s.action(ctx, actionID)
		if err != nil {
			return err
		}
	}
	if a.State == "approved" {
		if err = m.queueAction(ctx, f, c, a, "prepared", nil, ""); err != nil {
			return err
		}
		if err = s.flushOutbox(ctx, m.owner, f.ID); err != nil {
			return err
		}
		if err = m.actionCheckpoint(ctx, a); err != nil {
			return err
		}
		// The durable dispatch marker always precedes the remote report and the
		// physical action. Recovery from any subsequent failure only observes.
		if err = m.queueAction(ctx, f, c, a, "dispatching", nil, "dispatching"); err != nil {
			return err
		}
		if err = s.flushOutbox(ctx, m.owner, f.ID); err != nil {
			return err
		}
		a.State = "dispatching"
		receipt, publishErr := finalaction.Publish(ctx, a.Intent, func(check context.Context) error { return m.actionCheckpoint(check, a) })
		if publishErr != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _ = s.db.ExecContext(cleanup, `UPDATE final_actions SET state='unknown',updated_at=? WHERE action_id=? AND state='dispatching'`, s.now().UnixMilli(), a.ID)
			return publishErr
		}
		if err = s.saveReceipt(a, receipt, "succeeded"); err != nil {
			return err
		}
		a.State = "verified"
		if err = m.queueAction(ctx, f, c, a, "succeeded", &receipt, ""); err != nil {
			return err
		}
		a.State = "verified"
	}
	if a.State == "dispatching" || a.State == "unknown" {
		if err = m.queueAction(ctx, f, c, a, "unknown", nil, "unknown"); err != nil {
			return err
		}
		if err = s.flushOutbox(ctx, m.owner, f.ID); err != nil {
			return err
		}
		receipt, reconcileErr := finalaction.Reconcile(ctx, a.Intent)
		if reconcileErr != nil {
			return reconcileErr
		}
		a.State = "unknown"
		if err = s.saveReceipt(a, receipt, "reconciled"); err != nil {
			return err
		}
		a.State = "verified"
		if err = m.queueAction(ctx, f, c, a, "reconciled", &receipt, ""); err != nil {
			return err
		}
		a.State = "verified"
	}
	if a.State != "verified" {
		return errors.New("final action is stopped: " + a.State)
	}
	// Recover a crash between evidence persistence and outbox publication.
	a, err = s.action(ctx, actionID)
	if err != nil {
		return err
	}
	if a.Receipt != nil && a.receiptStatus != "" {
		// Local crash recovery may find the receipt before the cloud observes
		// uncertainty. Preserve dispatching -> unknown -> reconciled, reusing
		// any already persisted unknown event exactly.
		if a.receiptStatus == "reconciled" {
			if err = m.queueAction(ctx, f, c, a, "unknown", nil, ""); err != nil {
				return err
			}
		}
		if err = m.queueAction(ctx, f, c, a, a.receiptStatus, a.Receipt, ""); err != nil {
			return err
		}
	}
	if err = s.flushOutbox(ctx, m.owner, f.ID); err != nil {
		_, _ = s.db.ExecContext(ctx, `UPDATE task_flow_runs SET error_code='completed_pending_sync' WHERE request_id=?`, f.ID)
		return err
	}
	client, err := s.executionClient(c)
	if err != nil {
		return err
	}
	r, err := client.Execution(ctx, f.ExecutionID)
	if err != nil {
		return err
	}
	if !sameExecution(f, r) {
		return taskproviders.ErrAuthorizationChanged
	}
	if r.Status == taskproviders.Succeeded {
		_, err = s.db.ExecContext(ctx, `UPDATE task_flow_runs SET state='succeeded',error_code='',deadline=0,lease_owner='',updated_at=? WHERE request_id=? AND lease_owner=?`, s.now().UnixMilli(), f.ID, m.owner)
		return err
	}
	return m.release("awaiting_acceptance")
}

func sameExecution(f flowRun, r tsync.ExecutionRecord) bool {
	return r.ExecutionID == f.ExecutionID && r.RequestID == f.ID && r.TaskKey == f.Snapshot.Key && r.AccountID == f.AccountID && r.DeviceID == f.Claim.DeviceID && r.Versions.Input == f.Input && r.Versions.Assignment == f.Assignment && r.Versions.Generation == f.Claim.Generation
}

func (m *managedExecution) observeCompleted() (bool, error) {
	s := m.runtime.service
	f, err := s.flow(m.ctx, m.id)
	if err != nil {
		return false, err
	}
	if f.Prepared == "" || f.ExecutionID == "" {
		return false, nil
	}
	c, err := s.consentAuthority(m.ctx, f, "execution:read")
	if err != nil {
		return false, err
	}
	client, err := s.executionClient(c)
	if err != nil {
		return false, err
	}
	r, err := client.Execution(m.ctx, f.ExecutionID)
	if err != nil {
		return false, err
	}
	if !sameExecution(f, r) {
		return false, taskproviders.ErrAuthorizationChanged
	}
	if r.Status != taskproviders.Succeeded {
		return false, nil
	}
	_, err = s.db.ExecContext(m.ctx, `UPDATE task_flow_runs SET state='succeeded',error_code='',deadline=0,lease_owner='',updated_at=? WHERE request_id=?`, s.now().UnixMilli(), f.ID)
	return true, err
}

func (m *managedExecution) actionCheckpoint(ctx context.Context, a Action) error {
	f, _, err := m.checkpoint(ctx, true)
	if err != nil {
		return err
	}
	s := m.runtime.service
	current, err := s.action(ctx, a.ID)
	if err != nil {
		return err
	}
	if current.Fingerprint != a.Fingerprint || (current.State != "approved" && current.State != "dispatching") || current.Intent.ExpiresAt <= s.now().UnixMilli() {
		return taskproviders.ErrAuthorizationChanged
	}
	expires, err := time.Parse(time.RFC3339Nano, current.Proof.Payload.ExpiresAt)
	if err != nil || !expires.After(s.now()) {
		return errors.New("specific submit consent expired")
	}
	var count int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM approval_records WHERE action_id=? AND fingerprint=? AND approver=? AND decision IN ('submit','preauthorized_submit') AND expires_at>?`, a.ID, a.Fingerprint, f.AccountID, s.now().UnixMilli()).Scan(&count)
	if err == nil && count != 1 {
		err = taskproviders.ErrAuthorizationChanged
	}
	return err
}

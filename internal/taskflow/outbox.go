// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/Xustalis/OpenPanda/internal/taskflow/finalaction"
	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/taskproviders/tsync"
	"github.com/Xustalis/OpenPanda/internal/util"
)

// enqueueOutbox records immutable event bytes in the same transaction as the
// local result. Retrying these events can never invoke a model or an actuator.
func (s *Service) enqueueOutbox(ctx context.Context, tx *sql.Tx, f flowRun, kind, event string, body any) error {
	raw, err := tsync.JSONBytes(body)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_sync_outbox(event_id,request_id,connection_id,account_id,auth_epoch,kind,path,body_json,body_hash,created_at) VALUES(?,?,?,?,?,?,?, ?,?,?)`, event, f.ID, f.ConnectionID, f.AccountID, f.AuthEpoch, kind, "", string(raw), finalaction.Hash(raw), s.now().UnixMilli())
	return err
}

// flushOutbox sends one logical run's events in insertion order. The process
// lease and row claim fence stale senders after restart. The server additionally
// deduplicates the exact event/body, including a lost successful response.
func (s *Service) flushOutbox(ctx context.Context, owner, request string) error {
	hashKey := fnv.New64a()
	_, _ = hashKey.Write([]byte(request))
	mu := &s.outboxLocks[hashKey.Sum64()%uint64(len(s.outboxLocks))]
	for !mu.TryLock() {
		if err := wait(ctx, 20*time.Millisecond); err != nil {
			return err
		}
	}
	defer mu.Unlock()
	f, err := s.flow(ctx, request)
	if err != nil {
		return err
	}
	c, err := s.Get(ctx, f.ConnectionID)
	if err != nil {
		return err
	}
	if c.State != "connected" || c.AccountID != f.AccountID || c.AuthEpoch != f.AuthEpoch || c.Generation != f.Generation {
		return taskproviders.ErrAuthorizationChanged
	}
	for {
		if err := s.archiveStoppedEvents(ctx, owner, f, c); err != nil {
			return err
		}
		var event, kind, raw, hash, state string
		var next int64
		err = s.db.QueryRowContext(ctx, `SELECT event_id,kind,body_json,body_hash,state,next_at FROM task_sync_outbox WHERE request_id=? AND state!='sent' ORDER BY CASE WHEN kind='release' THEN 0 ELSE 1 END,rowid LIMIT 1`, request).Scan(&event, &kind, &raw, &hash, &state, &next)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if state == "paused" || state == "conflict" {
			return fmt.Errorf("delivery synchronization requires attention: %s", state)
		}
		if next > s.now().UnixMilli() {
			return errors.New("delivery synchronization is backing off")
		}
		claim, err := util.UUIDv7()
		if err != nil {
			return err
		}
		r, err := s.db.ExecContext(ctx, `UPDATE task_sync_outbox SET state='sending',owner_id=?,lease_until=?,attempts=attempts+1 WHERE event_id=? AND (state='pending' OR (state='sending' AND lease_until<=?)) AND EXISTS(SELECT 1 FROM task_runtime_leases WHERE lease_key='sync' AND owner_id=? AND expires_at>?)`, claim, s.now().Add(time.Minute).UnixMilli(), event, s.now().UnixMilli(), owner, s.now().UnixMilli())
		if err == nil {
			err = requireOne(r)
		}
		if err != nil {
			return err
		}
		var result any
		if finalaction.Hash([]byte(raw)) != hash {
			err = taskproviders.ErrConflict
		} else {
			var live Connection
			scope := "execution:report"
			if kind == "delivery" {
				scope = "delivery:write"
			}
			if kind == "decision" {
				scope = "delivery:report-decision"
			}
			live, err = s.consentAuthority(ctx, f, scope)
			if err == nil {
				var client *tsync.Client
				client, err = s.executionClient(live)
				if err == nil {
					result, err = sendStoredEvent(ctx, client, kind, raw)
				}
			}
		}
		var pauseFence *tsync.HTTPError
		if kind == "release" && errors.As(err, &pauseFence) && pauseFence.Status == 409 && pauseFence.Code == "lease_lost" {
			var replaced bool
			replaced, result, err = s.recoverPauseEvent(ctx, owner, f, c, event, claim, raw)
			if replaced && err == nil {
				continue
			}
		}
		if err != nil {
			var consentErr *tsync.HTTPError
			if errors.As(err, &consentErr) && consentErr.Status == 400 && kind == "final_action" {
				renewed, renewErr := s.renewPreparedEvent(ctx, owner, f, event, claim, raw)
				if renewErr != nil {
					return renewErr
				}
				if renewed {
					continue
				}
			}
			var fenceErr *tsync.HTTPError
			lostFence := errors.As(err, &fenceErr) && fenceErr.Status == 409 && fenceErr.Code == "lease_lost"
			if lostFence {
				rebased, rebaseErr := s.rebaseEvent(ctx, owner, f, event, claim, kind, raw)
				if rebaseErr != nil {
					return rebaseErr
				}
				if rebased {
					continue
				}
			}
			status, code, delay := "pending", "sync_unavailable", 30*time.Second
			var httpErr *tsync.HTTPError
			if errors.As(err, &httpErr) {
				if httpErr.RetryAfter > delay {
					delay = httpErr.RetryAfter
				}
				if httpErr.Status >= 400 && httpErr.Status < 500 && httpErr.Status != 429 {
					status, code = "conflict", "remote_conflict"
				}
				if httpErr.Status == 400 && kind == "decision" {
					code = "decision_rejected"
				}
			}
			if errors.Is(err, taskproviders.ErrConflict) {
				status, code = "conflict", "remote_conflict"
			}
			// A sync-only sender may observe the expired fence before the owning
			// executor obtains its next one. Preserve the exact retry until then.
			if lostFence {
				status, code = "pending", "lease_unavailable"
			}
			if errors.Is(err, taskproviders.ErrReconnect) || errors.Is(err, taskproviders.ErrAuthorizationChanged) {
				status, code = "paused", "authorization_changed"
			}
			_, saveErr := s.db.ExecContext(ctx, `UPDATE task_sync_outbox SET state=?,error_code=?,next_at=?,owner_id='',lease_until=0 WHERE event_id=? AND owner_id=?`, status, code, s.now().Add(delay).UnixMilli(), event, claim)
			if saveErr != nil {
				return saveErr
			}
			if status == "paused" {
				_, _ = s.db.ExecContext(ctx, `UPDATE task_connections SET state='reconnect',last_error='authorization_changed' WHERE connection_id=? AND generation=?`, c.ID, c.Generation)
			}
			return err
		}
		err = s.write(ctx, owner, c, func(tx *sql.Tx) error {
			r, err := tx.ExecContext(ctx, `UPDATE task_sync_outbox SET state='sent',error_code='',owner_id='',lease_until=0 WHERE event_id=? AND owner_id=?`, event, claim)
			if err == nil {
				err = requireOne(r)
			}
			if err != nil {
				return err
			}
			switch value := result.(type) {
			case tsync.Delivery:
				_, err = tx.ExecContext(ctx, `UPDATE work_deliveries SET state='synced' WHERE delivery_id=? AND revision=?`, value.DeliveryID, value.Revision)
			case tsync.DeliveryDecisionRecord:
				state := "awaiting_acceptance"
				if value.Completed {
					state = "succeeded"
				} else if value.Decision == "return" {
					state = "paused"
				}
				// A confirmed remote decision remains a sent fact, but a local stop
				// committed during the request must retain authority over this run.
				_, err = tx.ExecContext(ctx, `UPDATE task_flow_runs SET state=?,error_code='',updated_at=? WHERE request_id=? AND state NOT IN ('paused','succeeded','failed','cancelled','superseded')`, state, s.now().UnixMilli(), request)
			case tsync.ExecutionRecord:
				if kind == "release" {
					state := string(value.Status)
					_, err = tx.ExecContext(ctx, `UPDATE task_flow_runs SET state=?,error_code='' WHERE request_id=? AND state='paused'`, state, request)
				} else if kind == "late_evidence" {
					_, err = tx.ExecContext(ctx, `UPDATE work_deliveries SET state='archived' WHERE request_id=? AND state='pending' AND delivery_id IN (SELECT json_extract(body_json,'$.deliveryId') FROM task_sync_event_history WHERE replaced_by=? AND kind='delivery')`, request, event)
				}
			}
			return err
		})
		if err != nil {
			return err
		}
	}
}

// Decoding into the canonical DTOs both restricts the allowed routes and keeps
// credentials out of untrusted paths. A byte comparison prevents serialization
// changes from silently changing a previously persisted idempotent request.
func sendStoredEvent(ctx context.Context, c *tsync.Client, kind, raw string) (any, error) {
	check := func(v any) error {
		b, e := tsync.JSONBytes(v)
		if e == nil && string(b) != raw {
			return taskproviders.ErrConflict
		}
		return e
	}
	switch kind {
	case "late_evidence", "receipt_evidence":
		var v tsync.ExecutionEvent
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		if err := check(v); err != nil {
			return nil, err
		}
		r, err := c.ReportExecution(ctx, v)
		if err == nil && (r.ExecutionID != v.ExecutionID || r.RequestID != v.RequestID) {
			err = taskproviders.ErrConflict
		}
		return r, err
	case "release":
		var v releaseEvent
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		if err := check(v); err != nil {
			return nil, err
		}
		r, err := c.ReleaseExecution(ctx, v.ExecutionID, v.Fence, v.Reason, v.EventID)
		if err == nil && (r.RequestID != v.RequestID || r.ExecutionID != v.ExecutionID || r.Status != taskproviders.Paused) {
			err = taskproviders.ErrConflict
		}
		return r, err
	case "delivery":
		var v tsync.Delivery
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		if err := check(v); err != nil {
			return nil, err
		}
		r, err := c.ReportDelivery(ctx, v)
		if err == nil && digest(r) != digest(v) {
			err = taskproviders.ErrConflict
		}
		return r, err
	case "final_action":
		var v tsync.FinalAction
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		if err := check(v); err != nil {
			return nil, err
		}
		r, err := c.ReportFinalAction(ctx, v)
		if err == nil && digest(r) != digest(v) {
			err = taskproviders.ErrConflict
		}
		return r, err
	case "decision":
		// The delivery ID is bound by the proof; it is not a free-form URL.
		var v tsync.DeliveryDecision
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		if err := check(v); err != nil {
			return nil, err
		}
		if v.ConsentProof == nil {
			return nil, taskproviders.ErrConflict
		}
		r, err := c.DecideDelivery(ctx, v.ConsentProof.Payload.DeliveryID, v)
		if err == nil && (r.DecisionID != v.DecisionID || r.DeliveryID != v.ConsentProof.Payload.DeliveryID || r.Revision != v.Revision || r.Decision != v.Decision || r.AccountID != v.ConsentProof.Payload.AccountID || r.InputRevision != v.ConsentProof.Payload.InputRevision || r.AssignmentRevision != v.ConsentProof.Payload.AssignmentRevision || r.Generation != v.ConsentProof.Payload.Generation || r.ScopeHash != v.ConsentProof.Payload.ScopeHash || digest(r.ConsentProof) != digest(v.ConsentProof) || digest(r.Reason) != digest(v.Reason)) {
			err = taskproviders.ErrConflict
		}
		return r, err
	default:
		return nil, taskproviders.ErrUnsupported
	}
}

// A renewed local approval only replaces an expired prepared proof after an
// explicit server rejection. Ambiguous or successful responses retry original
// bytes; dispatching/unknown/success facts are never rewritten this way.
func (s *Service) renewPreparedEvent(ctx context.Context, owner string, f flowRun, event, claim, raw string) (bool, error) {
	var v tsync.FinalAction
	if json.Unmarshal([]byte(raw), &v) != nil || v.Status != "prepared" {
		return false, nil
	}
	oldExpiry, err := time.Parse(time.RFC3339Nano, v.ConsentProof.Payload.ExpiresAt)
	if err != nil || oldExpiry.After(s.now()) {
		return false, nil
	}
	a, err := s.action(ctx, v.ActionID)
	if err != nil {
		return false, err
	}
	newExpiry, err := time.Parse(time.RFC3339Nano, a.Proof.Payload.ExpiresAt)
	if err != nil || !newExpiry.After(s.now()) || a.State != "approved" || a.Fingerprint != v.Fingerprint || a.Proof.Payload.Nonce == v.ConsentProof.Payload.Nonce {
		return false, nil
	}
	next, err := util.UUIDv7()
	if err != nil {
		return false, err
	}
	v.EventID, v.ConsentProof = next, a.Proof
	b, err := tsync.JSONBytes(v)
	if err != nil {
		return false, err
	}
	c, err := s.Get(ctx, f.ConnectionID)
	if err != nil {
		return false, err
	}
	err = s.write(ctx, owner, c, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO task_sync_event_history(event_id,request_id,kind,body_json,body_hash,replaced_by,created_at) VALUES(?,?,?,?,?,?,?)`, event, f.ID, "final_action", raw, finalaction.Hash([]byte(raw)), next, s.now().UnixMilli())
		if err != nil {
			return err
		}
		r, err := tx.ExecContext(ctx, `UPDATE task_sync_outbox SET event_id=?,body_json=?,body_hash=?,state='pending',owner_id='',lease_until=0,next_at=0,error_code='' WHERE event_id=? AND owner_id=?`, next, string(b), finalaction.Hash(b), event, claim)
		if err == nil {
			err = requireOne(r)
		}
		return err
	})
	return err == nil, err
}

// rebaseEvent runs only after the server explicitly confirms an event was not
// accepted (lease_lost is checked after exact replay on the server). It changes
// transport identity/fence only, retaining the full original event for audit.
func (s *Service) rebaseEvent(ctx context.Context, owner string, f flowRun, event, claim, kind, raw string) (bool, error) {
	current, err := s.flow(ctx, f.ID)
	if err != nil {
		return false, err
	}
	var leaseOwner string
	var deadline int64
	if err = s.db.QueryRowContext(ctx, `SELECT lease_owner,deadline FROM task_flow_runs WHERE request_id=?`, f.ID).Scan(&leaseOwner, &deadline); err != nil {
		return false, err
	}
	if leaseOwner != owner || deadline <= s.now().UnixMilli() {
		return false, nil
	}
	next, err := util.UUIDv7()
	if err != nil {
		return false, err
	}
	var body any
	switch kind {
	case "delivery":
		var v tsync.Delivery
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return false, err
		}
		if v.Fence == current.Record.Fence {
			return false, nil
		}
		v.EventID, v.Fence = next, current.Record.Fence
		body = v
	case "final_action":
		var v tsync.FinalAction
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return false, err
		}
		if v.Fence == current.Record.Fence {
			return false, nil
		}
		v.EventID, v.Fence = next, current.Record.Fence
		body = v
	default:
		return false, nil
	}
	b, err := tsync.JSONBytes(body)
	if err != nil {
		return false, err
	}
	c, err := s.Get(ctx, f.ConnectionID)
	if err != nil {
		return false, err
	}
	err = s.write(ctx, owner, c, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO task_sync_event_history(event_id,request_id,kind,body_json,body_hash,replaced_by,created_at) VALUES(?,?,?,?,?,?,?)`, event, f.ID, kind, raw, finalaction.Hash([]byte(raw)), next, s.now().UnixMilli())
		if err != nil {
			return err
		}
		r, err := tx.ExecContext(ctx, `UPDATE task_sync_outbox SET event_id=?,body_json=?,body_hash=?,state='pending',owner_id='',lease_until=0,next_at=0,error_code='' WHERE event_id=? AND owner_id=? AND EXISTS(SELECT 1 FROM task_flow_runs WHERE request_id=? AND lease_owner=? AND deadline>?)`, next, string(b), finalaction.Hash(b), event, claim, f.ID, owner, s.now().UnixMilli())
		if err == nil {
			err = requireOne(r)
		}
		return err
	})
	return err == nil, err
}

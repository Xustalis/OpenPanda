// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Xustalis/OpenPanda/internal/taskflow/finalaction"
	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/taskproviders/tsync"
	"github.com/Xustalis/OpenPanda/internal/util"
)

func stopped(state taskproviders.ExecutionState) bool {
	return state == taskproviders.Paused || state == taskproviders.Succeeded || state == taskproviders.Cancelled || state == taskproviders.Superseded || state == taskproviders.Failed
}

// recoverPauseEvent runs only after a definitive lease_lost rejection. Its
// claim exists solely to release the logical run. It never grants local work
// authority, changes a deadline, invokes the core, or dispatches an action.
func (s *Service) recoverPauseEvent(ctx context.Context, owner string, f flowRun, c Connection, event, claim, raw string) (bool, any, error) {
	current, err := s.flow(ctx, f.ID)
	if err != nil {
		return false, nil, err
	}
	if current.State != "paused" {
		return false, nil, taskproviders.ErrConflict
	}
	client, err := s.executionClient(c)
	if err != nil {
		return false, nil, err
	}
	r, err := client.Execution(ctx, f.ExecutionID)
	if err != nil {
		return false, nil, err
	}
	if !sameExecution(f, r) {
		return false, nil, taskproviders.ErrAuthorizationChanged
	}
	if stopped(r.Status) && r.LeaseUntil == nil {
		return false, r, nil
	}
	var command string
	// A lost claim response is retried with its original key and exact body.
	err = s.write(ctx, owner, c, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT command_id FROM task_flow_commands WHERE request_id=? AND kind='pause_claim' AND state='pending' ORDER BY created_at LIMIT 1`, f.ID).Scan(&command)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		command, err = util.UUIDv7()
		if err != nil {
			return err
		}
		body, err := tsync.JSONBytes(f.Claim)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO task_flow_commands(command_id,request_id,kind,body_json,created_at,updated_at) VALUES(?,?,'pause_claim',?,?,?)`, command, f.ID, string(body), s.now().UnixMilli(), s.now().UnixMilli())
		return err
	})
	if err != nil {
		return false, nil, err
	}
	r, err = client.ClaimExecutionWithKey(ctx, f.Snapshot.Key, f.Claim, command)
	if err != nil {
		return false, nil, err
	}
	if !sameExecution(f, r) {
		return false, nil, taskproviders.ErrAuthorizationChanged
	}
	if stopped(r.Status) && r.LeaseUntil == nil {
		return false, r, nil
	}
	next, err := util.UUIDv7()
	if err != nil {
		return false, nil, err
	}
	v := releaseEvent{EventID: next, ExecutionID: f.ExecutionID, RequestID: f.ID, Fence: r.Fence, Reason: "paused"}
	b, err := tsync.JSONBytes(v)
	if err != nil {
		return false, nil, err
	}
	err = s.write(ctx, owner, c, func(tx *sql.Tx) error {
		if err := s.replaceEvent(ctx, tx, f.ID, event, claim, "release", raw, next, "release", b); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE task_flow_commands SET state='sent',updated_at=? WHERE command_id=?`, s.now().UnixMilli(), command)
		return err
	})
	return err == nil, nil, err
}

// replaceEvent preserves every exact request before changing transport identity.
// Callers must have received a definitive rejection, or be archiving a stopped
// run's remaining reports without trying to advance the remote execution.
func (s *Service) replaceEvent(ctx context.Context, tx *sql.Tx, request, event, claim, kind, raw, next, nextKind string, body []byte) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO task_sync_event_history(event_id,request_id,kind,body_json,body_hash,replaced_by,created_at) VALUES(?,?,?,?,?,?,?)`, event, request, kind, raw, finalaction.Hash([]byte(raw)), next, s.now().UnixMilli())
	if err != nil {
		return err
	}
	r, err := tx.ExecContext(ctx, `UPDATE task_sync_outbox SET event_id=?,kind=?,body_json=?,body_hash=?,state='pending',owner_id='',lease_until=0,next_at=0,error_code='' WHERE event_id=? AND owner_id=?`, next, nextKind, string(body), finalaction.Hash(body), event, claim)
	if err == nil {
		err = requireOne(r)
	}
	return err
}

// Once pause is confirmed, release removed the cloud lease. Remaining action,
// delivery and decision reports become bounded hash-only evidence under their
// original issued fence. The server archives this late evidence without state
// advancement. Private content and consent signatures stay in the local audit.
func (s *Service) archiveStoppedEvents(ctx context.Context, owner string, f flowRun, c Connection) error {
	return s.write(ctx, owner, c, func(tx *sql.Tx) error {
		var state string
		var pending, released int
		if err := tx.QueryRowContext(ctx, `SELECT state FROM task_flow_runs WHERE request_id=?`, f.ID).Scan(&state); err != nil {
			return err
		}
		if !stopped(taskproviders.ExecutionState(state)) {
			return nil
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_sync_outbox WHERE request_id=? AND kind='release' AND state!='sent'`, f.ID).Scan(&pending); err != nil {
			return err
		}
		if pending > 0 {
			return nil
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_sync_outbox WHERE request_id=? AND kind='release' AND state='sent'`, f.ID).Scan(&released); err != nil {
			return err
		}
		// A local policy change can pause work without confirming the cloud
		// lease was removed. A current fence would advance the remote state.
		if released == 0 {
			return nil
		}
		rows, err := tx.QueryContext(ctx, `SELECT event_id,kind,body_json,body_hash,owner_id FROM task_sync_outbox WHERE request_id=? AND state!='sent' AND kind IN ('delivery','final_action','decision')`, f.ID)
		if err != nil {
			return err
		}
		type pendingEvent struct{ id, kind, raw, hash, claim string }
		var events []pendingEvent
		for rows.Next() {
			var v pendingEvent
			if err = rows.Scan(&v.id, &v.kind, &v.raw, &v.hash, &v.claim); err != nil {
				break
			}
			events = append(events, v)
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		if err != nil {
			return err
		}
		for _, old := range events {
			if finalaction.Hash([]byte(old.raw)) != old.hash {
				return taskproviders.ErrConflict
			}
			fence := f.Record.Fence
			if old.kind != "decision" {
				var v struct {
					Fence tsync.Fence `json:"fence"`
				}
				if err = json.Unmarshal([]byte(old.raw), &v); err != nil {
					return err
				}
				fence = v.Fence
			}
			next, err := util.UUIDv7()
			if err != nil {
				return err
			}
			v := tsync.ExecutionEvent{EventID: next, RequestID: f.ID, ExecutionID: f.ExecutionID, Fence: fence, CausedBy: &old.id, Status: taskproviders.Paused, Summary: fmt.Sprintf("Stopped locally; retained %s event %s sha256 %s. No further action authorized.", old.kind, old.id, old.hash), PandaRefs: f.Record.PandaRefs}
			b, err := tsync.JSONBytes(v)
			if err != nil {
				return err
			}
			if err = s.replaceEvent(ctx, tx, f.ID, old.id, old.claim, old.kind, old.raw, next, "late_evidence", b); err != nil {
				return err
			}
		}
		rows, err = tx.QueryContext(ctx, `SELECT r.action_id,r.receipt_json FROM action_receipts r JOIN final_actions a ON a.action_id=r.action_id WHERE a.request_id=? AND r.verified=1 AND NOT EXISTS(SELECT 1 FROM task_sync_outbox o WHERE o.request_id=? AND o.kind='receipt_evidence' AND o.revision=a.action_id)`, f.ID, f.ID)
		if err != nil {
			return err
		}
		type fact struct{ action, receipt string }
		var facts []fact
		for rows.Next() {
			var v fact
			if err = rows.Scan(&v.action, &v.receipt); err != nil {
				break
			}
			facts = append(facts, v)
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		if err != nil {
			return err
		}
		for _, fact := range facts {
			id, err := util.UUIDv7()
			if err != nil {
				return err
			}
			v := tsync.ExecutionEvent{EventID: id, RequestID: f.ID, ExecutionID: f.ExecutionID, Fence: f.Record.Fence, Status: taskproviders.Paused, Summary: fmt.Sprintf("Late verified local receipt for action %s sha256 %s. No further action authorized.", fact.action, finalaction.Hash([]byte(fact.receipt))), PandaRefs: f.Record.PandaRefs}
			if err = s.enqueueOutbox(ctx, tx, f, "receipt_evidence", id, v); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE task_sync_outbox SET revision=? WHERE event_id=?`, fact.action, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) requireActiveRun(ctx context.Context, tx *sql.Tx, request, owner string) error {
	r, err := tx.ExecContext(ctx, `UPDATE task_flow_runs SET updated_at=updated_at WHERE request_id=? AND lease_owner=? AND deadline>? AND state IN ('leased','running','recovering','needs_reconciliation')`, request, owner, s.now().UnixMilli())
	if err == nil {
		err = requireOne(r)
	}
	return err
}

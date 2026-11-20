// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const AskRoundLease = time.Minute

type askRoundKey struct{}

// WithAskRound scopes every task creation in an entry ask, including tool
// batches and plan stages. The database verifies the round is still open.
// It is local transcript metadata, not an authorization or routing grant.
func WithAskRound(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, askRoundKey{}, id)
}

type AskRound struct {
	ID        string
	SessionID string
	Locale    string
	Status    string // open | closed | stopped | interrupted
	CreatedAt int64
	Tasks     []Task
	// PendingRecovery contains only failed tasks whose durable recovery decision
	// is still outstanding. Retryability alone does not mean work is running.
	PendingRecovery map[string]bool
}

func (s *TaskStore) BeginAskRound(ctx context.Context, id, sessionID, locale string) error {
	if id == "" || sessionID == "" {
		return fmt.Errorf("round and session identities required")
	}
	now := s.now()
	result, err := s.db.ExecContext(ctx, `INSERT INTO ask_rounds(round_id,session_id,locale,lease_until,created_at)
 SELECT ?,?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM ask_rounds a WHERE a.session_id=? AND a.status='open' AND a.lease_until>=?
 AND (EXISTS(SELECT 1 FROM conversation_goal_rounds g WHERE g.round_id=a.round_id) OR EXISTS(SELECT 1 FROM conversation_plan_runs p WHERE p.round_id=a.round_id AND p.status='running')))`, id, sessionID, locale, now+int64(AskRoundLease/time.Second), now, sessionID, now)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: session has an active goal or implementation round", ErrConflict)
	}
	return nil
}

func (s *TaskStore) TouchAskRound(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE ask_rounds SET lease_until=? WHERE round_id=? AND status='open'`, s.now()+int64(AskRoundLease/time.Second), id)
	return err
}

// A retry can race the controller's cancellation walk. Check the entire
// ancestry in the same transaction so revoked work cannot mint a new attempt.
func checkConversationTaskTx(ctx context.Context, tx *sql.Tx, taskID string) error {
	var revoked bool
	err := tx.QueryRowContext(ctx, `WITH RECURSIVE ancestors(id,parent_id) AS (
 SELECT task_id,parent_id FROM tasks WHERE task_id=?
 UNION SELECT t.task_id,t.parent_id FROM tasks t JOIN ancestors a ON t.task_id=a.parent_id
) SELECT EXISTS(SELECT 1 FROM ancestors a JOIN ask_round_tasks t ON t.task_id=a.id JOIN ask_rounds r ON r.round_id=t.round_id
 WHERE EXISTS(SELECT 1 FROM conversation_controls c WHERE c.session_id=r.session_id AND c.deleted=1)
 OR EXISTS(SELECT 1 FROM conversation_goal_rounds gr JOIN conversation_goals g ON g.id=gr.goal_id WHERE gr.round_id=r.round_id AND (g.generation!=gr.generation OR g.status='cleared' OR g.completion_requested=1))
 OR EXISTS(SELECT 1 FROM conversation_plan_runs p WHERE p.round_id=r.round_id AND p.status='cancelled'))`, taskID).Scan(&revoked)
	if err != nil {
		return err
	}
	if revoked {
		return fmt.Errorf("%w: conversation execution revoked", ErrConflict)
	}
	return nil
}

// CloseAskRound seals dispatch, not execution. Stopping an observation does
// not cancel already-queued tasks. Those tasks still have to settle before
// the finalizer can give the round its outcome.
func (s *TaskStore) CloseAskRound(ctx context.Context, id, status string) error {
	if status != "closed" && status != "stopped" && status != "interrupted" {
		return fmt.Errorf("invalid round closure")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE ask_rounds SET status=?, closed_at=? WHERE round_id=? AND status='open'`, status, s.now(), id)
	return err
}

// AskRoundProgress reads one operation's task tree within its owning session.
// Tasks may spawn children after the original ask has finished dispatching.
func (s *TaskStore) AskRoundProgress(ctx context.Context, sessionID, roundID string) (AskRound, error) {
	r := AskRound{ID: roundID, SessionID: sessionID}
	if err := s.db.QueryRowContext(ctx, "SELECT status FROM ask_rounds WHERE session_id=? AND round_id=?", sessionID, roundID).Scan(&r.Status); err != nil {
		return r, err
	}
	rows, err := s.db.QueryContext(ctx, `WITH RECURSIVE members(id) AS (
  SELECT task_id FROM ask_round_tasks WHERE round_id=?
  UNION
  SELECT t.task_id FROM tasks t JOIN members m ON t.parent_id=m.id
)
SELECT task_id,state FROM tasks JOIN members ON task_id=members.id ORDER BY task_id`, roundID)
	if err != nil {
		return r, err
	}
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.TaskID, &t.State); err != nil {
			rows.Close()
			return r, err
		}
		r.Tasks = append(r.Tasks, t)
	}
	err = rows.Err()
	rows.Close()
	if err == nil {
		err = s.askRoundRecovery(ctx, &r)
	}
	return r, err
}

// Read only recovery-relevant events, in one query for all failed members.
// This shares RecoveryPending with the plan controller without loading logs.
func (s *TaskStore) askRoundRecovery(ctx context.Context, r *AskRound) error {
	var ids []any
	var marks []string
	for _, t := range r.Tasks {
		if t.State == StateFailed {
			ids = append(ids, t.TaskID)
			marks = append(marks, "?")
		}
	}
	if len(ids) == 0 {
		return nil
	}
	args := append(ids, EvSubmit, EvRetry, EvResult, EvRecoveryAssessment, EvReview, EvExpire, EvCancel, EvExecAgentStart, EvSupervisionRound)
	rows, err := s.db.QueryContext(ctx, `SELECT e.task_id,e.type,e.data_json FROM task_events e
		WHERE e.task_id IN (`+strings.Join(marks, ",")+`)
		AND e.id > COALESCE((SELECT MAX(b.id) FROM task_events b WHERE b.task_id=e.task_id AND b.type IN (?,?)),0)
		AND e.type IN (?,?,?,?,?,?,?) ORDER BY e.id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	events := make(map[string][]Event)
	for rows.Next() {
		var id string
		var ev Event
		if err := rows.Scan(&id, &ev.Type, &ev.DataJSON); err != nil {
			return err
		}
		events[id] = append(events[id], ev)
	}
	r.PendingRecovery = make(map[string]bool)
	for id, evs := range events {
		r.PendingRecovery[id] = RecoveryPending(evs)
	}
	return rows.Err()
}

func (s *TaskStore) attachAskRoundTx(ctx context.Context, tx *sql.Tx, taskID string) (string, error) {
	id, _ := ctx.Value(askRoundKey{}).(string)
	if id == "" {
		return "", nil
	}
	var revoked bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_goal_rounds r JOIN conversation_goals g ON g.id=r.goal_id WHERE r.round_id=? AND (g.generation!=r.generation OR g.status='cleared' OR g.completion_requested=1)) OR EXISTS(SELECT 1 FROM conversation_controls c JOIN ask_rounds r ON r.session_id=c.session_id WHERE r.round_id=? AND c.deleted=1) OR EXISTS(SELECT 1 FROM conversation_plan_runs p WHERE p.round_id=? AND p.status!='running')`, id, id, id).Scan(&revoked); err != nil {
		return "", err
	}
	if revoked {
		return "", fmt.Errorf("goal round execution generation revoked")
	}
	var sessionID string
	if err := tx.QueryRowContext(ctx, `SELECT session_id FROM ask_rounds WHERE round_id=? AND status='open'`, id).Scan(&sessionID); err != nil {
		return "", fmt.Errorf("attach task to open ask round: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ask_round_tasks(task_id,round_id) VALUES(?,?)`, taskID, id); err != nil {
		return "", err
	}
	_, err := tx.ExecContext(ctx, `UPDATE tasks SET session_id=? WHERE task_id=?`, sessionID, taskID)
	return sessionID, err
}

// SettledAskRounds also reclaims abandoned dispatches by lease. It never
// declares a still-running task cancelled and never merges unrelated asks
// solely because they share a session.
//
// A review-parked task is not settled: a human still owes it a decision, so
// the round has no outcome to report yet. Reporting it here is what made the
// transcript announce "1 need review" as a result, and then announce the same
// round again once the review was resolved. The task card and the queue
// already say a task is waiting; the round speaks when it is over.
func (s *TaskStore) SettledAskRounds(ctx context.Context) ([]AskRound, error) {
	if _, err := s.db.ExecContext(ctx, `UPDATE ask_rounds SET status='interrupted', closed_at=? WHERE status='open' AND lease_until<?`, s.now(), s.now()); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT round_id,session_id,locale,status,created_at FROM ask_rounds r
  WHERE status!='open' AND reported_version != (`+askRoundVersionSQL+`) AND EXISTS(SELECT 1 FROM ask_round_tasks m WHERE m.round_id=r.round_id)
  AND NOT EXISTS(SELECT 1 FROM ask_round_tasks m JOIN tasks t ON t.task_id=m.task_id
   WHERE m.round_id=r.round_id AND t.state NOT IN (?,?,?,?)) ORDER BY r.created_at,r.round_id`, StateDone, StateFailed, StateCancelled, StateExpired)
	if err != nil {
		return nil, err
	}
	var out []AskRound
	for rows.Next() {
		var r AskRound
		if err := rows.Scan(&r.ID, &r.SessionID, &r.Locale, &r.Status, &r.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	// Release the query before fetching payload rows: stores can have a
	// single connection, and nested queries would deadlock that configuration.
	settled := out[:0]
	for i := range out {
		progress, err := s.AskRoundProgress(ctx, out[i].SessionID, out[i].ID)
		if err != nil {
			return nil, err
		}
		pending := false
		for _, t := range progress.Tasks {
			if (t.State != StateDone && t.State != StateFailed && t.State != StateCancelled && t.State != StateExpired) || progress.PendingRecovery[t.TaskID] {
				pending = true
				break
			}
		}
		if pending {
			continue
		}
		rs, err := s.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE task_id IN (SELECT task_id FROM ask_round_tasks WHERE round_id=?) ORDER BY task_id`, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Tasks, err = s.scanTasks(rs)
		rs.Close()
		if err != nil {
			return nil, err
		}
		settled = append(settled, out[i])
	}
	return settled, nil
}

// Version changes on retry and on review resolution even within one second.
// It is not derived from updated_at: event writes share that clock too.
func (r AskRound) Version() string {
	parts := make([]string, 0, len(r.Tasks))
	for _, t := range r.Tasks {
		parts = append(parts, t.TaskID+":"+t.AttemptID+":"+t.State+":"+strconv.Itoa(t.StateVersion))
	}
	return strings.Join(parts, ",")
}

const askRoundVersionSQL = `SELECT group_concat(v, ',') FROM (
 SELECT t.task_id || ':' || t.attempt_id || ':' || t.state || ':' || t.state_version AS v
 FROM ask_round_tasks m JOIN tasks t ON t.task_id=m.task_id WHERE m.round_id=r.round_id ORDER BY t.task_id)`

func (s *TaskStore) MarkAskRoundReported(ctx context.Context, id, version string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE ask_rounds SET reported_version=? WHERE round_id=? AND status!='open'`, version, id)
	return err
}

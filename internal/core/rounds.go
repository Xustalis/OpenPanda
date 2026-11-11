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
}

func (s *TaskStore) BeginAskRound(ctx context.Context, id, sessionID, locale string) error {
	if id == "" || sessionID == "" {
		return fmt.Errorf("round and session identities required")
	}
	now := s.now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO ask_rounds(round_id,session_id,locale,lease_until,created_at) VALUES(?,?,?,?,?)`, id, sessionID, locale, now+int64(AskRoundLease/time.Second), now)
	return err
}

func (s *TaskStore) TouchAskRound(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE ask_rounds SET lease_until=? WHERE round_id=? AND status='open'`, s.now()+int64(AskRoundLease/time.Second), id)
	return err
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

func (s *TaskStore) attachAskRoundTx(ctx context.Context, tx *sql.Tx, taskID string) (string, error) {
	id, _ := ctx.Value(askRoundKey{}).(string)
	if id == "" {
		return "", nil
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
	for i := range out {
		rs, err := s.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE task_id IN (SELECT task_id FROM ask_round_tasks WHERE round_id=?) ORDER BY task_id`, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Tasks, err = s.scanTasks(rs)
		rs.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
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

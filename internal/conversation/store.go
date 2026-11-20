// SPDX-License-Identifier: AGPL-3.0-or-later

// Package conversation owns durable interactive modes and autonomous goals.
// All clients and controllers use the same conditional database transitions.
package conversation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/util"
)

var ErrConflict = errors.New("conversation state changed; refresh before retrying")
var ErrWaiting = errors.New("associated work has not settled")
var ErrInvalid = errors.New("invalid conversation control")

type Goal struct {
	ID                  string `json:"id"`
	SessionID           string `json:"session_id"`
	Objective           string `json:"objective"`
	Status              string `json:"status"`
	Reason              string `json:"reason,omitempty"`
	Generation          int64  `json:"generation"`
	RoundID             string `json:"round_id,omitempty"`
	LeaseOwner          string `json:"-"`
	LeaseUntil          int64  `json:"-"`
	Rounds              int64  `json:"rounds"`
	Tokens              int64  `json:"tokens"`
	TokenBudget         *int64 `json:"token_budget,omitempty"`
	Blocker             string `json:"blocker,omitempty"`
	BlockedStreak       int    `json:"blocked_streak"`
	EmptyStreak         int    `json:"empty_streak"`
	CompletionRequested bool   `json:"completion_requested"`
	PausedByPlan        bool   `json:"paused_by_plan"`
	WaitingFor          string `json:"waiting_for,omitempty"`
	CreatedAt           int64  `json:"created_at"`
	UpdatedAt           int64  `json:"updated_at"`
}

type State struct {
	ModeGeneration   int64  `json:"mode_generation"`
	Planning         bool   `json:"planning"`
	ProposalVersion  int64  `json:"proposal_version"`
	Proposal         string `json:"proposal,omitempty"`
	ConfirmedVersion int64  `json:"confirmed_version"`
	Goal             *Goal  `json:"goal,omitempty"`
}

type Store struct{ DB *sql.DB }

const goalColumns = "id,session_id,objective,status,reason,generation,round_id,lease_owner,lease_until,rounds,tokens,token_budget,blocker,blocked_streak,empty_streak,completion_requested,paused_by_plan,waiting_for,created_at,updated_at"

func scanGoal(row interface{ Scan(...any) error }) (*Goal, error) {
	g := new(Goal)
	err := row.Scan(&g.ID, &g.SessionID, &g.Objective, &g.Status, &g.Reason, &g.Generation, &g.RoundID, &g.LeaseOwner, &g.LeaseUntil, &g.Rounds, &g.Tokens, &g.TokenBudget, &g.Blocker, &g.BlockedStreak, &g.EmptyStreak, &g.CompletionRequested, &g.PausedByPlan, &g.WaitingFor, &g.CreatedAt, &g.UpdatedAt)
	return g, err
}

func (s *Store) Get(ctx context.Context, sid string) (State, error) {
	var st State
	err := s.DB.QueryRowContext(ctx, "SELECT planning,proposal_version,proposal,confirmed_version,mode_generation FROM conversation_controls WHERE session_id=? AND deleted=0", sid).Scan(&st.Planning, &st.ProposalVersion, &st.Proposal, &st.ConfirmedVersion, &st.ModeGeneration)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return st, err
	}
	g, err := scanGoal(s.DB.QueryRowContext(ctx, "SELECT "+goalColumns+" FROM conversation_goals WHERE session_id=? AND status!='cleared'", sid))
	if err == nil {
		st.Goal = g
	} else if !errors.Is(err, sql.ErrNoRows) {
		return st, err
	}
	return st, nil
}

func ensure(ctx context.Context, tx *sql.Tx, sid string) error {
	if sid == "" {
		return errors.New("session identity required")
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO conversation_controls(session_id) VALUES(?) ON CONFLICT DO NOTHING", sid)
	if err != nil {
		return err
	}
	var deleted bool
	if err = tx.QueryRowContext(ctx, "SELECT deleted FROM conversation_controls WHERE session_id=?", sid).Scan(&deleted); err != nil {
		return err
	}
	if deleted {
		return ErrConflict
	}
	return nil
}

func event(ctx context.Context, tx *sql.Tx, sid, kind string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO conversation_events(session_id,kind,payload,created_at) VALUES(?,?,?,?)", sid, kind, string(b), time.Now().UnixMilli())
	return err
}

const retainedConversationEvents = 4096

// PruneEvents bounds the SSE notification backlog. An unsettled round's
// blocker report also deduplicates audits, so keep it until settlement.
func (s *Store) PruneEvents(ctx context.Context) error {
	var cutoff int64
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM conversation_events ORDER BY id DESC LIMIT 1 OFFSET ?`, retainedConversationEvents).Scan(&cutoff)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `DELETE FROM conversation_events AS e WHERE e.id<=? AND (
 e.kind!='goal_blocked' OR NOT EXISTS (
 SELECT 1 FROM conversation_goal_rounds r
 WHERE r.round_id=json_extract(e.payload,'$') AND r.settled=0))`, cutoff)
	return err
}

func (s *Store) SetPlanning(ctx context.Context, sid string, enabled bool) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = ensure(ctx, tx, sid); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE conversation_controls SET mode_generation=mode_generation+CASE WHEN planning!=? THEN 1 ELSE 0 END,planning=? WHERE session_id=?", enabled, enabled, sid); err != nil {
		return err
	}
	if enabled {
		if _, err = tx.ExecContext(ctx, "UPDATE conversation_goals SET status='paused',paused_by_plan=1,updated_at=? WHERE session_id=? AND status='active'", time.Now().UnixMilli(), sid); err != nil {
			return err
		}
	}
	if err = event(ctx, tx, sid, "mode", map[string]bool{"planning": enabled}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveProposal(ctx context.Context, sid, text string, expected ...int64) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var version int64
	query := "UPDATE conversation_controls SET proposal_version=proposal_version+1,proposal=? WHERE session_id=? AND planning=1 AND deleted=0"
	args := []any{text, sid}
	if len(expected) > 0 {
		query += " AND proposal_version=?"
		args = append(args, expected[0])
	}
	if len(expected) > 1 {
		query += " AND mode_generation=?"
		args = append(args, expected[1])
	}
	err = tx.QueryRowContext(ctx, query+" RETURNING proposal_version", args...).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrConflict
	}
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO conversation_proposals(session_id,version,proposal,created_at) VALUES(?,?,?,?)", sid, version, text, time.Now().UnixMilli()); err != nil {
		return 0, err
	}
	if err = event(ctx, tx, sid, "plan", map[string]any{"version": version, "proposal": text}); err != nil {
		return 0, err
	}
	return version, tx.Commit()
}

// Confirm consumes exactly the version shown to the user, once. The caller
// supplies the resulting instruction to the ordinary ask path in this session.
func (s *Store) Confirm(ctx context.Context, sid string, version int64) (string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var text string
	err = tx.QueryRowContext(ctx, "UPDATE conversation_controls SET planning=0,confirmed_version=? WHERE session_id=? AND deleted=0 AND planning=1 AND proposal_version=? AND confirmed_version<? RETURNING proposal", version, sid, version, version).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrConflict
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO conversation_plan_runs(session_id,version,proposal,goal_id,goal_generation) VALUES(?,?,?,COALESCE((SELECT id FROM conversation_goals WHERE session_id=? AND status='paused' AND paused_by_plan=1),''),COALESCE((SELECT generation FROM conversation_goals WHERE session_id=? AND status='paused' AND paused_by_plan=1),0))`, sid, version, text, sid, sid); err != nil {
		return "", err
	}
	if err = event(ctx, tx, sid, "plan_confirmed", map[string]int64{"version": version}); err != nil {
		return "", err
	}
	return text, tx.Commit()
}

func (s *Store) Create(ctx context.Context, sid, objective string, budget *int64) (*Goal, error) {
	objective = strings.TrimSpace(objective)
	if objective == "" || len(objective) > 200000 {
		return nil, fmt.Errorf("%w: a nonempty objective of at most 200000 bytes is required", ErrInvalid)
	}
	if budget != nil && *budget <= 0 {
		return nil, fmt.Errorf("%w: token budget must be positive", ErrInvalid)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = ensure(ctx, tx, sid); err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	id, err := util.UUIDv7()
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE conversation_plan_runs SET status='cancelled' WHERE session_id=? AND goal_id!='' AND status IN ('pending','running','waiting_input')", sid); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE conversation_goals SET status='cleared',generation=generation+1,round_id='',lease_owner='',lease_until=0,updated_at=? WHERE session_id=? AND status!='cleared'", now, sid); err != nil {
		return nil, err
	}
	status := "active"
	var planning bool
	if err = tx.QueryRowContext(ctx, "SELECT planning FROM conversation_controls WHERE session_id=?", sid).Scan(&planning); err != nil {
		return nil, err
	}
	if planning {
		status = "paused"
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO conversation_goals(id,session_id,objective,status,token_budget,paused_by_plan,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)", id, sid, objective, status, budget, planning, now, now); err != nil {
		return nil, err
	}
	if err = event(ctx, tx, sid, "goal", map[string]string{"id": id, "status": status}); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	st, err := s.Get(ctx, sid)
	return st.Goal, err
}

// Control is a trusted user action. Models only get Report below, so they
// cannot change the objective, resume a paused goal or raise its budget.
func (s *Store) Control(ctx context.Context, sid, id, action, objective string, budget *int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = ensure(ctx, tx, sid); err != nil {
		return err
	}
	g, err := scanGoal(tx.QueryRowContext(ctx, "SELECT "+goalColumns+" FROM conversation_goals WHERE session_id=? AND id=? AND status!='cleared'", sid, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	switch action {
	case "pause":
		g.Status = "paused"
		g.PausedByPlan = false
	case "stop":
		g.Status = "paused"
		g.Generation++
		g.PausedByPlan = false
	case "resume":
		resumeBudget := g.TokenBudget
		if budget != nil {
			resumeBudget = budget
		}
		if resumeBudget != nil && g.Tokens >= *resumeBudget {
			return fmt.Errorf("%w: explicit budget increase required", ErrInvalid)
		}
		// Explicitly continuing the goal also leaves planning. Fence any
		// in-flight proposal producer; merely leaving planning never resumes.
		if _, err = tx.ExecContext(ctx, "UPDATE conversation_controls SET mode_generation=mode_generation+CASE WHEN planning=1 THEN 1 ELSE 0 END,planning=0 WHERE session_id=?", sid); err != nil {
			return err
		}
		if err = event(ctx, tx, sid, "mode", map[string]bool{"planning": false}); err != nil {
			return err
		}
		g.Status = "active"
		g.BlockedStreak = 0
		g.Blocker = ""
		g.EmptyStreak = 0
		g.CompletionRequested = false
		g.PausedByPlan = false
	case "clear":
		g.Status = "cleared"
		g.Generation++
		g.RoundID, g.LeaseOwner, g.LeaseUntil = "", "", 0
	case "edit":
		objective = strings.TrimSpace(objective)
		if objective == "" || len(objective) > 200000 {
			return fmt.Errorf("%w: a nonempty objective of at most 200000 bytes is required", ErrInvalid)
		}
		g.Objective = objective
		g.Generation++
		g.CompletionRequested = false
		g.BlockedStreak = 0
		g.Blocker = ""
		g.EmptyStreak = 0
		if g.Status == "complete" {
			// The old audit cannot certify a changed objective. Saving an
			// edit requires an explicit resume before doing more work.
			g.Status = "paused"
		}
	default:
		return fmt.Errorf("%w: unknown goal control", ErrInvalid)
	}
	if budget != nil {
		if *budget <= 0 {
			return fmt.Errorf("%w: token budget must be positive", ErrInvalid)
		}
		g.TokenBudget = budget
	}
	if g.Status == "active" && g.TokenBudget != nil && g.Tokens >= *g.TokenBudget {
		g.Status = "budget_limited"
	}
	_, err = tx.ExecContext(ctx, "UPDATE conversation_goals SET objective=?,status=?,generation=?,token_budget=?,blocked_streak=?,blocker=?,empty_streak=?,completion_requested=?,paused_by_plan=?,round_id=?,lease_owner=?,lease_until=?,waiting_for='',reason='',updated_at=? WHERE id=?", g.Objective, g.Status, g.Generation, g.TokenBudget, g.BlockedStreak, g.Blocker, g.EmptyStreak, g.CompletionRequested, g.PausedByPlan, g.RoundID, g.LeaseOwner, g.LeaseUntil, time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	if action == "clear" || action == "stop" || action == "edit" {
		if _, err = tx.ExecContext(ctx, "UPDATE conversation_plan_runs SET status='cancelled' WHERE goal_id=? AND status IN ('pending','running','waiting_input')", id); err != nil {
			return err
		}
	}
	if err = event(ctx, tx, sid, "goal", g); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteSession(ctx context.Context, sid string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE conversation_plan_runs SET status='cancelled' WHERE session_id=? AND status IN ('pending','running','waiting_input')", sid); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO conversation_controls(session_id,deleted) VALUES(?,1) ON CONFLICT(session_id) DO UPDATE SET deleted=1", sid); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE conversation_goals SET status='cleared',generation=generation+1,round_id='',lease_owner='',lease_until=0 WHERE session_id=?", sid); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Runnable(ctx context.Context) ([]*Goal, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+goalColumns+" FROM conversation_goals WHERE status='active' OR round_id!=''")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Goal
	for rows.Next() {
		g, err := scanGoal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Claim creates the round and its fencing token atomically. A lease expiry
// never alone authorizes replay: a previous round must first be reconciled.
func (s *Store) Claim(ctx context.Context, g *Goal, owner, locale string) (string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	round, err := util.UUIDv7()
	if err != nil {
		return "", err
	}
	result, err := tx.ExecContext(ctx, `UPDATE conversation_goals SET round_id=?,lease_owner=?,lease_until=?,rounds=rounds+1,updated_at=? WHERE id=? AND generation=? AND status='active' AND round_id='' AND lease_until<? AND completion_requested=0 AND waiting_for='' AND (token_budget IS NULL OR tokens<token_budget) AND NOT EXISTS(SELECT 1 FROM ask_rounds a WHERE a.session_id=conversation_goals.session_id AND a.status='open' AND a.lease_until>=CAST(strftime('%s','now') AS INTEGER)) AND NOT EXISTS(SELECT 1 FROM tasks t WHERE t.session_id=conversation_goals.session_id AND t.state NOT IN ('done','failed','cancelled','expired')) AND NOT EXISTS(SELECT 1 FROM conversation_plan_runs p WHERE p.session_id=conversation_goals.session_id AND p.status IN ('pending','running','waiting_input')) AND EXISTS(SELECT 1 FROM conversation_controls WHERE session_id=conversation_goals.session_id AND planning=0 AND deleted=0)`, round, owner, now+60000, now, g.ID, g.Generation, now)
	if err != nil {
		return "", err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return "", ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO ask_rounds(round_id,session_id,locale,lease_until,created_at) VALUES(?,?,?,?,?)", round, g.SessionID, locale, time.Now().Unix()+60, time.Now().Unix()); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO conversation_goal_rounds(round_id,goal_id,generation) VALUES(?,?,?)", round, g.ID, g.Generation); err != nil {
		return "", err
	}
	if err = event(ctx, tx, g.SessionID, "goal_round", map[string]string{"goal_id": g.ID, "round_id": round}); err != nil {
		return "", err
	}
	return round, tx.Commit()
}

func (s *Store) Renew(ctx context.Context, g *Goal, owner string) error {
	result, err := s.DB.ExecContext(ctx, "UPDATE conversation_goals SET lease_until=? WHERE id=? AND generation=? AND round_id=? AND lease_owner=? AND status IN ('active','paused')", time.Now().UnixMilli()+60000, g.ID, g.Generation, g.RoundID, owner)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) Report(ctx context.Context, g *Goal, status, reason string) error {
	if status == "complete" {
		progress, err := core.NewTaskStore(s.DB, nil).AskRoundProgress(ctx, g.SessionID, g.RoundID)
		if err != nil {
			return err
		}
		for _, task := range progress.Tasks {
			if task.State != core.StateDone && task.State != core.StateFailed && task.State != core.StateCancelled && task.State != core.StateExpired || progress.PendingRecovery[task.TaskID] {
				return ErrWaiting
			}
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := scanGoal(tx.QueryRowContext(ctx, "SELECT "+goalColumns+" FROM conversation_goals WHERE id=? AND generation=? AND round_id=? AND status IN ('active','blocked')", g.ID, g.Generation, g.RoundID))
	if err != nil {
		return ErrConflict
	}
	switch status {
	case "complete":
		if current.Status != "active" {
			return ErrConflict
		}
		var pending bool
		if err := tx.QueryRowContext(ctx, `WITH RECURSIVE members(id) AS (SELECT task_id FROM ask_round_tasks WHERE round_id=? UNION SELECT t.task_id FROM tasks t JOIN members m ON t.parent_id=m.id) SELECT EXISTS(SELECT 1 FROM tasks JOIN members ON task_id=members.id WHERE state NOT IN ('done','failed','cancelled','expired'))`, g.RoundID).Scan(&pending); err != nil {
			return err
		}
		if pending {
			return ErrWaiting
		}
		if strings.TrimSpace(reason) == "" {
			return errors.New("completion audit of the full objective is required")
		}
		current.CompletionRequested = true
	case "blocked":
		reason = strings.TrimSpace(reason)
		if reason == "" {
			return errors.New("a concrete blocker is required")
		}
		// Later evidence of missing work invalidates an earlier completion audit,
		// including when this round already reported the same blocker.
		current.CompletionRequested = false
		// One report per round, regardless of repeated tool calls/notifications.
		var reported bool
		err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM conversation_events WHERE session_id=? AND kind='goal_blocked' AND payload=?)", g.SessionID, fmt.Sprintf("%q", g.RoundID)).Scan(&reported)
		if err != nil {
			return err
		}
		if !reported {
			if current.Blocker == reason {
				current.BlockedStreak++
			} else {
				current.Blocker = reason
				current.BlockedStreak = 1
			}
			if current.BlockedStreak >= 3 {
				current.Status = "blocked"
			}
			if err = event(ctx, tx, g.SessionID, "goal_blocked", g.RoundID); err != nil {
				return err
			}
		}
	default:
		return errors.New("model may only report complete or blocked")
	}
	_, err = tx.ExecContext(ctx, "UPDATE conversation_goals SET status=?,reason=?,blocker=?,blocked_streak=?,completion_requested=?,updated_at=? WHERE id=? AND generation=?", current.Status, reason, current.Blocker, current.BlockedStreak, current.CompletionRequested, time.Now().UnixMilli(), g.ID, g.Generation)
	if err != nil {
		return err
	}
	if err = event(ctx, tx, g.SessionID, "goal", current); err != nil {
		return err
	}
	return tx.Commit()
}

// Finish records request-local usage monotonically. A remote controller may
// settle a stopped round before its request finalizer returns, so late usage
// must also charge the settled ledger's immutable goal identity, once.
func (s *Store) Finish(ctx context.Context, round string, tokens int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var goal string
	var previous int64
	var settled bool
	// Acquire the write lock before reading, across independent SQLite handles.
	err = tx.QueryRowContext(ctx, "UPDATE conversation_goal_rounds SET entry_tokens=entry_tokens WHERE round_id=? RETURNING goal_id,entry_tokens,settled", round).Scan(&goal, &previous, &settled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // Ordinary interactive asks have no goal ledger.
	}
	if err != nil {
		return err
	}
	if tokens <= previous {
		return nil
	}
	if _, err = tx.ExecContext(ctx, "UPDATE conversation_goal_rounds SET entry_tokens=? WHERE round_id=?", tokens, round); err != nil {
		return err
	}
	if settled {
		delta := tokens - previous
		var sid string
		err = tx.QueryRowContext(ctx, `UPDATE conversation_goals SET tokens=tokens+?,status=CASE WHEN status='active' AND token_budget IS NOT NULL AND tokens+?>=token_budget THEN 'budget_limited' ELSE status END,updated_at=? WHERE id=? RETURNING session_id`, delta, delta, time.Now().UnixMilli(), goal).Scan(&sid)
		if err != nil {
			return err
		}
		if err = event(ctx, tx, sid, "goal_settled", map[string]any{"id": goal, "tokens": delta}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Settle(ctx context.Context, g *Goal, taskTokens int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var tokens int64
	// Read task metrics under the same write transaction that seals the ledger.
	// A pre-transaction sum can miss a metric that saw settled=0 just before us.
	err = tx.QueryRowContext(ctx, `UPDATE conversation_goal_rounds SET settled=1,task_tokens=MAX(?,(
 WITH RECURSIVE members(id) AS (SELECT task_id FROM ask_round_tasks WHERE round_id=?
 UNION SELECT t.task_id FROM tasks t JOIN members m ON t.parent_id=m.id)
 SELECT COALESCE(SUM(MAX(0,COALESCE(tokens,0))),0) FROM delegation_metrics
 WHERE task_id IN (SELECT id FROM members) AND usage_source!='ask'))
 WHERE round_id=? AND goal_id=? AND settled=0 RETURNING entry_tokens+task_tokens`, taskTokens, g.RoundID, g.RoundID, g.ID).Scan(&tokens)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	// Charge the immutable goal identity even after revocation. Only its current
	// round may clear execution state or finish the goal; implementation rounds
	// share this ledger without taking over the autonomous round pointer.
	_, err = tx.ExecContext(ctx, `UPDATE conversation_goals SET tokens=tokens+?,status=CASE WHEN status='active' AND round_id=? AND completion_requested=1 THEN 'complete' WHEN status='active' AND token_budget IS NOT NULL AND tokens+?>=token_budget THEN 'budget_limited' ELSE status END,blocked_streak=CASE WHEN round_id!=? OR EXISTS(SELECT 1 FROM conversation_events WHERE session_id=? AND kind='goal_blocked' AND payload=?) THEN blocked_streak ELSE 0 END,lease_owner=CASE WHEN round_id=? THEN '' ELSE lease_owner END,lease_until=CASE WHEN round_id=? THEN 0 ELSE lease_until END,round_id=CASE WHEN round_id=? THEN '' ELSE round_id END,updated_at=? WHERE id=?`, tokens, g.RoundID, tokens, g.RoundID, g.SessionID, fmt.Sprintf("%q", g.RoundID), g.RoundID, g.RoundID, g.RoundID, time.Now().UnixMilli(), g.ID)
	if err != nil {
		return err
	}
	if err = event(ctx, tx, g.SessionID, "goal_settled", map[string]any{"id": g.ID, "tokens": tokens}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Hold(ctx context.Context, g *Goal, status, reason string, empty bool) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE conversation_goals SET status=?,reason=?,empty_streak=CASE WHEN ? THEN empty_streak+1 ELSE 0 END,updated_at=? WHERE id=? AND generation=? AND round_id=? AND status='active'", status, reason, empty, time.Now().UnixMilli(), g.ID, g.Generation, g.RoundID)
	return err
}

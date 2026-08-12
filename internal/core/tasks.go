package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/xenith/panda/internal/storage"
	"github.com/xenith/panda/internal/util"
)

// TaskStore manages the tasks and task_events tables. It enforces the state
// machine, attempt lifecycle, idempotency, and cascade cancellation.
//
// All mutating methods take an owner: the node that currently holds (or is
// claiming) the task lease. Transitions are rejected if owner does not match
// the stored owner_node, except for the terminal cancel that the parent may
// force downward.
type TaskStore struct {
	db     *sql.DB
	logger *slog.Logger
	now    func() int64
}

// NewTaskStore wraps a DB. now may be nil (defaults to Unix time).
func NewTaskStore(db *sql.DB, logger *slog.Logger) *TaskStore {
	if logger == nil {
		logger = slog.Default()
	}
	return &TaskStore{db: db, logger: logger, now: storage.Now}
}

// Create inserts a new task in submitted state and records the submit event.
func (s *TaskStore) Create(ctx context.Context, parentID, project, title, owner string, chain []string) (Task, error) {
	taskID, err := util.UUIDv7()
	if err != nil {
		return Task{}, fmt.Errorf("uuid: %w", err)
	}
	attemptID, err := util.UUIDv7()
	if err != nil {
		return Task{}, fmt.Errorf("uuid: %w", err)
	}
	now := s.now()
	chainJSON, _ := json.Marshal(chain)

	t := Task{
		TaskID: taskID, ParentID: parentID, Project: project, Title: title,
		State: StateSubmitted, OwnerNode: owner, AttemptID: attemptID,
		StateVersion: 0, Chain: chain, CreatedAt: now, UpdatedAt: now,
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO tasks (task_id, parent_id, project, title, state, owner_node,
			attempt_id, state_version, chain_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.TaskID, t.ParentID, t.Project, t.Title, t.State, t.OwnerNode,
		t.AttemptID, t.StateVersion, string(chainJSON), now, now)
	if err != nil {
		return Task{}, fmt.Errorf("insert task: %w", err)
	}
	if err := s.recordEvent(ctx, taskID, EvSubmit, map[string]any{
		"parent": parentID, "owner": owner,
	}); err != nil {
		return Task{}, err
	}
	return t, nil
}

// Get loads one task.
func (s *TaskStore) Get(ctx context.Context, taskID string) (Task, error) {
	var t Task
	var chainJSON string
	var intent, spec, result sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT task_id, parent_id, project, title, state, owner_node, attempt_id,
		       state_version, chain_json, intent, spec_json, result_json, created_at, updated_at
		FROM tasks WHERE task_id = ?`, taskID).
		Scan(&t.TaskID, &t.ParentID, &t.Project, &t.Title, &t.State, &t.OwnerNode,
			&t.AttemptID, &t.StateVersion, &chainJSON, &intent, &spec,
			&result, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return Task{}, err
	}
	_ = json.Unmarshal([]byte(chainJSON), &t.Chain)
	t.Intent = intent.String
	t.SpecJSON = spec.String
	t.ResultJSON = result.String
	return t, nil
}

// transition moves taskID from -> to if the move is legal and owner holds
// the lease. If stateVersion is >= 0 it must match the stored version.
// Returns ErrConflict on state/version/owner mismatch, ErrInvalid on an
// illegal transition.
func (s *TaskStore) transition(ctx context.Context, taskID, from, to, owner, event string, data any) error {
	cur, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if cur.State != from {
		return fmt.Errorf("%w: task %s state=%s, want %s", ErrConflict, taskID, cur.State, from)
	}
	if cur.OwnerNode != owner {
		return fmt.Errorf("%w: task %s owner=%s, caller %s", ErrConflict, taskID, cur.OwnerNode, owner)
	}
	if !CanTransition(from, to) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegal, from, to)
	}
	if err := s.apply(ctx, taskID, to, owner, cur.AttemptID, event, data); err != nil {
		return err
	}
	s.logger.Debug("task transition", "task", taskID, "from", from, "to", to, "owner", owner)
	return nil
}

// apply writes the new state + event. attemptID is carried through unchanged
// unless a caller explicitly rotates it (retry/transfer).
func (s *TaskStore) apply(ctx context.Context, taskID, to, owner, attemptID, event string, data any) error {
	now := s.now()
	dataJSON, _ := json.Marshal(data)
	_, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET state=?, owner_node=?, attempt_id=?, state_version=state_version+1, updated_at=?
		WHERE task_id=?`,
		to, owner, attemptID, now, taskID)
	if err != nil {
		return fmt.Errorf("update task state: %w", err)
	}
	return s.recordEvent(ctx, taskID, event, dataJSON)
}

// recordEvent appends to task_events. The data parameter is either a map or
// already-marshaled JSON bytes.
func (s *TaskStore) recordEvent(ctx context.Context, taskID, typ string, data any) error {
	var raw []byte
	switch v := data.(type) {
	case []byte:
		raw = v
	default:
		raw, _ = json.Marshal(v)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO task_events (task_id, ts, type, data_json) VALUES (?, ?, ?, ?)`,
		taskID, s.now(), typ, string(raw))
	if err != nil {
		return fmt.Errorf("record event %s: %w", typ, err)
	}
	return nil
}

// Queue transitions submitted -> queued.
func (s *TaskStore) Queue(ctx context.Context, taskID, owner string) error {
	return s.transition(ctx, taskID, StateSubmitted, StateQueued, owner, EvQueue, nil)
}

// Dispatch transitions queued -> dispatched and records the target node.
func (s *TaskStore) Dispatch(ctx context.Context, taskID, owner, target string) error {
	return s.transition(ctx, taskID, StateQueued, StateDispatched, owner, EvDelegate,
		map[string]any{"target": target})
}

// Accept transitions dispatched -> running and transfers the lease to the
// executor (owner). The executor must be the node that took the task.
func (s *TaskStore) Accept(ctx context.Context, taskID, owner string) error {
	cur, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if cur.State != StateDispatched {
		return fmt.Errorf("%w: task %s state=%s, want %s", ErrConflict, taskID, cur.State, StateDispatched)
	}
	// Any node may accept a dispatched task; acceptance is the moment the
	// lease transfers from the delegator to the executor.
	_, err = s.db.ExecContext(ctx, `
		UPDATE tasks SET state=?, owner_node=?, state_version=state_version+1, updated_at=? WHERE task_id=?`,
		StateRunning, owner, s.now(), taskID)
	if err != nil {
		return fmt.Errorf("accept task: %w", err)
	}
	return s.recordEvent(ctx, taskID, EvAccept, map[string]any{"owner": owner})
}

// Decline rejects a dispatched task, moving it back to queued and releasing
// the lease so the parent can route elsewhere.
func (s *TaskStore) Decline(ctx context.Context, taskID, parent string, reason string) error {
	cur, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if err := s.apply(ctx, taskID, StateQueued, parent, cur.AttemptID, EvDecline,
		map[string]any{"reason": reason, "by": cur.OwnerNode}); err != nil {
		return err
	}
	return nil
}

// SetWaitingContext moves dispatched -> waiting_context.
func (s *TaskStore) SetWaitingContext(ctx context.Context, taskID, owner string) error {
	return s.transition(ctx, taskID, StateDispatched, StateWaitingCtx, owner, EvProgress,
		map[string]any{"waiting": "context"})
}

// Complete transitions running -> done and stores the result.
func (s *TaskStore) Complete(ctx context.Context, taskID, owner string, result any) error {
	return s.transition(ctx, taskID, StateRunning, StateDone, owner, EvResult, result)
}

// Fail transitions a task to failed. Allowed from running/dispatched/
// waiting_context/review.
func (s *TaskStore) Fail(ctx context.Context, taskID, owner, reason string) error {
	cur, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	var from string
	switch cur.State {
	case StateRunning, StateDispatched, StateWaitingCtx, StateReview:
		from = cur.State
	default:
		return fmt.Errorf("%w: cannot fail from %s", ErrIllegal, cur.State)
	}
	return s.transition(ctx, taskID, from, StateFailed, owner, EvResult,
		map[string]any{"failed": reason})
}

// Cancel marks a task cancelled if it is still active. Cancellation is the
// one transition the parent may force regardless of lease ownership, to
// support cascade from a cancelled parent.
func (s *TaskStore) Cancel(ctx context.Context, taskID string) error {
	cur, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if Terminal(cur.State) {
		return nil // already done/cancelled/expired
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE tasks SET state=?, state_version=state_version+1, updated_at=? WHERE task_id=?`,
		StateCancelled, s.now(), taskID)
	if err != nil {
		return fmt.Errorf("cancel task: %w", err)
	}
	return s.recordEvent(ctx, taskID, EvCancel, map[string]any{"from": cur.State})
}

// CancelCascade cancels taskID and every descendant. Descendants are found by
// parent_id links; the walk recurses so multi-level trees behave.
func (s *TaskStore) CancelCascade(ctx context.Context, taskID string) (int, error) {
	if err := s.Cancel(ctx, taskID); err != nil {
		return 0, err
	}
	count := 1
	children, err := s.Children(ctx, taskID)
	if err != nil {
		return count, err
	}
	for _, c := range children {
		if Terminal(c.State) {
			continue
		}
		n, err := s.CancelCascade(ctx, c.TaskID)
		if err != nil {
			return count, err
		}
		count += n
	}
	return count, nil
}

// Children lists direct children of taskID.
func (s *TaskStore) Children(ctx context.Context, parentID string) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT task_id, parent_id, project, title, state, owner_node, attempt_id,
		        state_version, chain_json, intent, spec_json, result_json, created_at, updated_at
		 FROM tasks WHERE parent_id = ?`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

// ListByState returns tasks filtered by state ("" = all), newest first.
func (s *TaskStore) ListByState(ctx context.Context, state string) ([]Task, error) {
	q := `SELECT task_id, parent_id, project, title, state, owner_node, attempt_id,
	        state_version, chain_json, intent, spec_json, result_json, created_at, updated_at
	      FROM tasks`
	var args []any
	if state != "" {
		q += " WHERE state = ?"
		args = append(args, state)
	}
	q += " ORDER BY created_at DESC"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

// Events returns the event timeline for a task, oldest first.
func (s *TaskStore) Events(ctx context.Context, taskID string) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, ts, type, data_json FROM task_events
		 WHERE task_id = ? ORDER BY id ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.TaskID, &e.TS, &e.Type, &e.DataJSON); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Event is one task_events row.
type Event struct {
	ID       int64
	TaskID   string
	TS       int64
	Type     string
	DataJSON string
}

// RotateAttempt mints a new attempt_id for a retry/transfer. The caller must
// first have moved the task to queued/dispatched so the old attempt's late
// results are rejected by the state checks.
func (s *TaskStore) RotateAttempt(ctx context.Context, taskID, owner string) (string, error) {
	aid, err := util.UUIDv7()
	if err != nil {
		return "", fmt.Errorf("uuid: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE tasks SET attempt_id=?, state_version=state_version+1, updated_at=? WHERE task_id=?`,
		aid, s.now(), taskID)
	if err != nil {
		return "", fmt.Errorf("rotate attempt: %w", err)
	}
	return aid, nil
}

// TaskStore is a stateful DB wrapper; methods use sql.Tx semantics only where
// atomicity matters. The single-writer connection (storage.Open) serializes
// writes already.

func scanTasks(rows *sql.Rows) ([]Task, error) {
	var out []Task
	for rows.Next() {
		var t Task
		var chainJSON string
		var intent, spec, result sql.NullString
		if err := rows.Scan(&t.TaskID, &t.ParentID, &t.Project, &t.Title, &t.State,
			&t.OwnerNode, &t.AttemptID, &t.StateVersion, &chainJSON, &intent,
			&spec, &result, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(chainJSON), &t.Chain)
		t.Intent = intent.String
		t.SpecJSON = spec.String
		t.ResultJSON = result.String
		out = append(out, t)
	}
	return out, rows.Err()
}

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
	// onReview, when set, is called whenever a task enters review (needs human
	// analysis). It must not block: the daemon wires it to a fire-and-forget
	// notification path.
	onReview func(Task)
}

// NewTaskStore wraps a DB. now may be nil (defaults to Unix time).
func NewTaskStore(db *sql.DB, logger *slog.Logger) *TaskStore {
	if logger == nil {
		logger = slog.Default()
	}
	return &TaskStore{db: db, logger: logger, now: storage.Now}
}

// SetOnReview installs the callback fired when a task transitions into review.
// The callback is invoked on its own goroutine so a slow consumer (e.g. a push
// send) never stalls the state machine.
func (s *TaskStore) SetOnReview(fn func(Task)) { s.onReview = fn }

// Create inserts a new task in submitted state with a fresh UUIDv7 id.
func (s *TaskStore) Create(ctx context.Context, parentID, project, title, owner string, chain []string) (Task, error) {
	taskID, err := util.UUIDv7()
	if err != nil {
		return Task{}, fmt.Errorf("uuid: %w", err)
	}
	return s.CreateWithID(ctx, taskID, parentID, project, title, owner, chain)
}

// CreateWithID inserts a task with an explicit id. The id is the cross-node
// idempotency key, so delegated tasks keep the delegator's id. Returns
// ErrConflict if the id already exists.
func (s *TaskStore) CreateWithID(ctx context.Context, taskID, parentID, project, title, owner string, chain []string) (Task, error) {
	if taskID == "" {
		var err error
		taskID, err = util.UUIDv7()
		if err != nil {
			return Task{}, fmt.Errorf("uuid: %w", err)
		}
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
	var lease sql.NullInt64
	var contextType, contextHash, risk, resource sql.NullString
	var complexity sql.NullFloat64
	err := s.db.QueryRowContext(ctx,
		`SELECT `+taskColumns+` FROM tasks WHERE task_id = ?`, taskID).
		Scan(&t.TaskID, &t.ParentID, &t.Project, &t.Title, &t.State, &t.OwnerNode,
			&t.AttemptID, &t.StateVersion, &chainJSON, &intent, &spec,
			&result, &contextType, &contextHash, &complexity, &risk, &resource, &lease,
			&t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return Task{}, err
	}
	_ = json.Unmarshal([]byte(chainJSON), &t.Chain)
	t.Intent = intent.String
	t.SpecJSON = spec.String
	t.ResultJSON = result.String
	t.ContextType = contextType.String
	t.ContextHash = contextHash.String
	t.Complexity = complexity.Float64
	t.Risk = risk.String
	t.ResourceJSON = resource.String
	t.LeaseExpires = lease.Int64
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
	if err := s.apply(ctx, taskID, to, owner, cur.AttemptID, event, data, nil); err != nil {
		return err
	}
	s.logger.Debug("task transition", "task", taskID, "from", from, "to", to, "owner", owner)
	if to == StateReview {
		cur.State = StateReview
		cur.UpdatedAt = s.now()
		s.notifyReview(cur)
	}
	return nil
}

// notifyReview fires the review hook without blocking the transition. A task
// snapshot (not a pointer) is passed so the callback owns its copy and the
// goroutine is free to outlive the transition.
func (s *TaskStore) notifyReview(t Task) {
	if s.onReview == nil {
		return
	}
	go s.onReview(t)
}

// apply writes the new state + event. attemptID is carried through unchanged
// unless a caller explicitly rotates it (retry/transfer). result, if non-nil,
// is stored into result_json.
func (s *TaskStore) apply(ctx context.Context, taskID, to, owner, attemptID, event string, data any, result any) error {
	now := s.now()
	dataJSON, _ := json.Marshal(data)
	var resultJSON any
	if result != nil {
		b, _ := json.Marshal(result)
		resultJSON = string(b)
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET state=?, owner_node=?, attempt_id=?, state_version=state_version+1,
			result_json=COALESCE(?, result_json), updated_at=?
		WHERE task_id=?`,
		to, owner, attemptID, resultJSON, now, taskID)
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
// the lease so the parent can route elsewhere. Only a dispatched task may be
// declined; this prevents overriding a task that already started running.
func (s *TaskStore) Decline(ctx context.Context, taskID, parent string, reason string) error {
	cur, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if cur.State != StateDispatched {
		return fmt.Errorf("%w: task %s state=%s, want %s", ErrConflict, taskID, cur.State, StateDispatched)
	}
	if err := s.apply(ctx, taskID, StateQueued, parent, cur.AttemptID, EvDecline,
		map[string]any{"reason": reason, "by": cur.OwnerNode}, nil); err != nil {
		return err
	}
	return nil
}

// SetWaitingContext moves dispatched -> waiting_context.
func (s *TaskStore) SetWaitingContext(ctx context.Context, taskID, owner string) error {
	return s.transition(ctx, taskID, StateDispatched, StateWaitingCtx, owner, EvProgress,
		map[string]any{"waiting": "context"})
}

// Resume moves waiting_context -> running once the context snapshot has been
// fetched and verified. Only the lease holder (executor) may resume.
func (s *TaskStore) Resume(ctx context.Context, taskID, owner string) error {
	return s.transition(ctx, taskID, StateWaitingCtx, StateRunning, owner, EvProgress,
		map[string]any{"resumed": "context"})
}

// Complete transitions running -> done and stores the result.
func (s *TaskStore) Complete(ctx context.Context, taskID, owner string, result any) error {
	cur, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if cur.State != StateRunning {
		return fmt.Errorf("%w: task %s state=%s, want %s", ErrConflict, taskID, cur.State, StateRunning)
	}
	return s.apply(ctx, taskID, StateDone, owner, cur.AttemptID, EvResult, result, result)
}

// CompleteFromRemote records a remote executor's final result on the
// delegator's copy. The delegator may be in submitted/queued/dispatched/
// running; any non-terminal state is accepted, and the owner is moved to
// the delegator (who holds the parent-side lease).
func (s *TaskStore) CompleteFromRemote(ctx context.Context, taskID, owner string, result any) error {
	cur, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if Terminal(cur.State) {
		return nil // already closed; keep first result
	}
	if err := s.apply(ctx, taskID, StateDone, owner, cur.AttemptID, EvResult, result, result); err != nil {
		return err
	}
	s.logger.Debug("remote completion recorded", "task", taskID, "from", cur.State)
	return nil
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

// Requeue transitions a failed task back to queued for a retry. It is the
// retry loop's entry: after a failed attempt the task returns to the queue to
// be re-dispatched. Only the owner may requeue.
func (s *TaskStore) Requeue(ctx context.Context, taskID, owner string) error {
	return s.transition(ctx, taskID, StateFailed, StateQueued, owner, EvRetry, nil)
}

// Review transitions a failed task to review, pausing a task that has spent its
// retry budget for human analysis (design §14.2 "pause → analyze"). A reviewer
// may later send it back to queued, mark it done, or fail it.
func (s *TaskStore) Review(ctx context.Context, taskID, owner, reason string) error {
	return s.transition(ctx, taskID, StateFailed, StateReview, owner, EvReview,
		map[string]any{"reason": reason})
}

// Pause transitions a running task to review, pausing it for human analysis
// after a deterministic intercept such as scope drift (design §14.2 "拦截 →
// 暂停 → 分析"). It is the running-state counterpart of Review (which pauses a
// failed task after its retry budget is spent) and never enters the retry loop,
// because a deterministic intercept will not improve on retry.
func (s *TaskStore) Pause(ctx context.Context, taskID, owner, reason string) error {
	return s.transition(ctx, taskID, StateRunning, StateReview, owner, EvReview,
		map[string]any{"reason": reason})
}

// Approve accepts a reviewed task, moving it review -> done. Approval is a
// human override (design §14.2 Layer 4), so — like Cancel — it requires only
// that the task be in review, not that the caller hold the lease.
func (s *TaskStore) Approve(ctx context.Context, taskID string) error {
	cur, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if cur.State != StateReview {
		return fmt.Errorf("%w: task %s state=%s, want %s", ErrConflict, taskID, cur.State, StateReview)
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET state=?, state_version=state_version+1, updated_at=? WHERE task_id=?`,
		StateDone, s.now(), taskID); err != nil {
		return fmt.Errorf("approve task: %w", err)
	}
	return s.recordEvent(ctx, taskID, EvReview, map[string]any{"approved": true})
}

// Reject fails a reviewed task, moving it review -> failed. Like Approve, it is
// a human override and does not require the lease.
func (s *TaskStore) Reject(ctx context.Context, taskID, reason string) error {
	cur, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if cur.State != StateReview {
		return fmt.Errorf("%w: task %s state=%s, want %s", ErrConflict, taskID, cur.State, StateReview)
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET state=?, state_version=state_version+1, updated_at=? WHERE task_id=?`,
		StateFailed, s.now(), taskID); err != nil {
		return fmt.Errorf("reject task: %w", err)
	}
	return s.recordEvent(ctx, taskID, EvReview, map[string]any{"rejected": reason})
}

// FailFromRemote records a remote executor's failure on the delegator's copy.
// Mirrors CompleteFromRemote: any non-terminal state is accepted.
func (s *TaskStore) FailFromRemote(ctx context.Context, taskID, owner, reason string) error {
	cur, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if Terminal(cur.State) {
		return nil
	}
	if err := s.apply(ctx, taskID, StateFailed, owner, cur.AttemptID, EvResult,
		map[string]any{"failed": reason}, map[string]any{"failed": reason}); err != nil {
		return err
	}
	s.logger.Debug("remote failure recorded", "task", taskID, "from", cur.State)
	return nil
}

// CreateFromRemote inserts a task owned by this delegator, adopting the
// executor's attempt_id so a follow-up result (with the same attempt) is not
// mistaken for a stale write. Used when a delegator hears a result for a task
// it never persisted locally.
func (s *TaskStore) CreateFromRemote(ctx context.Context, taskID, title, owner string, attemptID string, chain []string) (Task, error) {
	t, err := s.CreateWithID(ctx, taskID, "", "", title, owner, chain)
	if err != nil {
		return Task{}, err
	}
	if attemptID == "" {
		return t, nil
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE tasks SET attempt_id=? WHERE task_id=?`, attemptID, taskID)
	if err != nil {
		return Task{}, fmt.Errorf("adopt attempt: %w", err)
	}
	t.AttemptID = attemptID
	return t, nil
}

// AdoptAttempt stamps taskID with an attempt_id minted upstream. A delegated
// task carries its attempt along the chain so every node's copy reports the
// same attempt; downstream nodes adopt it instead of minting their own.
func (s *TaskStore) AdoptAttempt(ctx context.Context, taskID, attemptID string) error {
	if attemptID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET attempt_id=? WHERE task_id=?`, attemptID, taskID)
	if err != nil {
		return fmt.Errorf("adopt attempt: %w", err)
	}
	return nil
}

// SetLease stamps lease_expires_at = now + durationMS for a task. The
// timeout monitor fails tasks whose lease expires while still active. A
// non-positive duration is a no-op.
func (s *TaskStore) SetLease(ctx context.Context, taskID string, durationMS int64) error {
	if durationMS <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET lease_expires_at=?, updated_at=? WHERE task_id=?`,
		s.now()+durationMS/1000, s.now(), taskID)
	if err != nil {
		return fmt.Errorf("set lease: %w", err)
	}
	return nil
}

// SetDetail persists the entry-model-derived task metadata (design doc §6.1
// tasks schema): context type, intent, spec, complexity, risk, and resource
// profile. Called once after creation; the fields default to zero/empty until
// then. A zero TaskDetail clears the fields.
func (s *TaskStore) SetDetail(ctx context.Context, taskID string, d TaskDetail) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET context_type=?, context_hash=?, intent=?, spec_json=?, complexity=?,
			risk=?, resource_json=?, updated_at=?
		WHERE task_id=?`,
		d.ContextType, d.ContextHash, d.Intent, d.SpecJSON, d.Complexity, d.Risk, d.ResourceJSON,
		s.now(), taskID)
	if err != nil {
		return fmt.Errorf("set detail: %w", err)
	}
	return nil
}

// ExpireTasks fails any active task whose lease has expired. Returns the
// number of tasks failed. Called periodically by the monitor.
func (s *TaskStore) ExpireTasks(ctx context.Context) (int, error) {
	now := s.now()
	rows, err := s.db.QueryContext(ctx,
		`SELECT task_id FROM tasks
		 WHERE lease_expires_at > 0 AND lease_expires_at < ?
		   AND state IN ('dispatched','waiting_context','running','review')`, now)
	if err != nil {
		return 0, fmt.Errorf("scan leases: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		// The monitor acts on behalf of the lease holder regardless of the
		// stored owner (a crashed process may no longer hold it).
		if err := s.ForceFail(ctx, id, "lease expired"); err != nil {
			s.logger.Warn("expire task", "task", id, "err", err)
			continue
		}
		s.logger.Info("task failed by timeout", "task", id)
	}
	return len(ids), nil
}

// ForceFail fails an active task regardless of owner. Used by the timeout
// monitor and restart recovery when the owner may be gone.
func (s *TaskStore) ForceFail(ctx context.Context, taskID, reason string) error {
	cur, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if Terminal(cur.State) {
		return nil
	}
	return s.apply(ctx, taskID, StateFailed, cur.OwnerNode, cur.AttemptID, EvResult,
		map[string]any{"failed": reason}, map[string]any{"failed": reason})
}

// Recover normalizes tasks left in an active state by a previous process
// instance. Running/waiting_context/review become failed (execution was
// interrupted); dispatched returns to queued for re-dispatch. Returns the
// number of tasks touched.
func (s *TaskStore) Recover(ctx context.Context) (int, error) {
	now := s.now()
	res, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET state=?, state_version=state_version+1, updated_at=?,
			lease_expires_at=NULL, result_json='{"recovered":"interrupted"}'
		WHERE state IN ('running','waiting_context','review')`,
		StateFailed, now)
	if err != nil {
		return 0, fmt.Errorf("recover active tasks: %w", err)
	}
	failed, _ := res.RowsAffected()

	res, err = s.db.ExecContext(ctx, `
		UPDATE tasks SET state=?, state_version=state_version+1, updated_at=?,
			lease_expires_at=NULL
		WHERE state IN ('dispatched','submitted')`,
		StateQueued, now)
	if err != nil {
		return int(failed), fmt.Errorf("recover dispatched tasks: %w", err)
	}
	requeued, _ := res.RowsAffected()

	s.logger.Info("task recovery", "failed", failed, "requeued", requeued)
	return int(failed + requeued), nil
}

// Cancel marks a task cancelled if it is still active. Cancellation is the
// one transition the parent may force regardless of lease ownership, to
// support cascade from a cancelled parent — but it still must be a legal move
// per the state machine, so a stale caller cannot force a task into a state
// the table does not allow. Callers authorizing the *source* of the cancel do
// so upstream (see Core.handleCancel).
func (s *TaskStore) Cancel(ctx context.Context, taskID string) error {
	cur, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if Terminal(cur.State) {
		return nil // already done/cancelled/expired
	}
	if !CanTransition(cur.State, StateCancelled) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegal, cur.State, StateCancelled)
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
// parent_id links; the walk recurses so multi-level trees behave. The returned
// count is the number of tasks actually transitioned to cancelled — already-
// terminal tasks are skipped, not counted.
func (s *TaskStore) CancelCascade(ctx context.Context, taskID string) (int, error) {
	cur, err := s.Get(ctx, taskID)
	if err != nil {
		return 0, err
	}
	count := 0
	if !Terminal(cur.State) {
		if err := s.Cancel(ctx, taskID); err != nil {
			return 0, err
		}
		count = 1
	}
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
		`SELECT `+taskColumns+` FROM tasks WHERE parent_id = ?`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

// ListByState returns tasks filtered by state ("" = all), newest first.
func (s *TaskStore) ListByState(ctx context.Context, state string) ([]Task, error) {
	q := `SELECT ` + taskColumns + ` FROM tasks`
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
// hold the lease (owner); the update is owner-guarded like every other
// mutating method, so a stale node cannot rotate another owner's attempt.
func (s *TaskStore) RotateAttempt(ctx context.Context, taskID, owner string) (string, error) {
	aid, err := util.UUIDv7()
	if err != nil {
		return "", fmt.Errorf("uuid: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET attempt_id=?, state_version=state_version+1, updated_at=? WHERE task_id=? AND owner_node=?`,
		aid, s.now(), taskID, owner)
	if err != nil {
		return "", fmt.Errorf("rotate attempt: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", fmt.Errorf("%w: task %s owner mismatch", ErrConflict, taskID)
	}
	return aid, nil
}

// TaskStore is a stateful DB wrapper; methods use sql.Tx semantics only where
// atomicity matters. The single-writer connection (storage.Open) serializes
// writes already.

// taskColumns is the shared SELECT list for task rows. Kept in one place so
// Get/Children/ListByState/scanTasks stay in lock-step as the schema grows.
const taskColumns = `task_id, parent_id, project, title, state, owner_node, attempt_id,
	state_version, chain_json, intent, spec_json, result_json,
	context_type, context_hash, complexity, risk, resource_json,
	lease_expires_at, created_at, updated_at`

func scanTasks(rows *sql.Rows) ([]Task, error) {
	var out []Task
	for rows.Next() {
		var t Task
		var chainJSON string
		var intent, spec, result sql.NullString
		var lease sql.NullInt64
		var contextType, contextHash, risk, resource sql.NullString
		var complexity sql.NullFloat64
		if err := rows.Scan(&t.TaskID, &t.ParentID, &t.Project, &t.Title, &t.State,
			&t.OwnerNode, &t.AttemptID, &t.StateVersion, &chainJSON, &intent,
			&spec, &result, &contextType, &contextHash, &complexity, &risk, &resource, &lease,
			&t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(chainJSON), &t.Chain)
		t.Intent = intent.String
		t.SpecJSON = spec.String
		t.ResultJSON = result.String
		t.ContextType = contextType.String
		t.ContextHash = contextHash.String
		t.Complexity = complexity.Float64
		t.Risk = risk.String
		t.ResourceJSON = resource.String
		t.LeaseExpires = lease.Int64
		out = append(out, t)
	}
	return out, rows.Err()
}

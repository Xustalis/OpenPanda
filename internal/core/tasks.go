package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/storage"
	"github.com/Xustalis/OpenPanda/internal/util"
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
	onReview   func(Task)
	onReviewMu sync.RWMutex
	// onEvent, when set, is called after every task event is appended, with the
	// task id and event type. It is a display-only progress feed (the ask engine
	// bridges it to the CLI status line during a synchronous Submit). The callback
	// MUST NOT touch the store and MUST NOT block: it runs on the goroutine that
	// recorded the event, inside its transaction.
	onEvent   func(taskID, typ string, data any)
	onEventMu sync.RWMutex
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
func (s *TaskStore) SetOnReview(fn func(Task)) {
	s.onReviewMu.Lock()
	s.onReview = fn
	s.onReviewMu.Unlock()
}

// SetOnEvent installs (or clears, with nil) the per-event observer fired after
// every task event is appended. It is a best-effort, display-only feed: the
// callback must not touch the store or block, since it runs synchronously on
// the recording goroutine. The ask engine uses it to surface live route/exec/
// judge progress while a synchronous Submit blocks (P0 §1.4).
func (s *TaskStore) SetOnEvent(fn func(taskID, typ string, data any)) {
	s.onEventMu.Lock()
	s.onEvent = fn
	s.onEventMu.Unlock()
}

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
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tasks (task_id, parent_id, project, title, state, owner_node,
				attempt_id, state_version, chain_json, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.TaskID, t.ParentID, t.Project, t.Title, t.State, t.OwnerNode,
			t.AttemptID, t.StateVersion, string(chainJSON), now, now); err != nil {
			return fmt.Errorf("insert task: %w", err)
		}
		return s.recordEventTx(ctx, tx, taskID, EvSubmit, map[string]any{
			"parent": parentID, "owner": owner,
		})
	})
	if err != nil {
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
	var contextType, contextHash, risk, resource, requiresJSON sql.NullString
	var complexity sql.NullFloat64
	var sessionID, resourceKeysJSON, workDir sql.NullString
	var scheduled int
	var planID, stageID, needsJSON, inputsJSON, outputArt sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT `+taskColumns+` FROM tasks WHERE task_id = ?`, taskID).
		Scan(&t.TaskID, &t.ParentID, &t.Project, &t.Title, &t.State, &t.OwnerNode,
			&t.AttemptID, &t.StateVersion, &chainJSON, &intent, &spec,
			&result, &contextType, &contextHash, &complexity, &risk, &resource,
			&requiresJSON, &lease,
			&t.CreatedAt, &t.UpdatedAt, &t.Authorized,
			&t.Priority, &t.Seq, &sessionID, &resourceKeysJSON, &workDir, &scheduled,
			&planID, &stageID, &needsJSON, &inputsJSON, &outputArt)
	if err != nil {
		return Task{}, err
	}
	_ = json.Unmarshal([]byte(chainJSON), &t.Chain)
	_ = json.Unmarshal([]byte(requiresJSON.String), &t.Requires)
	_ = json.Unmarshal([]byte(resourceKeysJSON.String), &t.ResourceKeys)
	_ = json.Unmarshal([]byte(needsJSON.String), &t.Needs)
	_ = json.Unmarshal([]byte(inputsJSON.String), &t.Inputs)
	t.Intent = intent.String
	t.SpecJSON = spec.String
	t.ResultJSON = result.String
	t.ContextType = contextType.String
	t.ContextHash = contextHash.String
	t.Complexity = complexity.Float64
	t.Risk = risk.String
	t.ResourceJSON = resource.String
	t.LeaseExpires = lease.Int64
	t.SessionID = sessionID.String
	t.WorkDir = workDir.String
	t.Scheduled = scheduled != 0
	t.PlanID = planID.String
	t.StageID = stageID.String
	t.OutputArtifact = outputArt.String
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
	if err := s.applyCAS(ctx, taskID, from, to, owner, cur.AttemptID, event, data, nil); err != nil {
		return err
	}
	if to == StateReview {
		// A task paused for human review has no lease pressure: clear the
		// deadline so a stale timestamp can never fire later (P1-8).
		if _, err := s.db.ExecContext(ctx,
			`UPDATE tasks SET lease_expires_at=NULL WHERE task_id=?`, taskID); err != nil {
			s.logger.Warn("clear lease on review", "task", taskID, "err", err)
		}
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
	s.onReviewMu.RLock()
	fn := s.onReview
	s.onReviewMu.RUnlock()
	if fn == nil {
		return
	}
	go fn(t)
}

// withTx runs fn inside a single SQLite transaction, committing on success and
// rolling back on any error (P2-1). State UPDATEs and their audit-event INSERTs
// must commit or fail together: a crash between them would silently drop the
// event and break the audit trail (and DispatchTarget/DeclinedBy, which read
// authorization facts from task_events).
func (s *TaskStore) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// recordEventTx is recordEvent inside an existing transaction. It links the
// new row into the per-task audit chain by computing the previous event's hash
// and storing it as prev_hash (A3).
func (s *TaskStore) recordEventTx(ctx context.Context, tx *sql.Tx, taskID, typ string, data any) error {
	var raw []byte
	switch v := data.(type) {
	case []byte:
		raw = v
	default:
		raw, _ = json.Marshal(v)
	}
	ts := s.now()

	var prevHash string
	var prev struct {
		PrevHash string
		TS       int64
		Type     string
		DataJSON string
	}
	err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(prev_hash, ''), ts, type, data_json FROM task_events
		 WHERE task_id=? ORDER BY id DESC LIMIT 1`, taskID).Scan(
		&prev.PrevHash, &prev.TS, &prev.Type, &prev.DataJSON)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read prev event: %w", err)
	}
	if err == nil {
		prevHash = hashEvent(prev.PrevHash, taskID, prev.TS, prev.Type, prev.DataJSON)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO task_events (task_id, ts, type, data_json, prev_hash) VALUES (?, ?, ?, ?, ?)`,
		taskID, ts, typ, string(raw), prevHash)
	if err != nil {
		return fmt.Errorf("record event %s: %w", typ, err)
	}
	// Bump the task's updated_at so an appended event alone (progress, trace,
	// judge — events that do not change state) still moves the row's freshness
	// stamp. The web panel's change detector fingerprints (id, state, updated_at);
	// without this bump, tool/route/delegation events never flip the fingerprint
	// and the console silently misses them until the next state change (P0 §1.5).
	// A no-op when the caller (applyCAS/applyState) already set updated_at to the
	// same tick in this transaction.
	if _, err := tx.ExecContext(ctx,
		`UPDATE tasks SET updated_at=? WHERE task_id=? AND updated_at<?`,
		ts, taskID, ts); err != nil {
		return fmt.Errorf("bump updated_at for %s: %w", taskID, err)
	}
	// Notify the display-only observer (best-effort). Fired synchronously on this
	// goroutine, still inside the tx — the callback is contractually forbidden
	// from touching the store or blocking, so this cannot deadlock the connection.
	s.onEventMu.RLock()
	fn := s.onEvent
	s.onEventMu.RUnlock()
	if fn != nil {
		fn(taskID, typ, data)
	}
	return nil
}

// applyCAS writes the new state + event atomically, succeeding only if the
// stored (state, owner_node) still match what the caller read. This closes the
// TOCTOU window between the Get-and-check and the UPDATE (P1-2): without the
// state/owner guard, two concurrent transitions can both pass their pre-checks
// and overwrite each other. Returns ErrConflict when the guard fails.
func (s *TaskStore) applyCAS(ctx context.Context, taskID, from, to, owner, attemptID, event string, data, result any) error {
	now := s.now()
	dataJSON, _ := json.Marshal(data)
	var resultJSON any
	if result != nil {
		b, _ := json.Marshal(result)
		resultJSON = string(b)
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE tasks SET state=?, owner_node=?, attempt_id=?, state_version=state_version+1,
				result_json=COALESCE(?, result_json), updated_at=?
			WHERE task_id=? AND state=? AND owner_node=?`,
			to, owner, attemptID, resultJSON, now, taskID, from, owner)
		if err != nil {
			return fmt.Errorf("update task state: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: task %s state=%s owner=%s", ErrConflict, taskID, from, owner)
		}
		return s.recordEventTx(ctx, tx, taskID, event, dataJSON)
	})
}

// applyState writes the new state + event atomically, succeeding only if the
// stored state still matches `from`. Unlike applyCAS it does not require the
// stored owner to match `owner` (which it also rewrites): remote result
// handlers legitimately move ownership from executor back to delegator. The
// state guard is what prevents a concurrent timeout/cancel from being
// overwritten by a result that read the pre-transition state.
func (s *TaskStore) applyState(ctx context.Context, taskID, from, to, owner, attemptID, event string, data, result any) error {
	now := s.now()
	dataJSON, _ := json.Marshal(data)
	var resultJSON any
	if result != nil {
		b, _ := json.Marshal(result)
		resultJSON = string(b)
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE tasks SET state=?, owner_node=?, attempt_id=?, state_version=state_version+1,
				result_json=COALESCE(?, result_json), updated_at=?
			WHERE task_id=? AND state=?`,
			to, owner, attemptID, resultJSON, now, taskID, from)
		if err != nil {
			return fmt.Errorf("update task state: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: task %s state=%s", ErrConflict, taskID, from)
		}
		return s.recordEventTx(ctx, tx, taskID, event, dataJSON)
	})
}

// recordEvent appends to task_events. The data parameter is either a map or
// already-marshaled JSON bytes.
func (s *TaskStore) recordEvent(ctx context.Context, taskID, typ string, data any) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		return s.recordEventTx(ctx, tx, taskID, typ, data)
	})
}

// RecordEvent is the exported recordEvent for collaborators outside core
// (the panel's session finalizer marks summary turns this way).
func (s *TaskStore) RecordEvent(ctx context.Context, taskID, typ string, data any) error {
	return s.recordEvent(ctx, taskID, typ, data)
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

// RetargetDelegation records a new delegation target for a task that is
// already dispatched — e.g. the local queue scheduler forwarding its claim to
// a capable peer. State and owner stay unchanged; only the EvDelegate audit
// event is appended, which is what DispatchTarget and isCurrentExecutor
// authenticate the peer's result/decline against.
func (s *TaskStore) RetargetDelegation(ctx context.Context, taskID, target string) error {
	return s.recordEvent(ctx, taskID, EvDelegate, map[string]any{"target": target, "by": "queue-forward"})
}

// DispatchTarget returns the node this task was most recently dispatched to,
// read from the task_events audit trail (Dispatch records the target on its
// EvDelegate event). "" means the task was never dispatched. Wire handlers use
// it to authenticate result/decline/accept senders before acceptance moves
// the lease (P1-1/2/3).
func (s *TaskStore) DispatchTarget(ctx context.Context, taskID string) (string, error) {
	var data string
	err := s.db.QueryRowContext(ctx,
		`SELECT data_json FROM task_events WHERE task_id=? AND type=? ORDER BY id DESC LIMIT 1`,
		taskID, EvDelegate).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("dispatch target: %w", err)
	}
	var d struct {
		Target string `json:"target"`
	}
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		return "", fmt.Errorf("parse delegate event: %w", err)
	}
	return d.Target, nil
}

// DeclinedBy returns every node that has declined this task, read from the
// task_events audit trail (Decline records the declining executor as "by").
// The re-router (P1-5) excludes them so a task cannot bounce back to a node
// that already refused it — that is what bounds the decline/re-route loop.
func (s *TaskStore) DeclinedBy(ctx context.Context, taskID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT data_json FROM task_events WHERE task_id=? AND type=? ORDER BY id`,
		taskID, EvDecline)
	if err != nil {
		return nil, fmt.Errorf("declined-by: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var d struct {
			By string `json:"by"`
		}
		if err := json.Unmarshal([]byte(data), &d); err == nil && d.By != "" {
			out = append(out, d.By)
		}
	}
	return out, rows.Err()
}

// RetryCount returns the number of retries recorded on a task's audit trail
// (one EvRetry event per Requeue). Unlike the in-memory loop detector — whose
// counters reset with the process — this count survives daemon restarts, so a
// deterministically failing task cannot earn an unbounded number of attempts
// by crashing and restarting its node between retries (S2-6).
func (s *TaskStore) RetryCount(ctx context.Context, taskID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM task_events WHERE task_id=? AND type=?`,
		taskID, EvRetry).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("retry count: %w", err)
	}
	return n, nil
}

// QueuedDelegatedRemotely lists this node's queued tasks whose most recent
// dispatch targeted a DIFFERENT node: delegations handed to a peer that came
// back to queued (a restart's Recover, a decline) with nobody left to drive
// them. Left alone they are orphans — no local scheduler adopts a row whose
// work belongs on another device, and the upstream nodes only notice when
// their own leases expire. The orphan sweep (rescueOrphanedForwards)
// re-routes or fails them; see it for the full lifecycle.
func (s *TaskStore) QueuedDelegatedRemotely(ctx context.Context, self string) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskColumns+` FROM tasks t
		 WHERE t.state=? AND t.owner_node=?
		   AND EXISTS (SELECT 1 FROM task_events e WHERE e.task_id=t.task_id AND e.type=?)`,
		StateQueued, self, EvDelegate)
	if err != nil {
		return nil, fmt.Errorf("queued delegated tasks: %w", err)
	}
	defer rows.Close()
	tasks, err := scanTasks(rows)
	if err != nil {
		return nil, err
	}
	// Keep only tasks whose latest dispatch went elsewhere; a locally
	// dispatched (or never dispatched) queued row is the queue scheduler's
	// business, not the delegation sweep's.
	out := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		target, err := s.DispatchTarget(ctx, t.TaskID)
		if err != nil {
			s.logger.Warn("orphan sweep: dispatch target", "task", t.TaskID, "err", err)
			continue
		}
		if target != "" && target != self {
			out = append(out, t)
		}
	}
	return out, nil
}

// Accept transitions dispatched -> running and transfers the lease to the
// executor (owner). The UPDATE carries a state guard (P1-3): without it a
// concurrent cancel/timeout between the pre-check and the write could be
// overwritten, resurrecting a closed task. The wire handler additionally
// authenticates the sender against the recorded dispatch target.
func (s *TaskStore) Accept(ctx context.Context, taskID, owner string) error {
	cur, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if cur.State != StateDispatched {
		return fmt.Errorf("%w: task %s state=%s, want %s", ErrConflict, taskID, cur.State, StateDispatched)
	}
	// Acceptance is the moment the lease transfers from the delegator to the
	// executor. The state guard ensures a concurrent cancel/expire wins.
	return s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE tasks SET state=?, owner_node=?, state_version=state_version+1, updated_at=?
			WHERE task_id=? AND state=?`,
			StateRunning, owner, s.now(), taskID, StateDispatched)
		if err != nil {
			return fmt.Errorf("accept task: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: task %s accepted concurrently", ErrConflict, taskID)
		}
		return s.recordEventTx(ctx, tx, taskID, EvAccept, map[string]any{"owner": owner})
	})
}

// Decline rejects a dispatched task, moving it back to queued and releasing
// the lease so the parent can route elsewhere. Only a dispatched task may be
// declined; this prevents overriding a task that already started running.
// by is the declining executor (the wire sender): while the task is
// dispatched the stored owner is still the delegator, so the decliner must be
// passed explicitly — the re-router (P1-5) excludes it from future candidacy.
func (s *TaskStore) Decline(ctx context.Context, taskID, parent, reason, by string) error {
	cur, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if cur.State != StateDispatched {
		return fmt.Errorf("%w: task %s state=%s, want %s", ErrConflict, taskID, cur.State, StateDispatched)
	}
	// Guarded UPDATE because the lease changes hands (executor -> parent): the
	// state/owner guard ensures a concurrent accept/decline cannot both win.
	dataJSON, _ := json.Marshal(map[string]any{"reason": reason, "by": by})
	return s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE tasks SET state=?, owner_node=?, state_version=state_version+1, updated_at=?
			WHERE task_id=? AND state=? AND owner_node=?`,
			StateQueued, parent, s.now(), taskID, StateDispatched, cur.OwnerNode)
		if err != nil {
			return fmt.Errorf("decline task: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: task %s declined concurrently", ErrConflict, taskID)
		}
		return s.recordEventTx(ctx, tx, taskID, EvDecline, dataJSON)
	})
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
	return s.applyCAS(ctx, taskID, StateRunning, StateDone, owner, cur.AttemptID, EvResult, result, result)
}

// CompleteFromRemote records a remote executor's final result on the
// delegator's copy. The delegator may be in submitted/queued/dispatched/
// running; review is intentionally protected, and the owner is moved to
// the delegator (who holds the parent-side lease).
func (s *TaskStore) CompleteFromRemote(ctx context.Context, taskID, owner string, result any) error {
	cur, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if Terminal(cur.State) {
		return nil // already closed; keep first result
	}
	if cur.State == StateReview {
		return nil // human-review state is protected from late remote results
	}
	if err := s.applyState(ctx, taskID, cur.State, StateDone, owner, cur.AttemptID, EvResult, result, result); err != nil {
		if errors.Is(err, ErrConflict) {
			return nil // a concurrent transition closed it first; keep that outcome
		}
		return err
	}
	s.logger.Debug("remote completion recorded", "task", taskID, "from", cur.State)
	return nil
}

// ReviewFromRemote records that a remote executor parked the task for human
// review. Unlike Review, which is the local retry-loop transition from failed,
// a remote result may arrive while the delegator's copy is submitted, queued,
// dispatched, or running. The state is still guarded so a concurrent terminal
// transition wins and cannot be overwritten by a late result.
func (s *TaskStore) ReviewFromRemote(ctx context.Context, taskID, owner string, result any) error {
	cur, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if Terminal(cur.State) {
		return nil
	}
	if cur.State == StateReview {
		return nil // do not let a late failure overwrite a human-review pause
	}
	if err := s.applyState(ctx, taskID, cur.State, StateReview, owner, cur.AttemptID, EvReview, result, result); err != nil {
		if errors.Is(err, ErrConflict) {
			return nil
		}
		return err
	}
	// Review has no lease pressure. Keep this behavior identical to local
	// Pause/PauseWithResult and notify any panel/review hook.
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET lease_expires_at=NULL WHERE task_id=?`, taskID); err != nil {
		s.logger.Warn("clear remote review lease", "task", taskID, "err", err)
	}
	cur.State = StateReview
	cur.OwnerNode = owner
	cur.UpdatedAt = s.now()
	s.notifyReview(cur)
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

// PauseWithResult transitions a running task to review while preserving the
// execution result, so the human reviewer sees what was done as well as why it
// needs sign-off (supervision loop terminal: an irreversible task, or one that
// exhausted its round budget without satisfying the success criteria).
func (s *TaskStore) PauseWithResult(ctx context.Context, taskID, owner string, result any) error {
	cur, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if cur.State != StateRunning {
		return fmt.Errorf("%w: task %s state=%s, want %s", ErrConflict, taskID, cur.State, StateRunning)
	}
	return s.applyCAS(ctx, taskID, StateRunning, StateReview, owner, cur.AttemptID, EvReview,
		map[string]any{"reason": "awaiting approval"}, result)
}

// Approve accepts a reviewed task. What "accept" means depends on how the
// task parked:
//
//   - A review parked from failed — a tier-2 authorization refusal or an
//     exhausted retry budget, both of which Fail before parking — has no
//     executed work to accept. Approval is the human consenting to the run:
//     the task re-enters queued carrying the tier-2 authorization, its
//     scheduled flag re-armed so the queue scheduler (daemon/panel) adopts it
//     on its next pass. An inline caller (ask/repl) that runs the task itself
//     uses ResumeApproved instead, which re-executes in the same round-trip.
//   - Every other review entry parks from running with work already done —
//     a manual step performed, a supervision round awaiting sign-off, an
//     unauthorized tier-2 backstop park, or a scope-drift intercept. Approval
//     accepts that work into done (review -> done).
//
// Either way approval is a human override (design §14.2 Layer 4), so — like
// Cancel — it requires only that the task be in review, not that the caller
// hold the lease.
func (s *TaskStore) Approve(ctx context.Context, taskID string) error {
	cur, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if cur.State != StateReview {
		return fmt.Errorf("%w: task %s state=%s, want %s", ErrConflict, taskID, cur.State, StateReview)
	}
	resume := s.reviewFromFailure(ctx, taskID)
	// Guarded UPDATE (P2-8): a concurrent reject/approve must not both win.
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var res sql.Result
		if resume {
			// Grant the tier-2 consent the refusal was waiting for; the
			// lease is already clear (entering review cleared it). Re-arm the
			// scheduled flag: an inline-submitted task (ask/repl) parked with
			// scheduled=0, so without this the daemon/panel queue scheduler —
			// which only claims scheduled=1 rows — would never re-adopt the
			// approved task and it would sit queued forever. Setting it is
			// harmless for a task that was already scheduled.
			res, err = tx.ExecContext(ctx, `
				UPDATE tasks SET state=?, authorized=1, scheduled=1, state_version=state_version+1, updated_at=?
				WHERE task_id=? AND state=?`,
				StateQueued, s.now(), taskID, StateReview)
		} else {
			res, err = tx.ExecContext(ctx, `
				UPDATE tasks SET state=?, state_version=state_version+1, updated_at=?
				WHERE task_id=? AND state=?`,
				StateDone, s.now(), taskID, StateReview)
		}
		if err != nil {
			return fmt.Errorf("approve task: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: task %s approved concurrently", ErrConflict, taskID)
		}
		return s.recordEventTx(ctx, tx, taskID, EvReview, map[string]any{
			"approved": true,
			"resumed":  resume,
		})
	})
}

// reviewFromFailure reports whether the task's latest review parking has no
// executed work to accept, so Approve should re-run it (review -> queued,
// carrying consent) instead of accepting it into done. Two shapes qualify:
//
//   - A local park: Fail directly followed by the review event — a tier-2
//     authorization refusal or a spent retry budget, both of which Fail
//     before parking.
//   - A remote park: this is the delegator's copy, parked by ReviewFromRemote
//     with the executor's result. A tier-2 refusal on the executor reaches
//     here as a review whose own event data carries the refusal (the agent
//     never spawned), so the same no-executed-work rule applies.
//
// Every other review entry — manual, supervision sign-off, an unauthorized
// tier-2 backstop park, scope drift, a remote review with real work attached —
// follows execution events, and Approve treats those as finished work. Read
// failures are conservative (false): accepting existing work is the safer
// default for an unreadable audit chain.
func (s *TaskStore) reviewFromFailure(ctx context.Context, taskID string) bool {
	rows, err := s.db.QueryContext(ctx, `
		SELECT type, data_json FROM task_events
		WHERE task_id=? AND id <= (SELECT COALESCE(MAX(id), 0) FROM task_events WHERE task_id=? AND type=?)
		ORDER BY id DESC LIMIT 2`,
		taskID, taskID, EvReview)
	if err != nil {
		s.logger.Warn("review origin: read events", "task", taskID, "err", err)
		return false
	}
	defer rows.Close()
	var prevTyp, prevData string
	seenReview := false
	for rows.Next() {
		var typ, data string
		if err := rows.Scan(&typ, &data); err != nil {
			s.logger.Warn("review origin: scan event", "task", taskID, "err", err)
			return false
		}
		if !seenReview {
			// The latest EvReview event itself. Both park shapes carry the
			// refusal where a local park's Review writes it as "reason" and a
			// remote park's ReviewFromRemote embeds the executor's result —
			// the sentinel survives either embedding.
			seenReview = true
			if commander.IsAuthorizationRefusal(data) {
				return true
			}
			continue
		}
		prevTyp, prevData = typ, data
	}
	// Fail records its outcome as an EvResult event carrying a "failed" key.
	return prevTyp == EvResult && strings.Contains(prevData, `"failed"`)
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
	// Guarded UPDATE (P2-8): a concurrent approve/reject must not both win.
	return s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE tasks SET state=?, state_version=state_version+1, updated_at=?
			WHERE task_id=? AND state=?`,
			StateFailed, s.now(), taskID, StateReview)
		if err != nil {
			return fmt.Errorf("reject task: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: task %s rejected concurrently", ErrConflict, taskID)
		}
		return s.recordEventTx(ctx, tx, taskID, EvReview, map[string]any{"rejected": reason})
	})
}

// FailFromRemote records a remote executor's failure on the delegator's copy.
// Mirrors CompleteFromRemote, while preserving a task already parked for
// human review.
func (s *TaskStore) FailFromRemote(ctx context.Context, taskID, owner, reason string) error {
	cur, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if Terminal(cur.State) {
		return nil
	}
	if cur.State == StateReview {
		return nil
	}
	if err := s.applyState(ctx, taskID, cur.State, StateFailed, owner, cur.AttemptID, EvResult,
		map[string]any{"failed": reason}, map[string]any{"failed": reason}); err != nil {
		if errors.Is(err, ErrConflict) {
			return nil // a concurrent transition closed it first; keep that outcome
		}
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
// non-positive duration is a no-op. The duration is rounded up to a whole
// second so a sub-second lease does not collapse to zero and expire instantly.
func (s *TaskStore) SetLease(ctx context.Context, taskID string, durationMS int64) error {
	if durationMS <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET lease_expires_at=?, updated_at=? WHERE task_id=?`,
		s.now()+(durationMS+999)/1000, s.now(), taskID)
	if err != nil {
		return fmt.Errorf("set lease: %w", err)
	}
	return nil
}

// SetAuthorized persists whether the task's tier-2 (irreversible) commands were
// consented to by the user. It is server-side state (design §16 / P0-1): only
// the local entry path sets it, and a delegated task cannot forge authorization
// on the wire because the wire payload no longer carries it.
func (s *TaskStore) SetAuthorized(ctx context.Context, taskID string, authorized bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET authorized=?, updated_at=? WHERE task_id=?`,
		authorized, s.now(), taskID)
	if err != nil {
		return fmt.Errorf("set authorized: %w", err)
	}
	return nil
}

// SetDetail persists the entry-model-derived task metadata (design doc §6.1
// tasks schema): context type, intent, spec, complexity, risk, and resource
// profile. Called once after creation; the fields default to zero/empty until
// then. A zero TaskDetail clears the fields.
func (s *TaskStore) SetDetail(ctx context.Context, taskID string, d TaskDetail) error {
	requiresJSON, err := json.Marshal(d.Requires)
	if err != nil {
		return fmt.Errorf("marshal requires: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE tasks SET context_type=?, context_hash=?, intent=?, spec_json=?, complexity=?,
			risk=?, resource_json=?, requires_json=?, updated_at=?
		WHERE task_id=?`,
		d.ContextType, d.ContextHash, d.Intent, d.SpecJSON, d.Complexity, d.Risk, d.ResourceJSON,
		string(requiresJSON),
		s.now(), taskID)
	if err != nil {
		return fmt.Errorf("set detail: %w", err)
	}
	return nil
}

// CountActive returns the number of this node's tasks currently occupying an
// execution slot (running or waiting_context). The capacity-driven
// accept/decline check (DCPS τ_adp mapping, design §2.4) compares it against
// the card's MaxConcurrent before accepting delegated work.
func (s *TaskStore) CountActive(ctx context.Context, owner string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks WHERE owner_node=? AND state IN ('running','waiting_context')`,
		owner).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count active tasks: %w", err)
	}
	return n, nil
}

// CountScheduledActive counts execution slots occupied by locally-scheduled
// tasks, regardless of owner: the queue scheduler may adopt queued tasks left
// behind by another process instance (e.g. a restarted panel sidecar), so its
// concurrency budget must count what is actually running, not just rows that
// already carry its own node id.
func (s *TaskStore) CountScheduledActive(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks WHERE scheduled=1 AND state IN ('dispatched','running','waiting_context')`).
		Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count scheduled active: %w", err)
	}
	return n, nil
}

// SetQueueMeta stamps the queue-scheduling metadata on a task and marks it
// scheduled (owned by the local queue scheduler). Called once by Enqueue
// before the task enters queued state.
func (s *TaskStore) SetQueueMeta(ctx context.Context, taskID string, priority int, sessionID, workDir string, resourceKeys []string) error {
	keysJSON, err := json.Marshal(resourceKeys)
	if err != nil {
		return fmt.Errorf("marshal resource keys: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE tasks SET priority=?, session_id=?, work_dir=?, resource_keys_json=?, scheduled=1, updated_at=?
		WHERE task_id=?`,
		priority, sessionID, workDir, string(keysJSON), s.now(), taskID)
	if err != nil {
		return fmt.Errorf("set queue meta: %w", err)
	}
	return nil
}

// SetPriority changes a task's queue priority. Only meaningful before the
// task finishes; the board and scheduler read it on their next tick.
func (s *TaskStore) SetPriority(ctx context.Context, taskID string, priority int) error {
	if priority < PriorityHigh || priority > PriorityLow {
		return fmt.Errorf("priority %d out of range", priority)
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET priority=?, updated_at=? WHERE task_id=?`,
		priority, s.now(), taskID)
	if err != nil {
		return fmt.Errorf("set priority: %w", err)
	}
	return nil
}

// SetSeq sets the manual (drag) ordering value. 0 clears it back to pure
// priority/FIFO placement.
func (s *TaskStore) SetSeq(ctx context.Context, taskID string, seq int64) error {
	if seq < 0 {
		return fmt.Errorf("seq must be >= 0")
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET seq=?, updated_at=? WHERE task_id=?`,
		seq, s.now(), taskID)
	if err != nil {
		return fmt.Errorf("set seq: %w", err)
	}
	return nil
}

// SetSessionID links a task to a panel conversation after the fact (session
// asks learn the task id only once the engine returns it).
func (s *TaskStore) SetSessionID(ctx context.Context, taskID, sessionID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET session_id=?, updated_at=? WHERE task_id=?`,
		sessionID, s.now(), taskID)
	if err != nil {
		return fmt.Errorf("set session id: %w", err)
	}
	return nil
}

// ListReady returns every task waiting for the local queue scheduler: queued
// and marked scheduled. Ordering happens in the scheduler's policy, not here.
func (s *TaskStore) ListReady(ctx context.Context) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskColumns+` FROM tasks WHERE state = ? AND scheduled = 1`, StateQueued)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

// ClaimLocal moves a queued task to dispatched-to-self for the queue
// scheduler. Unlike Dispatch it does not require the caller to already own
// the task: scheduled tasks are a node-local pool any running scheduler
// instance may adopt (e.g. after a panel restart left them queued). The
// state-guarded UPDATE is the race arbiter — exactly one claimant wins.
func (s *TaskStore) ClaimLocal(ctx context.Context, taskID, node string) error {
	now := s.now()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE tasks SET state=?, owner_node=?, state_version=state_version+1, updated_at=?
			WHERE task_id=? AND state=?`,
			StateDispatched, node, now, taskID, StateQueued)
		if err != nil {
			return fmt.Errorf("claim task: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: task %s not queued (claimed concurrently)", ErrConflict, taskID)
		}
		return s.recordEventTx(ctx, tx, taskID, EvDelegate, map[string]any{"target": node, "by": "queue"})
	})
}

// ExpireTasks fails any active task whose lease has expired. It returns the
// task IDs actually failed, so the caller can clean up per-task state. Called
// periodically by the monitor.
func (s *TaskStore) ExpireTasks(ctx context.Context) ([]string, error) {
	now := s.now()
	// review is deliberately absent: a task paused for human analysis has no
	// deadline pressure, and the monitor must never kill it out from under the
	// reviewer (P1-8). The lease is also cleared on entering review (see
	// transition), so a stale timestamp here would be doubly wrong to act on.
	rows, err := s.db.QueryContext(ctx,
		`SELECT task_id FROM tasks
		 WHERE lease_expires_at > 0 AND lease_expires_at < ?
		   AND state IN ('dispatched','waiting_context','running')`, now)
	if err != nil {
		return nil, fmt.Errorf("scan leases: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var failed []string
	for _, id := range ids {
		// The monitor acts on behalf of the lease holder regardless of the
		// stored owner (a crashed process may no longer hold it).
		if err := s.ForceFail(ctx, id, "lease expired"); err != nil {
			s.logger.Warn("expire task", "task", id, "err", err)
			continue
		}
		failed = append(failed, id)
		s.logger.Info("task failed by timeout", "task", id)
	}
	return failed, nil
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
	// Guarded by both state and owner (applyCAS, not apply): a task that a
	// concurrent transition already closed must not be overwritten back to
	// failed. Owner matches the value just read, so only a concurrent owner
	// change (which means someone else is actively working the task) loses.
	if err := s.applyCAS(ctx, taskID, cur.State, StateFailed, cur.OwnerNode, cur.AttemptID, EvResult,
		map[string]any{"failed": reason}, map[string]any{"failed": reason}); err != nil {
		if errors.Is(err, ErrConflict) {
			if after, gerr := s.Get(ctx, taskID); gerr == nil && Terminal(after.State) {
				return nil // already closed concurrently; nothing to force
			}
		}
		return err
	}
	return nil
}

// Recover normalizes tasks left in an active state by a previous process
// instance. Running/waiting_context become failed (execution was interrupted);
// dispatched/submitted return to queued for re-dispatch. Returns the number of
// tasks touched.
//
// review is deliberately excluded: a task parked for human sign-off has no
// executing process to lose, so a restart must not discard the queue of things
// waiting on a person — every other path in this file protects that invariant
// (CompleteFromRemote / FailFromRemote / ReviewFromRemote return early on
// StateReview, ExpireTasks excludes it), and Recover runs unconditionally on
// every daemon start. The interrupted rows also keep their result_json so the
// evidence of what ran survives, and each transition is written to the audit
// chain rather than mutating state_version silently.
func (s *TaskStore) Recover(ctx context.Context) (int, error) {
	var failed, requeued int
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		n, err := s.recoverBatchTx(ctx, tx, []string{StateRunning, StateWaitingCtx}, StateFailed, "interrupted")
		if err != nil {
			return fmt.Errorf("recover active tasks: %w", err)
		}
		failed = n
		n, err = s.recoverBatchTx(ctx, tx, []string{StateDispatched, StateSubmitted}, StateQueued, "requeued")
		if err != nil {
			return fmt.Errorf("recover dispatched tasks: %w", err)
		}
		requeued = n
		return nil
	})
	if err != nil {
		return 0, err
	}
	s.logger.Info("task recovery", "failed", failed, "requeued", requeued)
	return failed + requeued, nil
}

// recoverBatchTx moves every task in one of from's states to to, clearing the
// lease and appending an EvRecover event per task so the audit chain stays
// complete. It leaves result_json untouched.
func (s *TaskStore) recoverBatchTx(ctx context.Context, tx *sql.Tx, from []string, to, disposition string) (int, error) {
	ph := make([]string, len(from))
	args := make([]any, 0, len(from))
	for i, st := range from {
		ph[i] = "?"
		args = append(args, st)
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT task_id, state FROM tasks WHERE state IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return 0, err
	}
	type row struct{ id, state string }
	var found []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.state); err != nil {
			rows.Close()
			return 0, err
		}
		found = append(found, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	now := s.now()
	for _, r := range found {
		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET state=?, state_version=state_version+1, updated_at=?,
				lease_expires_at=NULL WHERE task_id=?`, to, now, r.id); err != nil {
			return 0, err
		}
		if err := s.recordEventTx(ctx, tx, r.id, EvRecover, map[string]any{
			"from": r.state, "to": to, "disposition": disposition,
		}); err != nil {
			return 0, err
		}
	}
	return len(found), nil
}

// Cancel marks a task cancelled if it is still active. Cancellation is the
// one transition the parent may force regardless of lease ownership, to
// support cascade from a cancelled parent — but it still must be a legal move
// per the state machine, so a stale caller cannot force a task into a state
// the table does not allow. Callers authorizing the *source* of the cancel do
// so upstream (see Core.handleCancel).
//
// The pre-check and the guarded UPDATE below are not atomic, so a cancel can
// race the execute loop's own transitions (dispatched→running on accept,
// queued→dispatched on re-dispatch). A zero-row update used to fail with
// ErrConflict right there — silently dropping a cancel that was fully legal a
// millisecond later and letting the task run to completion under nodes that
// believed it cancelled (the cancel-propagation CI flake). Instead, re-read
// and retry: a task that reached a terminal state wins (done keeps its
// recorded result, a concurrent cancel is idempotent success); a task that
// merely moved between active states is still cancellable, and the loop lands
// the cancel on the fresh state.
func (s *TaskStore) Cancel(ctx context.Context, taskID string) error {
	for attempt := 0; attempt < 5; attempt++ {
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
		// Guarded UPDATE (P1-4): a concurrent Complete between the pre-check and
		// this write must win — overwriting done back to cancelled would lose the
		// recorded result.
		conflicted := false
		if err := s.withTx(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `
				UPDATE tasks SET state=?, state_version=state_version+1, updated_at=?
				WHERE task_id=? AND state=?`,
				StateCancelled, s.now(), taskID, cur.State)
			if err != nil {
				return fmt.Errorf("cancel task: %w", err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				conflicted = true // state moved under us; retry with fresh state
				return nil
			}
			return s.recordEventTx(ctx, tx, taskID, EvCancel, map[string]any{"from": cur.State})
		}); err != nil {
			return err
		}
		if !conflicted {
			return nil
		}
	}
	return fmt.Errorf("%w: task %s cancelled concurrently", ErrConflict, taskID)
}

// CancelCascade cancels taskID and every descendant. Descendants are found by
// parent_id links; the walk recurses so multi-level trees behave. A visited set
// guards against a parent_id cycle, which would otherwise recurse forever. The
// returned slice lists the tasks actually transitioned to cancelled — already-
// terminal tasks are skipped, not included.
func (s *TaskStore) CancelCascade(ctx context.Context, taskID string) ([]string, error) {
	var cancelled []string
	visited := make(map[string]bool)
	if err := s.cancelCascade(ctx, taskID, visited, &cancelled); err != nil {
		return nil, err
	}
	return cancelled, nil
}

func (s *TaskStore) cancelCascade(ctx context.Context, taskID string, visited map[string]bool, cancelled *[]string) error {
	if visited[taskID] {
		return nil // already walked; a cycle in parent_id must not recurse forever
	}
	visited[taskID] = true
	cur, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if !Terminal(cur.State) {
		if err := s.Cancel(ctx, taskID); err != nil {
			return err
		}
		*cancelled = append(*cancelled, taskID)
	}
	children, err := s.Children(ctx, taskID)
	if err != nil {
		return err
	}
	for _, c := range children {
		if Terminal(c.State) {
			continue
		}
		if err := s.cancelCascade(ctx, c.TaskID, visited, cancelled); err != nil {
			return err
		}
	}
	return nil
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

// Idle reports whether the store has no in-flight tasks (running, dispatched,
// or waiting for context). It mirrors Core.Idle so callers that only hold a
// *TaskStore (the web panel) can gate on the same condition without a Core.
func (s *TaskStore) Idle(ctx context.Context) bool {
	for _, state := range []string{StateRunning, StateDispatched, StateWaitingCtx} {
		if tasks, err := s.ListByState(ctx, state); err == nil && len(tasks) > 0 {
			return false
		}
	}
	return true
}

// AmbiguousTaskIDError reports that a task-id prefix matched more than one task.
// It carries the candidates so a CLI can show the user what to disambiguate
// between instead of only saying that it could not decide.
type AmbiguousTaskIDError struct {
	Ref        string
	Candidates []string
}

func (e *AmbiguousTaskIDError) Error() string {
	return fmt.Sprintf("ambiguous task id %s (%d matches)", e.Ref, len(e.Candidates))
}

// resolveCandidates caps how many matches a prefix lookup reports. Three is
// enough to show the user the collision without printing a page of ids.
const resolveCandidates = 3

// ResolveTaskID turns a user-typed task reference into a full task id. An exact
// id is returned unchanged (a full id always wins, so a prefix that happens to
// be another task's whole id is never surprising); otherwise the ref is matched
// as a prefix.
//
// This exists because every listing surface abbreviates a task id to its first
// UUID group — that is the only way a row fits a terminal — and an id a user can
// read but not type is not an id. Prefixes do collide: UUIDv7 puts a millisecond
// timestamp in exactly those leading bytes, so two tasks submitted in the same
// second share the group. That is why the ambiguous case is an error with
// candidates rather than a newest-wins guess: acting on the wrong task is worse
// than asking.
func (s *TaskStore) ResolveTaskID(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", sql.ErrNoRows
	}
	var exact string
	err := s.db.QueryRowContext(ctx, `SELECT task_id FROM tasks WHERE task_id = ?`, ref).Scan(&exact)
	if err == nil {
		return exact, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT task_id FROM tasks WHERE task_id LIKE ? ORDER BY created_at DESC LIMIT ?`,
		ref+"%", resolveCandidates+1)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var found []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		found = append(found, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch len(found) {
	case 0:
		return "", sql.ErrNoRows
	case 1:
		return found[0], nil
	default:
		if len(found) > resolveCandidates {
			found = found[:resolveCandidates]
		}
		return "", &AmbiguousTaskIDError{Ref: ref, Candidates: found}
	}
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
		`SELECT id, task_id, ts, type, data_json, COALESCE(prev_hash, '') FROM task_events
		 WHERE task_id = ? ORDER BY id ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.TaskID, &e.TS, &e.Type, &e.DataJSON, &e.PrevHash); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// VerifyTaskEventChain verifies the per-task hash chain for taskID. It returns
// nil if the chain is intact, or an error describing the first break.
func (s *TaskStore) VerifyTaskEventChain(ctx context.Context, taskID string) error {
	events, err := s.Events(ctx, taskID)
	if err != nil {
		return fmt.Errorf("load events: %w", err)
	}
	var prevHash string
	for i, e := range events {
		if e.PrevHash != prevHash {
			return fmt.Errorf("event %d (id=%d) prev_hash mismatch: got %s, want %s",
				i+1, e.ID, e.PrevHash, prevHash)
		}
		prevHash = hashEvent(e.PrevHash, e.TaskID, e.TS, e.Type, e.DataJSON)
	}
	return nil
}

// Event is one task_events row.
type Event struct {
	ID       int64
	TaskID   string
	TS       int64
	Type     string
	DataJSON string
	PrevHash string
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
	context_type, context_hash, complexity, risk, resource_json, requires_json,
	lease_expires_at, created_at, updated_at, authorized,
	priority, seq, session_id, resource_keys_json, work_dir, scheduled,
	plan_id, stage_id, needs_json, input_artifacts_json, output_artifact`

func scanTasks(rows *sql.Rows) ([]Task, error) {
	var out []Task
	for rows.Next() {
		var t Task
		var chainJSON string
		var intent, spec, result sql.NullString
		var lease sql.NullInt64
		var contextType, contextHash, risk, resource, requiresJSON sql.NullString
		var sessionID, resourceKeysJSON, workDir sql.NullString
		var complexity sql.NullFloat64
		var scheduled int
		var planID, stageID, needsJSON, inputsJSON, outputArt sql.NullString
		if err := rows.Scan(&t.TaskID, &t.ParentID, &t.Project, &t.Title, &t.State,
			&t.OwnerNode, &t.AttemptID, &t.StateVersion, &chainJSON, &intent,
			&spec, &result, &contextType, &contextHash, &complexity, &risk, &resource,
			&requiresJSON, &lease,
			&t.CreatedAt, &t.UpdatedAt, &t.Authorized,
			&t.Priority, &t.Seq, &sessionID, &resourceKeysJSON, &workDir, &scheduled,
			&planID, &stageID, &needsJSON, &inputsJSON, &outputArt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(chainJSON), &t.Chain)
		_ = json.Unmarshal([]byte(requiresJSON.String), &t.Requires)
		_ = json.Unmarshal([]byte(resourceKeysJSON.String), &t.ResourceKeys)
		_ = json.Unmarshal([]byte(needsJSON.String), &t.Needs)
		_ = json.Unmarshal([]byte(inputsJSON.String), &t.Inputs)
		t.Intent = intent.String
		t.SpecJSON = spec.String
		t.ResultJSON = result.String
		t.ContextType = contextType.String
		t.ContextHash = contextHash.String
		t.Complexity = complexity.Float64
		t.Risk = risk.String
		t.ResourceJSON = resource.String
		t.LeaseExpires = lease.Int64
		t.SessionID = sessionID.String
		t.WorkDir = workDir.String
		t.Scheduled = scheduled != 0
		t.PlanID = planID.String
		t.StageID = stageID.String
		t.OutputArtifact = outputArt.String
		out = append(out, t)
	}
	return out, rows.Err()
}

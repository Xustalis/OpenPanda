// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/todos"
)

// ExecutionRequest identifies business work independently of kernel attempts.
// A retry reuses RequestID and TriggerKey. A deliberate rerun supplies new ones.
// These identifiers confer no operation authorization.
type ExecutionRequest struct {
	RequestID     string `json:"requestId"`
	ConnectionID  string `json:"connectionId"`
	AccountID     string `json:"accountId"`
	TaskKey       string `json:"taskKey"`
	TriggerKey    string `json:"triggerKey"`
	InputRevision string `json:"inputRevision"`
	Purpose       string `json:"purpose"`
	LocalTodoID   int64  `json:"localTodoId,omitempty"`
	ApprovalMode  string `json:"approvalMode"`
}

type EnqueueResult struct {
	Task           Task   `json:"task"`
	RequestID      string `json:"requestId"`
	AlreadyStarted bool   `json:"alreadyStarted"`
}

var ErrExecutionUnavailable = errors.New("historical execution is unavailable")

type reservedExecution struct {
	ExecutionRequest
	State, TaskID, PlanID string
}

type queuedRequestPayload struct {
	Input TaskInput
	Queue QueueSpec
}

// reserveExecution commits intent before filesystem/context/Plan preparation.
// A failed publication leaves only a recoverable request, never a runnable row.
func (s *TaskStore) reserveExecution(ctx context.Context, req ExecutionRequest, payload any, kind string, replay any) (reservedExecution, error) {
	if req.RequestID == "" || req.TaskKey == "" || req.TriggerKey == "" || req.Purpose == "" || req.LocalTodoID < 0 {
		return reservedExecution{}, errors.New("incomplete execution request")
	}
	if req.ApprovalMode == "" {
		req.ApprovalMode = "before_submit"
	}
	if req.ApprovalMode != "before_submit" && req.ApprovalMode != "after_execution" {
		return reservedExecution{}, errors.New("invalid business approval mode")
	}
	if req.LocalTodoID > 0 && (req.ConnectionID != "" || req.AccountID != "" || req.TaskKey != fmt.Sprintf("local/local/local/%d", req.LocalTodoID)) {
		return reservedExecution{}, errors.New("local task identity does not match request")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return reservedExecution{}, err
	}
	h := sha256.Sum256(b)
	hash := hex.EncodeToString(h[:])
	replayJSON, err := json.Marshal(replay)
	if err != nil {
		return reservedExecution{}, err
	}
	var result reservedExecution
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		now := s.now()
		_, err := tx.ExecContext(ctx, `INSERT INTO execution_requests
 (request_id,connection_id,account_id,task_key,trigger_key,input_revision,purpose,payload_hash,local_todo_id,approval_mode,created_at,updated_at,payload_kind,payload_json)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
			req.RequestID, req.ConnectionID, req.AccountID, req.TaskKey, req.TriggerKey, req.InputRevision, req.Purpose, hash, req.LocalTodoID, req.ApprovalMode, now, now, kind, string(replayJSON))
		if err != nil {
			return err
		}
		var storedHash string
		err = tx.QueryRowContext(ctx, `SELECT request_id,connection_id,account_id,task_key,trigger_key,input_revision,purpose,local_todo_id,approval_mode,state,task_id,plan_id,payload_hash
 FROM execution_requests WHERE request_id=? OR (connection_id=? AND account_id=? AND task_key=? AND trigger_key=?) ORDER BY request_id`,
			req.RequestID, req.ConnectionID, req.AccountID, req.TaskKey, req.TriggerKey).Scan(
			&result.RequestID, &result.ConnectionID, &result.AccountID, &result.TaskKey, &result.TriggerKey, &result.InputRevision, &result.Purpose,
			&result.LocalTodoID, &result.ApprovalMode, &result.State, &result.TaskID, &result.PlanID, &storedHash)
		if err != nil {
			return err
		}
		if result.ConnectionID != req.ConnectionID || result.AccountID != req.AccountID || result.TaskKey != req.TaskKey || result.TriggerKey != req.TriggerKey || result.InputRevision != req.InputRevision || result.Purpose != req.Purpose || result.LocalTodoID != req.LocalTodoID || result.ApprovalMode != req.ApprovalMode || storedHash != hash {
			return fmt.Errorf("%w: execution request reused with different input", ErrConflict)
		}
		// A caller cannot use the id of one request and trigger of another.
		if result.RequestID != req.RequestID {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM execution_requests WHERE request_id=?`, req.RequestID).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				return ErrConflict
			}
		}
		// An old hash-only reservation can gain recovery input only after the
		// original identity and payload have been checked above.
		_, err = tx.ExecContext(ctx, `UPDATE execution_requests SET payload_kind=?,payload_json=? WHERE request_id=? AND payload_json='' AND state='preparing'`, kind, string(replayJSON), result.RequestID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE execution_requests SET recovery_allowed=1,preparation_error='' WHERE request_id=? AND state='preparing'`, result.RequestID)
		if err != nil {
			return err
		}
		return nil
	})
	return result, err
}

// requestPublishedTx repeats the idempotency check while holding the write
// lock: two callers may both have prepared before either publishes.
func (s *TaskStore) requestPublishedTx(ctx context.Context, tx *sql.Tx, req *ExecutionRequest) (taskID, planID string, err error) {
	if req == nil {
		return "", "", nil
	}
	var state string
	err = tx.QueryRowContext(ctx, `SELECT state,task_id,plan_id FROM execution_requests WHERE request_id=?`, req.RequestID).Scan(&state, &taskID, &planID)
	if err != nil {
		return "", "", err
	}
	if state == "published" {
		return taskID, planID, nil
	}
	if state != "preparing" {
		return "", "", ErrExecutionUnavailable
	}
	return "", "", nil
}

func (s *TaskStore) bindExecutionTx(ctx context.Context, tx *sql.Tx, req *ExecutionRequest, tasks []Task, planID string) error {
	if req == nil {
		return nil
	}
	if len(tasks) == 0 {
		return errors.New("cannot publish an empty execution")
	}
	now := s.now()
	if req.ConnectionID != "" {
		var valid int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_flow_runs f
 JOIN task_connections c ON c.connection_id=f.connection_id AND c.account_id=f.account_id
 JOIN task_policies p ON p.connection_id=f.connection_id AND p.account_id=f.account_id
 AND p.space_id=json_extract(f.snapshot_json,'$.key.spaceId') AND p.policy_json=f.policy_json
 JOIN task_snapshots s ON s.connection_id=f.connection_id AND s.account_id=f.account_id AND s.task_key=f.task_key
 WHERE f.request_id=? AND f.connection_id=? AND f.account_id=? AND f.task_key=?
 AND f.trigger_key=? AND f.input_revision=? AND f.state='leased' AND f.deadline>?
 AND f.lease_owner!='' AND c.state='connected' AND c.auth_epoch=f.auth_epoch
	AND c.generation=f.connection_generation AND s.auth_epoch=f.auth_epoch
	AND json_extract(s.snapshot_json,'$.inputRevision')=f.input_revision
	AND json_extract(s.snapshot_json,'$.assignmentRevision')=f.assignment_revision
	AND json_extract(s.snapshot_json,'$.automationGeneration')=f.automation_generation
	AND json_extract(p.policy_json,'$.execute')=1 AND json_extract(p.policy_json,'$.expiresAt')>?`, req.RequestID, req.ConnectionID, req.AccountID,
			req.TaskKey, req.TriggerKey, req.InputRevision, now, now).Scan(&valid); err != nil {
			return err
		}
		if valid != 1 {
			return fmt.Errorf("%w: remote execution has no current lease", ErrConflict)
		}
	}
	// The publication lock also protects the business-task slot. Distinct
	// request IDs (including explicit reruns) must not publish overlapping work.
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM execution_requests r
 JOIN execution_links l ON l.request_id=r.request_id JOIN tasks t ON t.task_id=l.task_id
 WHERE r.connection_id=? AND r.account_id=? AND r.task_key=? AND r.request_id<>?
 AND t.state NOT IN ('done','cancelled','expired')`,
		req.ConnectionID, req.AccountID, req.TaskKey, req.RequestID).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return fmt.Errorf("%w: business task already has an active execution", ErrConflict)
	}
	if req.LocalTodoID > 0 {
		todo, err := todos.ReadTx(ctx, tx, req.LocalTodoID)
		if err != nil {
			return err
		}
		if req.InputRevision != todos.ExecutionRevision(todo) {
			return fmt.Errorf("%w: local task changed during preparation", ErrConflict)
		}
		if todo.TaskID != "" && req.Purpose != "rerun" {
			return fmt.Errorf("%w: local task already has an execution", ErrConflict)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE todos SET task_id=?,status='running',started_at=CASE WHEN started_at=0 THEN ? ELSE started_at END,done_at=0,updated_at=? WHERE id=?`,
			tasks[0].TaskID, now, now, req.LocalTodoID); err != nil {
			return err
		}
	}
	for _, t := range tasks {
		role := "root"
		if planID != "" {
			role = "stage"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO execution_links(request_id,task_id,plan_id,stage_id,role,created_at) VALUES(?,?,?,?,?,?)`,
			req.RequestID, t.TaskID, planID, t.StageID, role, now); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE execution_requests SET state='published',task_id=?,plan_id=?,updated_at=? WHERE request_id=? AND state='preparing'`,
		tasks[0].TaskID, planID, now, req.RequestID)
	return err
}

// EnqueueRequest returns the original execution when the same logical request
// is retried. Preparation performs no task-row writes; publication is atomic.
func (c *Core) EnqueueRequest(ctx context.Context, req ExecutionRequest, in TaskInput, q QueueSpec) (EnqueueResult, error) {
	if req.ConnectionID != "" && (req.AccountID == "" || in.Authorized || q.Authorized) {
		return EnqueueResult{}, fmt.Errorf("%w: business assignment does not grant unrestricted operations", ErrConflict)
	}
	// Conversation IDs are display associations, not execution identity.
	identityInput, identityQueue := in, q
	identityInput.SessionID, identityQueue.SessionID = "", ""
	r, err := c.store.reserveExecution(ctx, req, queuedRequestPayload{identityInput, identityQueue}, "task", queuedRequestPayload{in, q})
	if err != nil {
		return EnqueueResult{}, err
	}
	if r.State == "published" {
		if r.PlanID != "" {
			return EnqueueResult{}, fmt.Errorf("%w: request already bound to a Plan", ErrConflict)
		}
		t, err := c.store.Get(ctx, r.TaskID)
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrExecutionUnavailable
		}
		return EnqueueResult{Task: t, RequestID: r.RequestID, AlreadyStarted: true}, err
	}
	if r.State != "preparing" {
		return EnqueueResult{}, ErrExecutionUnavailable
	}
	result, err := c.enqueueAtomic(ctx, in, q, &r.ExecutionRequest)
	if errors.Is(err, ErrConflict) {
		// An explicitly rejected overlapping or stale request must never turn
		// into a new run merely because the process later restarts. The user
		// can still explicitly resubmit that same request after resolving it.
		if _, recordErr := c.db.ExecContext(ctx, `UPDATE execution_requests SET recovery_allowed=0,preparation_error=? WHERE request_id=? AND state='preparing'`, err.Error(), r.RequestID); recordErr != nil {
			return result, errors.Join(err, recordErr)
		}
	}
	return result, err
}

func (c *Core) prepareQueuedTask(ctx context.Context, in TaskInput, q QueueSpec) (Task, error) {
	if q.Priority < PriorityHigh || q.Priority > PriorityLow {
		return Task{}, fmt.Errorf("queue priority %d out of range", q.Priority)
	}
	if in.WorkDir != "" && q.WorkDir != "" && in.WorkDir != q.WorkDir {
		return Task{}, errors.New("queue and task work directories differ")
	}
	if q.WorkDir == "" {
		q.WorkDir = in.WorkDir
	}
	in.WorkDir, in.SessionID = q.WorkDir, q.SessionID
	if q.WorkDir != "" {
		if err := projects.ValidateWorkDir(q.WorkDir); err != nil {
			return Task{}, err
		}
	}
	id, err := newUUID()
	if err != nil {
		return Task{}, err
	}
	attempt, err := newUUID()
	if err != nil {
		return Task{}, err
	}
	hash, _, err := c.packContext(ctx, in)
	if err != nil {
		return Task{}, fmt.Errorf("pack context: %w", err)
	}
	d := in.detail()
	t := Task{TaskID: id, ParentID: in.ParentID, Project: in.Project, Title: in.Title,
		State: StateQueued, StateVersion: 1, OwnerNode: c.nodeID, AttemptID: attempt, Chain: []string{c.nodeID},
		Intent: d.Intent, SpecJSON: d.SpecJSON, ContextType: d.ContextType, ContextHash: hash,
		Complexity: d.Complexity, Risk: d.Risk, ResourceJSON: d.ResourceJSON, Requires: d.Requires,
		Authorized: in.Authorized, UserLocale: d.UserLocale, Transport: d.Transport, DeadlineUnix: d.DeadlineUnix,
		Priority: q.Priority, SessionID: q.SessionID, WorkDir: q.WorkDir, Scheduled: true}
	if in.Project != "" {
		t.ScopePaths, t.Isolate = normalizeScopePaths(in.Paths), in.Isolate
	}
	if len(q.ResourceKeys) != 0 {
		t.ResourceKeys = mergeActuatorKeys(q.ResourceKeys, in.Requires, in.SpecJSON)
	} else if in.Project != "" && (len(t.ScopePaths) > 0 || in.Isolate) {
		t.ResourceKeys = childResourceKeys(id, in)
	} else {
		t.ResourceKeys = c.resourceKeysFor(in)
	}
	if in.ParentID == "" {
		c.mu.RLock()
		t.TokenBudget = max(0, c.taskTokenBudget)
		c.mu.RUnlock()
	}
	return t, nil
}

func (c *Core) enqueueAtomic(ctx context.Context, in TaskInput, q QueueSpec, req *ExecutionRequest) (EnqueueResult, error) {
	t, err := c.prepareQueuedTask(ctx, in, q)
	if err != nil {
		return EnqueueResult{}, err
	}
	result := EnqueueResult{Task: t}
	if req != nil {
		result.RequestID = req.RequestID
	}
	err = c.store.withTx(ctx, func(tx *sql.Tx) error {
		existing, planID, err := c.store.requestPublishedTx(ctx, tx, req)
		if err != nil {
			return err
		}
		if planID != "" {
			return ErrConflict
		}
		if existing != "" {
			result.Task.TaskID, result.AlreadyStarted = existing, true
			return nil
		}
		now := c.store.now()
		t.CreatedAt, t.UpdatedAt = now, now
		cols := []string{"task_id", "parent_id", "project", "title", "state", "owner_node", "attempt_id", "state_version", "chain_json", "remote", "created_at", "updated_at", "intent", "spec_json", "context_type", "context_hash", "complexity", "risk", "resource_json", "requires_json", "authorized", "token_budget", "priority", "session_id", "work_dir", "resource_keys_json", "scheduled", "scope_paths_json", "isolate", "transport", "deadline_unix"}
		encode := func(v any) string { b, _ := json.Marshal(v); return string(b) }
		values := []any{t.TaskID, t.ParentID, t.Project, t.Title, t.State, t.OwnerNode, t.AttemptID, t.StateVersion, encode(t.Chain), false, now, now, t.Intent, t.SpecJSON, t.ContextType, t.ContextHash, t.Complexity, t.Risk, t.ResourceJSON, encode(t.Requires), t.Authorized, t.TokenBudget, t.Priority, t.SessionID, t.WorkDir, encode(t.ResourceKeys), true, encode(t.ScopePaths), t.Isolate, t.Transport, t.DeadlineUnix}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tasks (`+strings.Join(cols, ",")+`) VALUES (`+strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")+`)`, values...); err != nil {
			return err
		}
		if sessionID, err := c.store.attachAskRoundTx(ctx, tx, t.TaskID); err != nil {
			return err
		} else if sessionID != "" {
			t.SessionID = sessionID
		}
		if err := c.store.recordEventTx(ctx, tx, t.TaskID, EvSubmit, map[string]any{"parent": t.ParentID, "owner": t.OwnerNode}); err != nil {
			return err
		}
		if in.ClassifyKind != "" {
			if err := c.store.recordEventTx(ctx, tx, t.TaskID, EvClassifyResult, map[string]any{"kind": in.ClassifyKind, "note": in.Title, "stages_count": 1}); err != nil {
				return err
			}
		}
		if err := c.store.recordEventTx(ctx, tx, t.TaskID, EvQueue, nil); err != nil {
			return err
		}
		if err := c.store.bindExecutionTx(ctx, tx, req, []Task{t}, ""); err != nil {
			return err
		}
		result.Task = t
		return nil
	})
	if err != nil {
		return EnqueueResult{}, err
	}
	if result.AlreadyStarted {
		result.Task, err = c.store.Get(ctx, result.Task.TaskID)
		return result, err
	}
	if c.projects != nil && t.Project != "" {
		if err := c.projects.Touch(t.Project); err != nil {
			c.logger.Debug("touch project recency", "err", err)
		}
	}
	notifyTaskCreated(ctx, result.Task)
	c.queueWake()
	return result, nil
}

// SPDX-License-Identifier: AGPL-3.0-or-later

// Package taskhub is the business view of work. Kernel task IDs remain execution
// records; local todos and account-scoped provider snapshots are its sources.
package taskhub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/plan"
	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/todos"
	"github.com/Xustalis/OpenPanda/internal/util"
)

type Store struct {
	DB    *sql.DB
	Todos *todos.Store
}

func New(db *sql.DB) *Store { return &Store{DB: db, Todos: todos.NewStore(db)} }

type Task struct {
	ConnectionID string                 `json:"connectionId"`
	AccountID    string                 `json:"accountId"`
	Snapshot     taskproviders.Snapshot `json:"snapshot"`
	Local        *todos.Todo            `json:"local,omitempty"`
}
type History struct {
	RequestID      string `json:"requestId"`
	TaskID         string `json:"taskId"`
	PlanID         string `json:"planId"`
	StageID        string `json:"stageId"`
	State          string `json:"state"`
	ExecutionState string `json:"executionState"`
	CreatedAt      int64  `json:"createdAt"`
}

func (s *Store) List(ctx context.Context, connection string) ([]Task, error) {
	out := []Task{}
	if connection == "" || connection == "local" {
		ts, err := s.Todos.List(ctx, todos.Filter{})
		if err != nil {
			return nil, err
		}
		for _, t := range ts {
			out = append(out, Task{ConnectionID: "local", AccountID: "local", Snapshot: taskproviders.LocalSnapshot(t), Local: &t})
		}
	}
	q := `SELECT s.connection_id,s.account_id,s.snapshot_json FROM task_snapshots s JOIN task_connections c ON c.connection_id=s.connection_id AND c.account_id=s.account_id AND c.auth_epoch=s.auth_epoch WHERE c.state='connected'`
	args := []any{}
	if connection != "" {
		q += ` AND s.connection_id=?`
		args = append(args, connection)
	}
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t Task
		var raw string
		if err = rows.Scan(&t.ConnectionID, &t.AccountID, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &t.Snapshot); err != nil {
			return nil, err
		}
		if t.Snapshot.Permissions.Read {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}
func (s *Store) Get(ctx context.Context, connection, key string) (Task, error) {
	k, err := taskproviders.ParseKey(key)
	if err != nil {
		return Task{}, err
	}
	if k.InstanceID == "local" && (connection == "" || connection == "local") {
		id, err := taskproviders.LocalID(k)
		if err != nil {
			return Task{}, err
		}
		t, err := s.Todos.Get(ctx, id)
		return Task{ConnectionID: "local", AccountID: "local", Snapshot: taskproviders.LocalSnapshot(t), Local: &t}, err
	}
	if connection == "" {
		return Task{}, errors.New("connection required for a remote task")
	}
	ts, err := s.List(ctx, connection)
	if err != nil {
		return Task{}, err
	}
	for _, t := range ts {
		if t.Snapshot.Key == k {
			return t, nil
		}
	}
	return Task{}, todos.ErrNotFound
}
func (s *Store) History(ctx context.Context, connection, key string) ([]History, error) {
	t, err := s.Get(ctx, connection, key)
	if err != nil {
		return nil, err
	}
	conn, account := t.ConnectionID, t.AccountID
	if conn == "local" {
		conn, account = "", ""
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT r.request_id,COALESCE(l.task_id,r.task_id),COALESCE(l.plan_id,r.plan_id),COALESCE(l.stage_id,''),r.state,COALESCE(t.state,'unavailable'),r.created_at FROM execution_requests r LEFT JOIN execution_links l ON l.request_id=r.request_id LEFT JOIN tasks t ON t.task_id=l.task_id WHERE r.connection_id=? AND r.account_id=? AND r.task_key=? ORDER BY r.created_at DESC,r.request_id,l.stage_id`, conn, account, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []History{}
	for rows.Next() {
		var h History
		if err = rows.Scan(&h.RequestID, &h.TaskID, &h.PlanID, &h.StageID, &h.State, &h.ExecutionState, &h.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

type Runner interface {
	EnqueueWork(context.Context, core.ExecutionRequest, core.TaskInput, core.QueueSpec) (core.EnqueueResult, string, error)
}
type PlanRunner interface {
	StartWorkPlan(context.Context, core.ExecutionRequest, plan.Plan, core.QueueSpec) (core.PlanRequestResult, string, error)
}
type RunOptions struct {
	RequestID    string     `json:"requestId"`
	Purpose      string     `json:"purpose"`
	ApprovalMode string     `json:"approvalMode"`
	Plan         *plan.Plan `json:"plan,omitempty"`
}
type RunResult struct {
	core.EnqueueResult
	Warning string      `json:"warning"`
	Todo    *todos.Todo `json:"todo,omitempty"`
	PlanID  string      `json:"planId,omitempty"`
}

// StartLocal is used by both compatibility todo start entrypoints and work run.
// The former returns its first execution; only an explicit rerun mints a new one.
func (s *Store) StartLocal(ctx context.Context, runner Runner, id int64, opts RunOptions) (RunResult, error) {
	t, err := s.Todos.Get(ctx, id)
	if err != nil {
		return RunResult{}, err
	}
	if opts.Purpose == "" {
		opts.Purpose = "manual"
	}
	if opts.Purpose != "manual" && opts.Purpose != "rerun" {
		return RunResult{}, errors.New("local execution purpose must be manual or rerun")
	}
	if t.TaskID != "" && opts.Purpose != "rerun" {
		task, err := core.NewTaskStore(s.DB, nil).Get(ctx, t.TaskID)
		if errors.Is(err, sql.ErrNoRows) {
			return RunResult{}, core.ErrExecutionUnavailable
		}
		if err != nil {
			return RunResult{}, err
		}
		var requestID, planID string
		err = s.DB.QueryRowContext(ctx, `SELECT r.request_id,r.plan_id FROM execution_requests r
 JOIN execution_links l ON l.request_id=r.request_id WHERE r.connection_id=''
 AND r.account_id='' AND r.task_key=? AND l.task_id=? ORDER BY r.created_at DESC,r.request_id LIMIT 1`,
			fmt.Sprintf("local/local/local/%d", id), t.TaskID).Scan(&requestID, &planID)
		if err != nil {
			return RunResult{}, core.ErrExecutionUnavailable
		}
		return RunResult{EnqueueResult: core.EnqueueResult{Task: task, RequestID: requestID, AlreadyStarted: true}, Todo: &t, PlanID: planID}, nil
	}
	if runner == nil {
		return RunResult{}, errors.New("task execution is not configured")
	}
	key := taskproviders.LocalSnapshot(t).Key.String()
	trigger := "todo:start"
	if opts.Purpose == "rerun" {
		if opts.RequestID == "" {
			opts.RequestID, err = util.UUIDv7()
			if err != nil {
				return RunResult{}, err
			}
		}
		trigger = "rerun:" + opts.RequestID
	}
	if opts.RequestID == "" {
		opts.RequestID = fmt.Sprintf("todo:start:%d", id)
	}
	q := core.DefaultQueueSpec()
	q.Project = t.Project
	switch t.Priority {
	case todos.PriorityHigh:
		q.Priority = core.PriorityHigh
	case todos.PriorityLow:
		q.Priority = core.PriorityLow
	}
	prompt := strings.TrimSpace(t.Description)
	if prompt == "" {
		prompt = t.Title
	}
	req := core.ExecutionRequest{RequestID: opts.RequestID, TaskKey: key, TriggerKey: trigger, InputRevision: todos.ExecutionRevision(t), Purpose: opts.Purpose, LocalTodoID: id, ApprovalMode: opts.ApprovalMode}
	var r core.EnqueueResult
	var warn, planID string
	if opts.Plan != nil {
		pr, ok := runner.(PlanRunner)
		if !ok {
			return RunResult{}, taskproviders.ErrUnsupported
		}
		result, warning, planErr := pr.StartWorkPlan(ctx, req, *opts.Plan, q)
		if planErr != nil {
			return RunResult{}, planErr
		}
		planID, warn = result.PlanID, warning
		var rootID string
		if err = s.DB.QueryRowContext(ctx, `SELECT task_id FROM execution_requests WHERE request_id=?`, result.RequestID).Scan(&rootID); err != nil {
			return RunResult{}, err
		}
		root, getErr := core.NewTaskStore(s.DB, nil).Get(ctx, rootID)
		if getErr != nil {
			return RunResult{}, getErr
		}
		r = core.EnqueueResult{Task: root, RequestID: result.RequestID, AlreadyStarted: result.AlreadyStarted}
	} else {
		r, warn, err = runner.EnqueueWork(ctx, req, core.TaskInput{Title: t.Title, Intent: prompt, Project: t.Project, Requires: []string{"coding"}}, q)
	}
	if err != nil {
		return RunResult{}, err
	}
	updated, err := s.Todos.Get(ctx, id)
	return RunResult{EnqueueResult: r, Warning: warn, Todo: &updated, PlanID: planID}, err
}

package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/xenith/panda/internal/bus"
)

// TaskInput is the local-entry form of a task: the structured task the entry
// model emitted, translated into the fields the core persists and executes. It
// mirrors the wire TaskDelegatePayload but originates locally rather than over
// the bus. The caller (cmd/panda) owns the translation from entry.TaskSpec.
type TaskInput struct {
	Title        string
	Project      string
	ContextType  string
	Intent       string
	SpecJSON     string
	Requires     []string
	Complexity   float64
	Risk         string
	ResourceJSON string
}

// detail folds the input into the persisted detail columns.
func (in TaskInput) detail() TaskDetail {
	return TaskDetail{
		ContextType:  in.ContextType,
		Intent:       in.Intent,
		SpecJSON:     in.SpecJSON,
		Complexity:   in.Complexity,
		Risk:         in.Risk,
		ResourceJSON: in.ResourceJSON,
	}
}

// SubmitLocal creates and executes a task on this node — no delegation. It is
// the Phase 1 local closed loop: the entry model's task output lands here, is
// persisted with full detail, then runs through the same execute pipeline used
// for inbound delegated tasks. It returns the final task row and the execution
// result payload.
//
// On an execution error the task is failed (not left queued) so a local task
// always ends in a terminal state. ErrCancelled is not an error to the caller:
// the task was cancelled mid-run and the result payload reflects that outcome.
func (c *Core) SubmitLocal(ctx context.Context, in TaskInput) (Task, bus.TaskResultPayload, error) {
	t, err := c.store.Create(ctx, "", in.Project, in.Title, c.nodeID, []string{c.nodeID})
	if err != nil {
		return Task{}, bus.TaskResultPayload{}, fmt.Errorf("create task: %w", err)
	}
	if err := c.store.SetDetail(ctx, t.TaskID, in.detail()); err != nil {
		return Task{}, bus.TaskResultPayload{}, fmt.Errorf("set detail: %w", err)
	}

	result, err := c.execute(ctx, t.TaskID, in.Intent, in.Requires)
	if err != nil && !errors.Is(err, ErrCancelled) {
		// Route/dispatch/accept failures would otherwise leave the task stuck
		// in a non-terminal state. Fail it so the queue reflects reality.
		if ferr := c.store.ForceFail(ctx, t.TaskID, err.Error()); ferr != nil {
			c.logger.Warn("fail local task", "task", t.TaskID, "err", ferr)
		}
		return t, result, err
	}

	final, err := c.store.Get(ctx, t.TaskID)
	if err != nil {
		return t, result, err
	}
	return final, result, nil
}

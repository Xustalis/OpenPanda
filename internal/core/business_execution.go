// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/entry"
)

// BusinessExecutor is an enforceable, tool-free preparation boundary. Remote
// business requests never fall through to unrestricted shell/agent routing.
// The owner installs it before starting the queue. Its implementation checks
// fresh authority and a cloud fence, and cancels on loss of either.
type BusinessExecutor interface {
	Prepare(context.Context, Task) (string, error)
}

// BusinessModel snapshots the current configured client. Its bounded auxiliary
// API preserves native credentials and cannot invoke execution tools.
func (c *Core) BusinessModel() *entry.Client { return c.currentSupervisor() }

func (c *Core) SetBusinessExecutor(executor BusinessExecutor) {
	c.mu.Lock()
	c.businessExecutor = executor
	c.mu.Unlock()
}
func (c *Core) isRemoteBusiness(ctx context.Context, taskID string) (bool, error) {
	var n int
	err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM execution_links l JOIN execution_requests r ON r.request_id=l.request_id WHERE l.task_id=? AND r.connection_id!=''`, taskID).Scan(&n)
	return n > 0, err
}
func (c *Core) runBusiness(ctx context.Context, taskID string) (out bus.TaskResultPayload, err error) {
	c.mu.RLock()
	executor := c.businessExecutor
	c.mu.RUnlock()
	if executor == nil {
		err = errors.New("business execution owner is unavailable")
		c.failLocal(ctx, taskID, err)
		return out, err
	}
	task, unlock, err := c.claimForRun(ctx, taskID, "", &commander.Plan{Kind: "business"}, func() {})
	if err != nil {
		return out, err
	}
	defer unlock()
	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer c.registerRunning(taskID, cancel)()
	defer c.renewLease(execCtx, taskID, task.Chain, task.AttemptID)()
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("business preparation interrupted")
			defer func() { panic(p) }()
		}
		if err != nil {
			writeCtx, done := c.storeWriteCtx(ctx)
			defer done()
			_ = c.store.FailAttempt(writeCtx, taskID, c.nodeID, task.AttemptID, err.Error())
		}
	}()
	text, err := executor.Prepare(execCtx, task)
	if err != nil {
		return out, err
	}
	out = bus.TaskResultPayload{TaskID: taskID, AttemptID: task.AttemptID, State: StateDone, Stdout: text, Executor: c.nodeID}
	// Kernel done records that preparation ended. Business completion is a
	// separate versioned delivery decision and cannot be inferred from this row.
	err = c.store.CompleteAttempt(execCtx, taskID, c.nodeID, task.AttemptID, out)
	return out, err
}

package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/xenith/panda/internal/bus"
)

// handleDelegate processes an incoming task_delegate. It creates the task in
// the local store and executes it via the commander (Phase 0) or declines if
// no capability matches.
func (c *Core) handleDelegate(ctx context.Context, env bus.Envelope) {
	var p bus.TaskDelegatePayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad task_delegate", "err", err, "from", env.From)
		c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{
			TaskID: p.TaskID, Reason: "bad payload",
		})
		return
	}
	if p.TaskID == "" {
		c.logger.Warn("task_delegate missing task_id", "from", env.From)
		return
	}

	// Idempotency: if we already know this task, do not re-create.
	if _, err := c.store.Get(ctx, p.TaskID); err == nil {
		c.logger.Info("duplicate task_delegate ignored", "task", p.TaskID, "msg", env.MsgID)
		return
	}

	chain := p.Chain
	if chain == nil {
		chain = []string{env.From}
	}
	chain = append(chain, c.nodeID)

	t, err := c.store.CreateWithID(ctx, p.TaskID, p.ParentID, p.Project, p.TitleOrDefault(), c.nodeID, chain)
	if err != nil {
		c.logger.Error("create task from delegate", "err", err)
		return
	}
	_ = t

	// Route: Phase 0 commander decides native vs agent. If no capability
	// matches, decline with a reason. On success, the result is sent back
	// to the delegator (env.From).
	result, err := c.routeDelegated(ctx, t.TaskID, p, chain)
	if err != nil {
		c.logger.Warn("route delegated task", "err", err, "task", t.TaskID)
		c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{
			TaskID: t.TaskID, Reason: err.Error(),
		})
		return
	}
	if err := c.reply(ctx, env, bus.MsgTaskResult, result); err != nil {
		c.logger.Warn("send task_result", "err", err, "task", t.TaskID)
	}
}

// routeDelegated executes the task via the commander. It matches the task's
// required abilities against this node's capability card and runs the result.
func (c *Core) routeDelegated(ctx context.Context, taskID string, p bus.TaskDelegatePayload, chain []string) (bus.TaskResultPayload, error) {
	// Record the task in the local store before execution so the queue
	// reflects it even if the process dies mid-run.
	if err := c.store.Queue(ctx, taskID, c.nodeID); err != nil {
		return bus.TaskResultPayload{}, fmt.Errorf("queue: %w", err)
	}

	if c.router == nil {
		// No capability card loaded: nothing to execute.
		return bus.TaskResultPayload{}, fmt.Errorf("no commander configured")
	}

	required := p.Requires
	if len(required) == 0 {
		required = []string{p.ContextType}
	}

	plan, err := c.router.Route(required)
	if err != nil {
		return bus.TaskResultPayload{}, fmt.Errorf("route: %w", err)
	}

	if err := c.store.Dispatch(ctx, taskID, c.nodeID, c.nodeID); err != nil {
		return bus.TaskResultPayload{}, fmt.Errorf("dispatch: %w", err)
	}
	if err := c.store.Accept(ctx, taskID, c.nodeID); err != nil {
		return bus.TaskResultPayload{}, fmt.Errorf("accept: %w", err)
	}

	res := c.router.Execute(ctx, plan, p.Intent, "")
	if res.NeedManual {
		// Manual tasks: notify and mark done; the human completes offline.
		if err := c.store.Complete(ctx, taskID, c.nodeID, map[string]any{
			"manual": true, "notify": res.Stdout,
		}); err != nil {
			return bus.TaskResultPayload{}, fmt.Errorf("complete manual: %w", err)
		}
		return bus.TaskResultPayload{TaskID: taskID, OK: true, ExitCode: 0, Stdout: res.Stdout}, nil
	}

	if !res.OK {
		if err := c.store.Fail(ctx, taskID, c.nodeID, res.Stderr); err != nil {
			return bus.TaskResultPayload{}, fmt.Errorf("fail: %w", err)
		}
		return bus.TaskResultPayload{
			TaskID: taskID, OK: false, ExitCode: res.ExitCode, Stderr: res.Stderr,
		}, nil
	}

	if err := c.store.Complete(ctx, taskID, c.nodeID, map[string]any{
		"ok": true, "exit_code": res.ExitCode, "stdout": res.Stdout,
	}); err != nil {
		return bus.TaskResultPayload{}, fmt.Errorf("complete: %w", err)
	}
	return bus.TaskResultPayload{
		TaskID: taskID, OK: true, ExitCode: res.ExitCode, Stdout: res.Stdout,
	}, nil
}

// handleAccept processes task_accept from the executor.
func (c *Core) handleAccept(ctx context.Context, env bus.Envelope) {
	var p bus.TaskAcceptPayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad task_accept", "err", err)
		return
	}
	// The parent transitions dispatched -> running locally; lease moves to
	// the executor (env.From). Only the current owner may call Accept on the
	// parent side, and here we're acting as the parent, so this call records
	// the executor's acceptance.
	t, err := c.store.Get(ctx, p.TaskID)
	if err != nil {
		c.logger.Warn("accept for unknown task", "task", p.TaskID)
		return
	}
	if t.State != StateDispatched {
		c.logger.Debug("accept ignored (not dispatched)", "task", p.TaskID, "state", t.State)
		return
	}
	// The executor already moved its own copy to running; the parent updates
	// its view so the queue shows running.
	_ = t
}

// handleDecline processes task_decline from an executor.
func (c *Core) handleDecline(ctx context.Context, env bus.Envelope) {
	var p bus.TaskDeclinePayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad task_decline", "err", err)
		return
	}
	c.logger.Info("task declined", "task", p.TaskID, "reason", p.Reason, "by", env.From)
	// Parent returns the task to queued for re-routing elsewhere.
	if err := c.store.Decline(ctx, p.TaskID, c.nodeID, p.Reason); err != nil {
		c.logger.Debug("decline apply failed", "task", p.TaskID, "err", err)
	}
}

// handleResult processes a task_result from an executor. Idempotency: a
// result for a task we no longer own, or for a stale attempt, is ignored.
func (c *Core) handleResult(ctx context.Context, env bus.Envelope) {
	var p bus.TaskResultPayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad task_result", "err", err)
		return
	}
	t, err := c.store.Get(ctx, p.TaskID)
	if errors.Is(err, sql.ErrNoRows) {
		// The delegator may not have a local row if it delegated without
		// creating one (Phase 0 entry flow). Reconstruct a minimal row so
		// the queue reflects the outcome.
		if _, err := c.store.CreateWithID(ctx, p.TaskID, "", "", p.TaskID, c.nodeID, []string{c.nodeID, env.From}); err != nil {
			c.logger.Warn("create task from result", "task", p.TaskID, "err", err)
			return
		}
		t, _ = c.store.Get(ctx, p.TaskID)
	} else if err != nil {
		c.logger.Warn("load task for result", "task", p.TaskID, "err", err)
		return
	}
	if p.AttemptID != "" && t.AttemptID != "" && t.AttemptID != p.AttemptID {
		c.logger.Info("stale attempt result ignored", "task", p.TaskID,
			"stored", t.AttemptID, "got", p.AttemptID)
		return
	}
	if p.OK {
		if err := c.store.CompleteFromRemote(ctx, p.TaskID, c.nodeID, p); err != nil {
			c.logger.Warn("complete from result", "task", p.TaskID, "err", err)
		}
	} else {
		if err := c.store.FailFromRemote(ctx, p.TaskID, c.nodeID, p.Stderr); err != nil {
			c.logger.Warn("fail from result", "task", p.TaskID, "err", err)
		}
	}
}

// handleCancel processes a task_cancel request.
func (c *Core) handleCancel(ctx context.Context, env bus.Envelope) {
	var p bus.TaskCancelPayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad task_cancel", "err", err)
		return
	}
	if _, err := c.store.CancelCascade(ctx, p.TaskID); err != nil {
		c.logger.Warn("cancel failed", "task", p.TaskID, "err", err)
	}
}

// reply sends a message back to the sender of env.
func (c *Core) reply(ctx context.Context, env bus.Envelope, typ string, payload any) error {
	envOut, err := bus.NewEnvelope(typ, c.nodeID, mustUUID(), payload)
	if err != nil {
		return err
	}
	envOut.To = env.From
	conn := c.connFor(env.From)
	if conn == nil {
		// No return channel for this peer (e.g. it just disconnected);
		// the result will be recovered via a later heartbeat/sync.
		c.logger.Warn("no peer connection to reply", "peer", env.From, "type", typ)
		return errors.New("no peer")
	}
	return conn.Send(envOut)
}

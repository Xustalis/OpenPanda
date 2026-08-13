package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/xenith/panda/internal/bus"
	"github.com/xenith/panda/internal/ledger"
	"github.com/xenith/panda/internal/scheduler"
)

// handleDelegate processes an incoming task_delegate. It decides where the
// task runs: locally (Phase 0 behavior), forwarded to a capable peer (P2P
// per-edge delegation), or declined when nothing in the known network matches.
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

	// Append this node to the delegation chain. Revisiting a node means a
	// routing loop, which we reject instead of echoing around forever.
	chain := p.Chain
	if chain == nil {
		chain = []string{env.From}
	}
	chain, err := scheduler.AppendChain(chain, c.nodeID)
	if err != nil {
		c.logger.Warn("delegation loop", "task", p.TaskID, "from", env.From)
		c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{
			TaskID: p.TaskID, Reason: "delegation loop",
		})
		return
	}

	t, err := c.store.CreateWithID(ctx, p.TaskID, p.ParentID, p.Project, p.TitleOrDefault(), c.nodeID, chain)
	if err != nil {
		c.logger.Error("create task from delegate", "err", err)
		return
	}
	// Adopt the upstream attempt so every copy of this task reports the same
	// attempt_id, and a result relayed along the chain is not flagged stale.
	if err := c.store.AdoptAttempt(ctx, t.TaskID, p.AttemptID); err != nil {
		c.logger.Warn("adopt attempt", "task", t.TaskID, "err", err)
	}
	// Persist the entry-model detail carried on the wire so the local queue
	// shows intent/context/complexity/risk even before execution starts.
	if err := c.store.SetDetail(ctx, t.TaskID, delegateDetail(p)); err != nil {
		c.logger.Warn("set detail", "task", t.TaskID, "err", err)
	}
	if p.TimeoutMS > 0 {
		if err := c.store.SetLease(ctx, t.TaskID, p.TimeoutMS); err != nil {
			c.logger.Warn("set lease", "task", t.TaskID, "err", err)
		}
	}

	required := delegateRequired(p)
	decision := scheduler.Route(c.nodeID, chain, c.onlineEmployees(ctx), c.localMatch(), required)

	switch decision.Action {
	case scheduler.ActionLocal:
		c.handleLocalDelegate(ctx, env, t.TaskID, p, required, chain)
	case scheduler.ActionForward:
		// Sub-scheduler: hand the task to a capable peer. The peer's result
		// arrives later via handleResult, which relays it up the chain — so no
		// immediate reply to the parent here.
		if err := c.forwardDelegated(ctx, t.TaskID, decision.Target, p, chain); err != nil {
			c.logger.Warn("forward delegated", "task", t.TaskID, "target", decision.Target, "err", err)
			c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{
				TaskID: t.TaskID, Reason: err.Error(),
			})
		}
	case scheduler.ActionDecline:
		c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{
			TaskID: t.TaskID, Reason: decision.Reason,
		})
	}
}

// handleLocalDelegate runs a task this node can execute, resolving its context
// first (design doc §12.4). A pointer hit executes immediately (zero transfer);
// a pointer miss parks the task in waiting_context and fetches the snapshot
// from the source node; summary and inline-full need no fetch.
func (c *Core) handleLocalDelegate(ctx context.Context, env bus.Envelope, taskID string, p bus.TaskDelegatePayload, required []string, chain []string) {
	level := p.ContextLevel
	hash := p.ContextHash

	if level == "full" && len(p.ContextData) > 0 {
		// Inline snapshot: cache it and proceed without a round-trip.
		if err := c.ctx.Put(ctx, hash, p.ContextType, p.ContextData, nil); err != nil {
			c.logger.Warn("store inline context", "task", taskID, "err", err)
		}
	} else if level == "pointer" && hash != "" {
		if ok, _ := c.ctx.Contains(ctx, hash); !ok {
			if err := c.prepare(ctx, taskID); err != nil {
				c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{TaskID: taskID, Reason: err.Error()})
				return
			}
			if err := c.store.SetWaitingContext(ctx, taskID, c.nodeID); err != nil {
				c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{TaskID: taskID, Reason: err.Error()})
				return
			}
			c.pendingCtx.Store(taskID, &pendingContext{intent: p.Intent, required: required, ctxType: p.ContextType})
			c.sendContextFetch(ctx, chain[0], taskID, hash, p.ContextType)
			return
		}
	}

	// Execute asynchronously so the message loop stays responsive to
	// task_cancel while a long native/agent command runs. The result (or a
	// decline) is reported back via the env captured here.
	go func() {
		result, err := c.execute(ctx, taskID, p.Intent, required)
		if err != nil {
			if errors.Is(err, ErrCancelled) {
				c.logger.Info("task cancelled during execution", "task", taskID)
				return
			}
			c.logger.Warn("route delegated task", "err", err, "task", taskID)
			c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{TaskID: taskID, Reason: err.Error()})
			return
		}
		if err := c.reply(ctx, env, bus.MsgTaskResult, result); err != nil {
			c.logger.Warn("send task_result", "err", err, "task", taskID)
		}
	}()
}

// delegateRequired resolves the abilities a delegated task needs, defaulting
// to the context type when the payload carries no explicit requires.
func delegateRequired(p bus.TaskDelegatePayload) []string {
	if len(p.Requires) > 0 {
		return p.Requires
	}
	if p.ContextType != "" {
		return []string{p.ContextType}
	}
	return nil
}

// localMatch reports whether this node's commander can route the required
// abilities locally. A nil router (no capability card) matches nothing.
func (c *Core) localMatch() func([]string) bool {
	return func(required []string) bool {
		if c.router == nil {
			return false
		}
		_, err := c.router.Route(required)
		return err == nil
	}
}

// onlineEmployees returns the known online nodes (self included) from the
// local capability directory.
func (c *Core) onlineEmployees(ctx context.Context) []ledger.Node {
	nodes, err := ledger.Query(c.db, "online", "")
	if err != nil {
		c.logger.Warn("query employees", "err", err)
		return nil
	}
	return nodes
}

// forwardDelegated records the task as dispatched to target and sends the
// delegate onward, carrying the appended chain.
func (c *Core) forwardDelegated(ctx context.Context, taskID, target string, p bus.TaskDelegatePayload, chain []string) error {
	if err := c.store.Queue(ctx, taskID, c.nodeID); err != nil {
		return fmt.Errorf("queue: %w", err)
	}
	if err := c.store.Dispatch(ctx, taskID, c.nodeID, target); err != nil {
		return fmt.Errorf("dispatch: %w", err)
	}
	p.Chain = chain
	env, err := bus.NewEnvelope(bus.MsgTaskDelegate, c.nodeID, mustUUID(), p)
	if err != nil {
		return err
	}
	env.To = target
	if err := c.sendTo(target, env); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	c.logger.Info("forwarded delegated task", "task", taskID, "to", target)
	return nil
}

// delegateDetail maps the wire payload's entry-model fields onto the persisted
// TaskDetail. resource_json is not present on the Phase 1 wire format, so it is
// left empty until a later phase adds it.
func delegateDetail(p bus.TaskDelegatePayload) TaskDetail {
	return TaskDetail{
		ContextType: p.ContextType,
		ContextHash: p.ContextHash,
		Intent:      p.Intent,
		SpecJSON:    p.SpecJSON,
		Complexity:  p.Complexity,
		Risk:        p.Risk,
	}
}

// execute runs the shared post-creation pipeline for a task that needs no
// context fetch: queue → dispatch → run. Both the WebSocket delegation path and
// the local entry path (SubmitLocal/Submit) funnel through here so there is
// exactly one execution implementation. The task must already be persisted
// (with detail) by the caller.
func (c *Core) execute(ctx context.Context, taskID, intent string, required []string) (bus.TaskResultPayload, error) {
	if err := c.prepare(ctx, taskID); err != nil {
		return bus.TaskResultPayload{}, err
	}
	return c.run(ctx, taskID, intent, required)
}

// prepare records a freshly-created task in the local queue and dispatches it
// to this node, so the queue reflects it even if the process dies mid-run.
func (c *Core) prepare(ctx context.Context, taskID string) error {
	if err := c.store.Queue(ctx, taskID, c.nodeID); err != nil {
		return fmt.Errorf("queue: %w", err)
	}
	if err := c.store.Dispatch(ctx, taskID, c.nodeID, c.nodeID); err != nil {
		return fmt.Errorf("dispatch: %w", err)
	}
	return nil
}

// run accepts (or resumes) a dispatched task into running, executes it, and
// records the outcome. The task may already be running (a context-fetch resume
// moved it there), dispatched (the normal path), or waiting_context (resumed
// here after the snapshot arrived).
func (c *Core) run(ctx context.Context, taskID, intent string, required []string) (bus.TaskResultPayload, error) {
	if c.router == nil {
		// No capability card loaded: nothing to execute.
		return bus.TaskResultPayload{}, fmt.Errorf("no commander configured")
	}
	plan, err := c.router.Route(required)
	if err != nil {
		return bus.TaskResultPayload{}, fmt.Errorf("route: %w", err)
	}

	task, err := c.store.Get(ctx, taskID)
	if err != nil {
		return bus.TaskResultPayload{}, fmt.Errorf("load task: %w", err)
	}
	switch task.State {
	case StateDispatched:
		if err := c.store.Accept(ctx, taskID, c.nodeID); err != nil {
			return bus.TaskResultPayload{}, fmt.Errorf("accept: %w", err)
		}
	case StateWaitingCtx:
		if err := c.store.Resume(ctx, taskID, c.nodeID); err != nil {
			return bus.TaskResultPayload{}, fmt.Errorf("resume: %w", err)
		}
	case StateRunning:
		// Already running (a duplicate context_ack raced ahead).
	default:
		return bus.TaskResultPayload{}, fmt.Errorf("cannot run task in state %s", task.State)
	}

	// Capture the current attempt id so the result carries it; the delegator
	// uses it to reject stale results after a transfer/retry.
	attemptID := task.AttemptID

	res := c.router.Execute(ctx, plan, intent, "")
	if res.NeedManual {
		// Manual tasks: notify and mark done; the human completes offline.
		if err := c.store.Complete(ctx, taskID, c.nodeID, map[string]any{
			"manual": true, "notify": res.Stdout,
		}); err != nil {
			if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
				return bus.TaskResultPayload{}, ErrCancelled
			}
			return bus.TaskResultPayload{}, fmt.Errorf("complete manual: %w", err)
		}
		return bus.TaskResultPayload{TaskID: taskID, AttemptID: attemptID, OK: true, ExitCode: 0, Stdout: res.Stdout}, nil
	}

	if !res.OK {
		if err := c.store.Fail(ctx, taskID, c.nodeID, res.Stderr); err != nil {
			if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
				return bus.TaskResultPayload{}, ErrCancelled
			}
			return bus.TaskResultPayload{}, fmt.Errorf("fail: %w", err)
		}
		return bus.TaskResultPayload{
			TaskID: taskID, AttemptID: attemptID, OK: false, ExitCode: res.ExitCode, Stderr: res.Stderr,
		}, nil
	}

	if err := c.store.Complete(ctx, taskID, c.nodeID, map[string]any{
		"ok": true, "exit_code": res.ExitCode, "stdout": res.Stdout,
	}); err != nil {
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
			return bus.TaskResultPayload{}, ErrCancelled
		}
		return bus.TaskResultPayload{}, fmt.Errorf("complete: %w", err)
	}
	return bus.TaskResultPayload{
		TaskID: taskID, AttemptID: attemptID, OK: true, ExitCode: res.ExitCode, Stdout: res.Stdout,
	}, nil
}

// handleAccept processes task_accept from the executor. The parent updates its
// local view of the task from dispatched to running so the queue reflects the
// executor's acceptance, then relays the accept up the delegation chain.
func (c *Core) handleAccept(ctx context.Context, env bus.Envelope) {
	var p bus.TaskAcceptPayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad task_accept", "err", err)
		return
	}
	t, err := c.store.Get(ctx, p.TaskID)
	if err != nil {
		c.logger.Debug("accept for unknown task", "task", p.TaskID)
		return
	}
	if t.State != StateDispatched {
		c.logger.Debug("accept ignored (not dispatched)", "task", p.TaskID, "state", t.State)
		return
	}
	// The executor claims the lease; the parent records the new state and
	// owner so a later cancel/transfer routes correctly.
	if err := c.store.Accept(ctx, p.TaskID, env.From); err != nil {
		c.logger.Debug("accept apply failed", "task", p.TaskID, "err", err)
	}
	c.relayToParent(ctx, bus.MsgTaskAccept, t.Chain, p)
}

// handleDecline processes task_decline from an executor. The task returns to
// queued for re-routing, and the decline is relayed up the chain so the root
// can retry elsewhere.
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
	t, err := c.store.Get(ctx, p.TaskID)
	if err != nil {
		return
	}
	c.relayToParent(ctx, bus.MsgTaskDecline, t.Chain, p)
	// Unblock a synchronous Submit that forwarded this task: it sees a failed
	// result (exit 1) rather than hanging forever with no capability anywhere.
	c.signalResult(p.TaskID, bus.TaskResultPayload{
		TaskID: p.TaskID, OK: false, ExitCode: 1, Stderr: "declined: " + p.Reason,
	})
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
		// creating one (Phase 0 entry flow). Reconstruct a minimal row,
		// adopting the executor's attempt so the result is not flagged stale.
		if _, err := c.store.CreateFromRemote(ctx, p.TaskID, p.TaskID, c.nodeID, p.AttemptID, []string{c.nodeID, env.From}); err != nil {
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
	// Relay the result up the delegation chain so the root scheduler learns
	// the outcome (a root with no predecessor is a no-op).
	c.relayToParent(ctx, bus.MsgTaskResult, t.Chain, p)
	c.signalResult(p.TaskID, p)
}

// relayToParent forwards a control/result message to this task's parent in the
// delegation chain. It is a no-op for the root (no predecessor). A missing
// parent connection is logged, not fatal — the outcome is still recorded
// locally and can be reconciled later.
func (c *Core) relayToParent(ctx context.Context, typ string, chain []string, payload any) {
	parent := scheduler.Predecessor(chain, c.nodeID)
	if parent == "" {
		return
	}
	env, err := bus.NewEnvelope(typ, c.nodeID, mustUUID(), payload)
	if err != nil {
		c.logger.Warn("build relay", "type", typ, "err", err)
		return
	}
	env.To = parent
	if err := c.sendTo(parent, env); err != nil {
		c.logger.Warn("relay", "type", typ, "to", parent, "err", err)
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

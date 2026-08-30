package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/plan"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
	"github.com/Xustalis/OpenPanda/internal/security"
	"github.com/Xustalis/OpenPanda/internal/skills"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// progressInterval is the minimum spacing between EvProgress recordings:
// dense enough to feel live in `panda task` / the panel timeline, sparse
// enough that a chatty adapter cannot flood the event chain.
const progressInterval = 2 * time.Second

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

	// plan_id and stage_id become path segments of this node's stage work dir
	// (stageWorkDir), so they are whitelisted at the wire boundary — a peer
	// must never be able to aim execution or output packing at an arbitrary
	// directory (review P0-1).
	if (p.PlanID != "" && !plan.ValidID(p.PlanID)) || (p.StageID != "" && !plan.ValidID(p.StageID)) {
		c.logger.Warn("task_delegate with unsafe stage identity", "task", p.TaskID,
			"plan", p.PlanID, "stage", p.StageID, "from", env.From)
		c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{
			TaskID: p.TaskID, Reason: "invalid plan/stage id",
		})
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
	// — Trace: this node accepted a delegation hop (from=upstream delegator,
	// to=here). The orbit's delegation-chain reconstruction flattens these in
	// arrival order; best-effort only.
	c.EvTrace(ctx, t.TaskID, EvDelegationHop, map[string]any{
		"from_node":  env.From,
		"to_node":    c.nodeID,
		"via":        "direct",
		"chain":      chain,
		"attempt_id": p.AttemptID,
	})
	// Adopt the origin user's tier-2 consent so the executor's defense layer
	// honors what the delegating user already approved (the authenticated bus
	// is the trust boundary; see TaskDelegatePayload.Authorized).
	if p.Authorized {
		if err := c.store.SetAuthorized(ctx, t.TaskID, true); err != nil {
			c.logger.Warn("adopt authorization", "task", t.TaskID, "err", err)
		}
	}
	// Persist the entry-model detail carried on the wire so the local queue
	// shows intent/context/complexity/risk even before execution starts.
	if err := c.store.SetDetail(ctx, t.TaskID, delegateDetail(p)); err != nil {
		c.logger.Warn("set detail", "task", t.TaskID, "err", err)
	}
	// A delegated stage of a plan keeps its place in that plan and the artifacts
	// it must start from. Both are needed locally before execution: run() derives
	// the stage work dir from plan_id/stage_id, and fetchStageInputs pulls the
	// inputs from the nodes named here. The dependency graph is deliberately not
	// carried: only the node orchestrating the plan decides what runs next.
	if p.PlanID != "" || p.StageID != "" {
		if err := c.store.SetStage(ctx, t.TaskID, p.PlanID, p.StageID, nil); err != nil {
			c.logger.Warn("set stage", "task", t.TaskID, "err", err)
		}
		if len(p.Inputs) > 0 {
			if err := c.store.SetStageInputs(ctx, t.TaskID, p.Inputs); err != nil {
				c.logger.Warn("set stage inputs", "task", t.TaskID, "err", err)
			}
		}
	}
	if p.TimeoutMS > 0 {
		if err := c.store.SetLease(ctx, t.TaskID, p.TimeoutMS); err != nil {
			c.logger.Warn("set lease", "task", t.TaskID, "err", err)
		}
	}

	required := delegateRequired(p)
	decision := scheduler.Route(c.nodeID, chain, c.onlineEmployees(ctx), c.localMatch(), required,
		resourceRequirement(p.ResourceJSON), p.PreferredNode)

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
			c.terminalizeDeclined(ctx, t.TaskID, err.Error())
		}
	case scheduler.ActionDecline:
		c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{
			TaskID: t.TaskID, Reason: decision.Reason,
		})
		c.terminalizeDeclined(ctx, t.TaskID, decision.Reason)
	}
}

// terminalizeDeclined moves a locally-created task row to a terminal state when
// this node declines to run it (no matching capability, or a failed forward).
// Without this the row lingers in submitted/queued/dispatched, polluting the
// queue and — on restart — being resurrected by Recover. Cancel is the only
// terminal transition legal from every pre-execution state.
func (c *Core) terminalizeDeclined(ctx context.Context, taskID, reason string) {
	if err := c.store.Cancel(ctx, taskID); err != nil {
		c.logger.Warn("terminalize declined task", "task", taskID, "err", err)
	}
}

// handleLocalDelegate runs a task this node can execute, resolving its context
// first (design doc §12.4). A pointer hit executes immediately (zero transfer);
// a pointer miss parks the task in waiting_context and fetches the snapshot
// from the source node; summary and inline-full need no fetch.
//
// Capacity-driven accept/decline (DCPS τ_adp mapping, design §2.4): a node
// whose execution slots are full declines instead of silently queueing, so the
// delegator learns immediately and can re-route to a peer with free capacity.
func (c *Core) handleLocalDelegate(ctx context.Context, env bus.Envelope, taskID string, p bus.TaskDelegatePayload, required []string, chain []string) {
	if !c.hasCapacity(ctx) {
		c.logger.Info("declining delegated task: capacity full", "task", taskID)
		c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{TaskID: taskID, Reason: "capacity full"})
		c.terminalizeDeclined(ctx, taskID, "capacity full")
		return
	}
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
			// A task parked in waiting_context must carry a lease (P1-6):
			// without one the timeout monitor never scans it, and if the
			// context_fetch answer never arrives the task — and its pendingCtx
			// entry — would leak forever. Fall back to the default delegation
			// deadline when the wire carried no explicit timeout.
			timeoutMS := p.TimeoutMS
			if timeoutMS <= 0 {
				timeoutMS = c.lease().Milliseconds()
			}
			if err := c.store.SetLease(ctx, taskID, timeoutMS); err != nil {
				c.logger.Warn("lease waiting-context task", "task", taskID, "err", err)
			}
			c.pendingCtx.Store(taskID, &pendingContext{intent: p.Intent, required: required, ctxType: p.ContextType, source: chain[0]})
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
			// The decline tells the parent to re-route, but the local row must
			// not linger in submitted/queued: Recover would resurrect it on
			// restart as an orphan nobody dispatches (P1-9).
			c.terminalizeDeclined(ctx, taskID, err.Error())
			return
		}
		if err := c.reply(ctx, env, bus.MsgTaskResult, result); err != nil {
			c.logger.Warn("send task_result", "err", err, "task", taskID)
			// The delegator is unreachable at the moment the result is ready
			// (review P0-2): park it so the outcome is redelivered on reconnect
			// instead of the delegator's lease expiring into a false failed.
			if result.TaskID == "" {
				result.TaskID = taskID
			}
			c.outboxPersist(ctx, env.From, result)
		} else {
			c.outboxDrop(ctx, env.From, taskID)
		}
	}()
}

// hasCapacity reports whether this node can accept one more delegated task
// (DCPS capacity-driven accept/decline, design §2.4): true unless the
// capability card declares a MaxConcurrent limit and the active-task count has
// reached it. A node with no declared limit always accepts (unknown capacity is
// not a limit); a load-count failure fails closed (declining is recoverable —
// the delegator re-routes — while over-committing a saturated node is not).
func (c *Core) hasCapacity(ctx context.Context) bool {
	maxConcurrent := c.Card().Capacity.MaxConcurrent
	if maxConcurrent <= 0 {
		return true
	}
	active, err := c.store.CountActive(ctx, c.nodeID)
	if err != nil {
		c.logger.Warn("count active tasks", "err", err)
		return false
	}
	return active < maxConcurrent
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

// resourceRequirement decodes a task's declared hardware requirement into the
// form routing compares against a node's card. The task side is
// entry.ResourceProfile (floats — a model asked for "1.5 GiB" is a legitimate
// answer), the node side is ledger.ResourceProfile (whole units — a card declares
// the hardware that is physically there), so the crossing rounds *up*: needing
// 1.5 GiB of VRAM means an 1 GiB card will not do.
//
// An unparseable or absent profile is no requirement at all rather than an error.
// The field is optional, most tasks have none, and a task must not become
// unroutable because its resource hint was malformed.
func resourceRequirement(resourceJSON string) ledger.ResourceProfile {
	if resourceJSON == "" {
		return ledger.ResourceProfile{}
	}
	var want entry.ResourceProfile
	if err := json.Unmarshal([]byte(resourceJSON), &want); err != nil {
		return ledger.ResourceProfile{}
	}
	return ledger.ResourceProfile{
		CPU:          want.CPU,
		RAMGB:        int(math.Ceil(want.RAMGB)),
		GPUVRAMGB:    int(math.Ceil(want.GPUVRAMGB)),
		DurationHint: want.DurationHint,
	}
}

// localMatch reports whether this node's commander can route the required
// abilities locally. A nil router (no capability card) matches nothing.
func (c *Core) localMatch() func([]string) bool {
	return func(required []string) bool {
		router := c.currentRouter()
		if router == nil {
			return false
		}
		_, err := router.Route(required)
		return err == nil
	}
}

// onlineEmployees returns the known online nodes (self included) from the
// local capability directory, with this node's own active-task count refreshed
// from the tasks table.
//
// The refresh matters now that routing scores this node against its peers: self's
// row is only as fresh as the last heartbeat tick, so a burst of tasks published
// in one breath would all be scored against a count of zero and all stay home —
// exactly the load-balancing case the scoring exists for. Peers' counts arrive by
// heartbeat and cannot be better than that; ours can, and it is one COUNT.
func (c *Core) onlineEmployees(ctx context.Context) []ledger.Node {
	nodes, err := ledger.Query(c.db, "online", "")
	if err != nil {
		c.logger.Warn("query employees", "err", err)
		return nil
	}
	var active int
	if err := c.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks WHERE state IN ('running','waiting_context')`).Scan(&active); err != nil {
		c.logger.Warn("count active tasks for routing", "err", err)
		return nodes
	}
	for i := range nodes {
		if scheduler.IsSelfRow(nodes[i].ID, c.nodeID) {
			nodes[i].Capacity.CurrentTasks = active
			break
		}
	}
	return nodes
}

// defaultDelegateTimeout is the fallback task lease, used when the wire payload
// carries no explicit timeout and no config override is in force. It bounds how
// long a delegator waits on a *silent* executor before failing the task and
// propagating the failure upstream — not how long the work may take: a live
// executor heartbeats the lease (renewLease) for as long as it runs.
//
// It must stay above commander.AgentHardTimeout(); SetTimeouts enforces that
// for configured values.
const defaultDelegateTimeout = 20 * time.Minute

// forwardDelegated records the task as dispatched to target and sends the
// delegate onward, carrying the appended chain.
func (c *Core) forwardDelegated(ctx context.Context, taskID, target string, p bus.TaskDelegatePayload, chain []string) error {
	if err := c.store.Queue(ctx, taskID, c.nodeID); err != nil {
		return fmt.Errorf("queue: %w", err)
	}
	return c.dispatchDelegated(ctx, taskID, target, p, chain)
}

// dispatchDelegated is forwardDelegated for a task already in queued state —
// e.g. a declined task being re-routed (P1-5), where Decline already moved it
// dispatched -> queued and a second queue transition would conflict.
func (c *Core) dispatchDelegated(ctx context.Context, taskID, target string, p bus.TaskDelegatePayload, chain []string) error {
	if err := c.store.Dispatch(ctx, taskID, c.nodeID, target); err != nil {
		return fmt.Errorf("dispatch: %w", err)
	}
	// Stamp a lease on the local copy so a dead executor is detected and the
	// failure propagated, instead of leaving this copy dispatched forever (D3).
	// The timeout is carried on the wire so every hop inherits the same deadline.
	timeoutMS := p.TimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = c.lease().Milliseconds()
		p.TimeoutMS = timeoutMS
	}
	if err := c.store.SetLease(ctx, taskID, timeoutMS); err != nil {
		return fmt.Errorf("set lease: %w", err)
	}
	p.Chain = chain
	msgID, err := newUUID()
	if err != nil {
		return err
	}
	env, err := bus.NewEnvelope(bus.MsgTaskDelegate, c.nodeID, msgID, p)
	if err != nil {
		return err
	}
	env.To = target
	if err := c.sendTo(target, env); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	// — Trace: this node handed the task one hop downstream (from=here,
	// to=target). Recorded on this node's copy so the origin's task detail
	// shows the outbound leg too.
	c.EvTrace(ctx, taskID, EvDelegationHop, map[string]any{
		"from_node":  c.nodeID,
		"to_node":    target,
		"via":        "direct",
		"chain":      chain,
		"attempt_id": p.AttemptID,
	})
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
		// The hardware requirement is persisted, not just consulted in passing:
		// this node may re-route the task later (a decline, a re-queue), and the
		// wire payload is gone by then. Without it a training task would lose its
		// GPU requirement at the second hop.
		ResourceJSON: p.ResourceJSON,
		Requires:     delegateRequired(p),
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
	router := c.currentRouter()
	if router == nil {
		// No capability card loaded: nothing to execute.
		return bus.TaskResultPayload{}, fmt.Errorf("no commander configured")
	}
	plan, err := router.Route(required)
	if err != nil {
		return bus.TaskResultPayload{}, fmt.Errorf("route: %w", err)
	}

	// Circuit breaker (P2-27): refuse to run an agent that has been failing
	// repeatedly, before the task leaves its dispatched state, so the parent
	// can re-route it elsewhere instead of it stalling in running.
	var breakerKey string
	if plan.Kind == "agent" {
		breakerKey = "agent:" + plan.Agent
		if !c.breaker.Allow(breakerKey) {
			c.audit(ctx, taskID, "agent:spawn", plan.Agent, "open", "circuit open")
			return bus.TaskResultPayload{}, fmt.Errorf("agent %s circuit open", plan.Agent)
		}
		// Drop circuit-open alternates before the fallback chain runs. The
		// commander's fallback only knows about CLI availability, not breaker
		// state, so a blocked runner-up would otherwise be re-entered while it
		// is failing repeatedly. Blocked is read-only: filtering must not spend
		// a half-open trial slot on an agent this task may never reach.
		if len(plan.Alternates) > 0 {
			alternates := make([]string, 0, len(plan.Alternates))
			for _, name := range plan.Alternates {
				if !c.breaker.Blocked("agent:" + name) {
					alternates = append(alternates, name)
				}
			}
			plan.Alternates = alternates
		}
	}

	task, err := c.store.Get(ctx, taskID)
	if err != nil {
		return bus.TaskResultPayload{}, fmt.Errorf("load task: %w", err)
	}
	switch task.State {
	case StateDispatched:
		if err := c.store.Accept(ctx, taskID, c.nodeID); err != nil {
			// The accept can lose the race with a cancel landing between the
			// pre-check above and the guarded write. Reporting a plain error
			// here would turn into a task_decline, bouncing a task that is
			// already closed back into re-routing; re-read and distinguish:
			// running means a duplicate context_ack raced ahead (execute on),
			// terminal means the task was closed under us (cancelled/expired —
			// execution acknowledges, no result is reported).
			if errors.Is(err, ErrConflict) {
				fresh, gerr := c.store.Get(ctx, taskID)
				if gerr == nil && fresh.State == StateRunning {
					break // duplicate accept raced ahead; fall through to execution
				}
				if gerr == nil && Terminal(fresh.State) {
					return bus.TaskResultPayload{}, ErrCancelled
				}
			}
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

	// Notify the delegator that this node has accepted the task so its copy
	// transitions dispatched -> running and can time the execution (D3). A local
	// task (chain = [self]) has no predecessor and relayToParent is a no-op.
	c.relayToParent(ctx, bus.MsgTaskAccept, task.Chain, bus.TaskAcceptPayload{TaskID: taskID})

	// Capture the current attempt id so the result carries it; the delegator
	// uses it to reject stale results after a transfer/retry.
	attemptID := task.AttemptID

	// Execution lifetime (P0-1). Two things must hold for a long stage — a
	// training run, a multi-minute agent session — to survive:
	//   1. its lease has to keep being renewed, here and one hop up the chain,
	//      or the monitor force-fails work that is still running and the parent
	//      re-routes it to a second node;
	//   2. a force-fail has to actually stop the subprocess, which needs a
	//      cancellable context registered under the task id.
	// Both stop when this function returns.
	execCtx, cancelExec := context.WithCancel(ctx)
	defer cancelExec()
	defer c.registerRunning(taskID, cancelExec)()
	defer c.renewLease(execCtx, taskID, task.Chain, attemptID)()

	// Model-injection policy check (A1): decided once before the supervision
	// loop — the adapter (and therefore the plan) is identical across rounds,
	// so the decision does not vary between an initial run and a re-delegation.
	var injection commander.InjectionDecision
	if plan.Kind == "agent" {
		injection = router.InjectionDecision(plan.Adapter)
	}

	// workDir is normally the node-wide execution directory; a queued task may
	// pin its own (a panel session's worktree) so concurrent tasks never share
	// a working directory (queue redesign — SetWorkDir is process-global and
	// only the synchronous paths may swap it).
	workDir := c.workDir
	if task.WorkDir != "" {
		workDir = task.WorkDir
	}

	// A stage of a plan executes in a directory of its own, derived here rather
	// than carried on the wire (see stageWorkDir), and pre-loaded with the trees
	// its predecessors produced. Both steps must happen before anything runs: the
	// training stage's whole reason to exist is the script the coding stage wrote.
	// Returning the error rather than running on absent input is what keeps a
	// successor from reporting green over work that was done on nothing — the
	// caller's retry/fail path then retries the pull or surfaces the failure to
	// the delegator.
	if task.PlanID != "" {
		wd, err := c.stageWorkDir(task.PlanID, task.StageID)
		if err != nil {
			return bus.TaskResultPayload{}, err
		}
		workDir = wd
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			return bus.TaskResultPayload{}, fmt.Errorf("create stage work dir: %w", err)
		}
		if err := c.fetchStageInputs(execCtx, task, workDir); err != nil {
			return bus.TaskResultPayload{}, fmt.Errorf("stage inputs: %w", err)
		}
	}

	// Scope drift (design §14.2 signal A): for an agent task that declares a
	// scope, snapshot the working directory before execution so changes outside
	// the scope can be intercepted rather than silently committed. The snapshot
	// is taken once and reused across supervision rounds.
	scope := defense.NewScope(taskScope(task.SpecJSON))
	var before defense.Snapshot
	if plan.Kind == "agent" && !scope.Empty() {
		var err error
		if before, err = defense.SnapshotDir(workDir); err != nil {
			c.logger.Warn("snapshot workdir before agent", "task", taskID, "err", err)
		}
	}

	// Selective memory loading (A3) and live progress (A5) are per-task, not
	// per-round: the memory-file manifest and the throttled progress sink are
	// built once and reused across supervision rounds. They decorate execCtx,
	// which already carries the cancellation the abort registry holds.
	if plan.Kind == "agent" && task.Project == "" && c.memory != nil {
		if files, err := c.memory.Manifest(); err == nil {
			paths := make([]string, 0, len(files))
			for _, f := range files {
				paths = append(paths, f.Path)
			}
			execCtx = commander.WithMemoryFiles(execCtx, paths)
		}
	}
	if plan.Kind == "agent" {
		var lastNote atomic.Value // string
		lastNote.Store("")
		execCtx = commander.WithProgress(execCtx, func(note string) {
			if prev, _ := lastNote.Load().(string); prev == note {
				return // identical consecutive note: skip
			}
			lastNote.Store(note)
			c.progressMu.Lock()
			due := time.Since(c.lastProgress) >= progressInterval
			if due {
				c.lastProgress = time.Now()
			}
			c.progressMu.Unlock()
			if !due {
				return
			}
			if err := c.store.RecordEvent(context.WithoutCancel(ctx), taskID, EvProgress,
				map[string]any{"note": note}); err != nil {
				c.logger.Warn("record agent progress", "task", taskID, "err", err)
			}
		})
	}

	// Supervision loop. An agent task under a supervisor executes once and is
	// then judged; a "continue" verdict re-delegates the follow-up instruction
	// to the same plan (whose fallback chain may pick a different agent if the
	// primary's CLI is unavailable) until the judging model accepts the work or
	// the round budget runs out. Every non-agent plan — and every agent plan
	// without a supervisor — converges in a single round.
	maxRounds := 1
	if plan.Kind == "agent" && c.supervisor != nil {
		maxRounds = c.superviseRounds
	}

	currentIntent := intent
	var res commander.Result
	var usedSkills []*skills.Skill
	verdict := entry.SuperviseVerdict{Status: entry.VerdictDone}
	for round := 0; round < maxRounds; round++ {
		// emitRound traces one supervision_round with the round's final verdict.
		// Callers fire it only once the verdict actually exists — after the judge
		// pass, or immediately for rounds that never reach a judge (a failed run,
		// the last round of the budget, a plan without a supervisor). Tracing
		// earlier used to hand the agent's whole runtime to the "reviewing" stage:
		// a stage's on-screen duration runs until the next event lands.
		emitRound := func(status string) {
			c.EvTrace(execCtx, taskID, EvSupervisionRound, map[string]any{
				"round":          round + 1,
				"budget":         maxRounds,
				"agent":          res.Agent,
				"ok":             res.OK,
				"verdict_status": status,
			})
		}
		prompt, skillsUsed := buildAgentPrompt(c, currentIntent, task.Project, task.Title)
		usedSkills = skillsUsed

		// — Trace: exec_agent_start (orbit Step-3 "starting this stage on N").
		// We report before Execute so the orbit paints the stage bar as
		// running immediately. Best-effort only.
		c.EvTrace(execCtx, taskID, EvExecAgentStart, map[string]any{
			"round":      round,
			"budget":     maxRounds,
			"plan_kind":  plan.Kind,
			"agent":      plan.Agent,
			"adapter":    plan.Adapter,
			"authorized": task.Authorized,
			"tier":       plan.Tier,
		})

		res = router.Execute(execCtx, plan, prompt, workDir, task.Authorized)

		// — Trace: the Tier-2 gate outcome. A refusal is deterministic policy
		// (the task parks in review with the reason); an authorized run is
		// traced once on the first round — the gate verdict does not change
		// between supervision rounds.
		if plan.Tier >= defense.TierIrreversible && (round == 0 || commander.IsAuthorizationRefusal(res.Stderr)) {
			op := plan.Command
			target := ""
			if plan.Kind == "agent" {
				op = plan.Agent
			}
			// Design doc §3.1.1: operations: [{op, target, risk}] array.
			// If the gate refused we have a raw reason string; translate
			// target/risk to "unknown" when the defense layer did not
			// decompose them for us.
			// Defense layer currently has two tiers only: reversible vs
			// irreversible (Tier-2). Any Tier-2 here is at least medium risk.
			ops := []map[string]any{{
				"op":     op,
				"target": target,
				"risk":   "medium",
			}}
			ev := map[string]any{
				"operations":       ops,
				"kind":             plan.Kind,
				"tier":             plan.Tier,
				"authorized":       task.Authorized,
				"result":           "authorized",
				"parked_in_review": false,
			}
			if commander.IsAuthorizationRefusal(res.Stderr) {
				// A refusal is deterministic policy: the task parks in review
				// with the reason (retryLoop routes it there), so the orbit
				// marks the parking now — the later PauseWithResult trace is
				// for the *accepted* outcome and never fires on this path.
				ev["result"] = "denied"
				ev["reason"] = res.Stderr
				ev["parked_in_review"] = true
			}
			c.EvTrace(execCtx, taskID, EvTier2Triggered, ev)
		}

		// — Trace: supervision_round, part 1. Judged rounds trace after the
		// judge pass below, where their verdict becomes known; only rounds that
		// never reach a judge trace here, so the detail view's "2/5" badge and
		// verdict pill always reflect a decided round.
		judgeWillRun := plan.Kind == "agent" && c.supervisor != nil && round < maxRounds-1 && res.OK
		if !judgeWillRun {
			status := string(verdict.Status)
			if !res.OK {
				// A failed round is never judged (the failure branch below
				// returns before Supervise runs), so reporting the verdict
				// variable — its zero value, or the previous round's — would paint
				// "done" over a failed attempt. The execution outcome is the only
				// truthful status here.
				status = "failed"
			}
			emitRound(status)
		}

		if plan.Kind == "agent" {
			// Fallback chain visibility (A2): res.Agent names the agent that
			// actually executed; a mismatch means the scored primary's CLI was
			// unavailable and a runner-up took over. The breaker tracks the agent
			// that really ran.
			if res.Agent != "" && res.Agent != plan.Agent {
				breakerKey = "agent:" + res.Agent
				c.audit(ctx, taskID, "agent:fallback", plan.Agent, "fallback", "primary unavailable, fell back to "+res.Agent)
				if err := c.store.RecordEvent(ctx, taskID, EvAgentFallback, map[string]any{"from": plan.Agent, "to": res.Agent}); err != nil {
					c.logger.Warn("record agent fallback event", "task", taskID, "err", err)
				}
			}
			// Explicit injection reminder (A1): when panda overrode the agent's
			// model endpoint, say so at the top of the output, in the audit log,
			// and in the task event stream the Web task detail replays. Gated on
			// the adapter actually running (a tier refusal spawns nothing).
			if injection.Inject && res.Agent != "" {
				notice := commander.InjectionNotice(injection, res.Agent)
				if res.Stdout != "" {
					res.Stdout = notice + "\n" + res.Stdout
				} else {
					res.Stdout = notice
				}
				c.audit(ctx, taskID, "model:injected", res.Agent, "injected",
					"model="+injection.Model+" endpoint="+injection.BaseURL+" reason="+injection.Reason)
				if err := c.store.RecordEvent(ctx, taskID, EvModelInjection, map[string]any{
					"agent": res.Agent, "model": injection.Model, "base_url": injection.BaseURL, "reason": injection.Reason,
				}); err != nil {
					c.logger.Warn("record model injection event", "task", taskID, "err", err)
				}
			}
			if res.OK {
				c.breaker.RecordSuccess(breakerKey)
			} else if c.breaker.RecordFailure(breakerKey) {
				c.audit(ctx, taskID, "circuit:open", plan.Agent, "failed", "agent failure threshold reached")
			}
			// Skills only steer agent execution; record their use so the lifecycle
			// (dormant/expired) reflects real usage.
			recordSkillUse(c, usedSkills, res.OK)
		}
		// A Tier-2 (irreversible) native command is high-risk: record who ran it
		// and whether it was authorized, for later review (P3-32).
		if plan.Kind == "native" && plan.Tier >= defense.TierIrreversible {
			result := "authorized"
			if !task.Authorized {
				result = "denied"
			}
			c.audit(ctx, taskID, "native:tier2", plan.Command, result, "")
		}

		// Scope-drift intercept: a successful agent that touched files outside
		// its declared scope has overstepped the task. Pause it for human
		// analysis rather than mark it done, fail it into the retry loop, or
		// re-delegate — a deterministic intercept will not improve on retry.
		if plan.Kind == "agent" && !scope.Empty() && res.OK {
			after, err := defense.SnapshotDir(workDir)
			if err != nil {
				c.logger.Warn("snapshot workdir after agent", "task", taskID, "err", err)
			} else if drift := c.filterHostDrift(scope.Drift(after.Changed(before))); len(drift) > 0 {
				msg := "scope drift: agent changed files outside declared scope: " + strings.Join(drift, ", ")
				c.audit(ctx, taskID, "scope:drift", plan.Agent, "denied", msg)
				if err := c.store.Pause(ctx, taskID, c.nodeID, msg); err != nil {
					if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
						return bus.TaskResultPayload{}, ErrCancelled
					}
					return bus.TaskResultPayload{}, fmt.Errorf("pause on scope drift: %w", err)
				}
				c.logTask(task.Title, false)
				trackTask(c, task.Project, required, task.Title, false)
				return bus.TaskResultPayload{
					TaskID: taskID, AttemptID: attemptID, State: StateReview, OK: false, ExitCode: 1, Stderr: msg,
					Tokens: res.Tokens, Cost: res.Cost,
				}, nil
			}
		}

		if res.NeedManual {
			// Manual tasks park in review, not done: the human has not acted yet,
			// and only a task meeting its success definition may enter done. The
			// notify text is preserved as the result so whoever picks it up sees
			// what is being asked of them; Approve/Reject moves it on from there.
			if err := c.store.PauseWithResult(ctx, taskID, c.nodeID, map[string]any{
				"manual": true, "notify": res.Stdout,
			}); err != nil {
				if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
					return bus.TaskResultPayload{}, ErrCancelled
				}
				return bus.TaskResultPayload{}, fmt.Errorf("pause manual: %w", err)
			}
			c.logTask(task.Title, false)
			trackTask(c, task.Project, required, task.Title, false)
			return bus.TaskResultPayload{
				TaskID: taskID, AttemptID: attemptID, State: StateReview, OK: true, ExitCode: 0, Stdout: res.Stdout,
				Tokens: res.Tokens, Cost: res.Cost,
			}, nil
		}

		if !res.OK {
			// A tier-2 authorization refusal is deterministic policy, not a failed
			// attempt: retrying cannot produce consent. Park the task in review —
			// running -> failed -> review, the same shape the local retry loop
			// parks in — so an approval (inline at the ask prompt, or a
			// task_resume from the delegator) can re-run it with consent. Failing
			// here instead would strand the refusal as a dead-end failure on the
			// delegator: it surfaces the reason but offers no way to resolve it.
			if commander.IsAuthorizationRefusal(res.Stderr) {
				if err := c.store.Fail(ctx, taskID, c.nodeID, res.Stderr); err != nil {
					if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
						return bus.TaskResultPayload{}, ErrCancelled
					}
					return bus.TaskResultPayload{}, fmt.Errorf("fail: %w", err)
				}
				if err := c.store.Review(ctx, taskID, c.nodeID, res.Stderr); err != nil {
					if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
						return bus.TaskResultPayload{}, ErrCancelled
					}
					return bus.TaskResultPayload{}, fmt.Errorf("park refused task: %w", err)
				}
				c.logTask(task.Title, false)
				trackTask(c, task.Project, required, task.Title, false)
				return bus.TaskResultPayload{
					TaskID: taskID, AttemptID: attemptID, State: StateReview, OK: false, ExitCode: res.ExitCode, Stderr: res.Stderr,
					Tokens: res.Tokens, Cost: res.Cost,
				}, nil
			}
			if err := c.store.Fail(ctx, taskID, c.nodeID, res.Stderr); err != nil {
				if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
					return bus.TaskResultPayload{}, ErrCancelled
				}
				return bus.TaskResultPayload{}, fmt.Errorf("fail: %w", err)
			}
			c.logTask(task.Title, false)
			trackTask(c, task.Project, required, task.Title, false)
			return bus.TaskResultPayload{
				TaskID: taskID, AttemptID: attemptID, State: StateFailed, OK: false, ExitCode: res.ExitCode, Stderr: res.Stderr,
				Tokens: res.Tokens, Cost: res.Cost,
			}, nil
		}

		// Supervision applies only to agent tasks under a configured supervisor.
		if plan.Kind != "agent" || c.supervisor == nil {
			break
		}
		usageBefore := c.supervisor.Usage()
		// — Trace: judge_start. The reviewing stage's on-screen duration runs
		// until the next event lands, so the judge call needs its own opening
		// marker — otherwise its runtime is billed to the executing stage.
		// CLI-only: it is not in the panel's forwarded-event set, so the web
		// orbit never sees it.
		c.EvTrace(execCtx, taskID, EvJudgeStart, map[string]any{
			"round":  round + 1,
			"budget": maxRounds,
		})
		judgeStart := time.Now()
		v, serr := entry.Supervise(ctx, c.supervisor, currentIntent, res.Stdout)
		c.recordEntryUsage(context.WithoutCancel(ctx), taskID, c.supervisor, usageBefore,
			v.Status == entry.VerdictDone, time.Since(judgeStart))
		if serr != nil {
			// Supervisor unreachable: Supervise parks the result for review rather
			// than accepting it unverified (review P1-6). The work stops here and
			// a human decides it once the supervisor recovers — only verified
			// work may reach done.
			c.logger.Warn("supervise call failed", "task", taskID, "err", serr)
		}
		verdict = v
		// — Trace: supervision_round, part 2. The judge has spoken, so the
		// round's badge can carry the verdict it actually produced — and the
		// reviewing stage's stopwatch starts only now, not over the agent run.
		emitRound(string(v.Status))
		if err := c.store.RecordEvent(context.WithoutCancel(ctx), taskID, EvSupervise, map[string]any{
			"round": round + 1, "status": v.Status, "reason": v.Reason, "followup": v.Followup,
		}); err != nil {
			c.logger.Warn("record supervise event", "task", taskID, "err", err)
		}
		if v.Status == entry.VerdictDone {
			break
		}
		if v.Status == entry.VerdictReview {
			// No verdict obtainable: stop iterating and let the review branch
			// below hand the result to a human.
			break
		}
		if strings.TrimSpace(v.Followup) == "" {
			currentIntent = currentIntent + "\n\n上一轮未能完整完成，请继续完成剩余工作，并汇报最终结果。"
		} else {
			currentIntent = v.Followup
		}
	}

	// A stage hands its work-dir to its successors as a content-addressed
	// artifact. It is packed before the terminal branches below, not inside one of
	// them, because a stage that parks in review still produced a tree and the
	// hash has to travel with the result either way. A pack failure fails the
	// stage: reporting done without an artifact would block every successor
	// forever on something that was never produced.
	var outputArtifact string
	if task.PlanID != "" {
		var perr error
		if outputArtifact, perr = c.packStageOutput(ctx, task, workDir); perr != nil {
			return bus.TaskResultPayload{}, perr
		}
	}

	// The supervisor did not accept the result: either work still remains after
	// the round budget ("continue"), or the model answered without a usable
	// verdict ("review" — unparsable output, or an unreachable supervisor that
	// parks rather than accepts unverified work, review P1-6). Both park in
	// review with the latest result and a marker so a human sees what was done
	// and what remains, rather than looping indefinitely or silently accepting
	// unverified work.
	if plan.Kind == "agent" && c.supervisor != nil &&
		(verdict.Status == entry.VerdictContinue || verdict.Status == entry.VerdictReview) {
		if err := c.store.PauseWithResult(ctx, taskID, c.nodeID, map[string]any{
			"ok": true, "exit_code": res.ExitCode, "stdout": res.Stdout, "agent": res.Agent,
			"needs_followup": verdict.Status == entry.VerdictContinue,
			"verdict":        verdict.Status, "verdict_reason": verdict.Reason,
		}); err != nil {
			if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
				return bus.TaskResultPayload{}, ErrCancelled
			}
			return bus.TaskResultPayload{}, fmt.Errorf("pause for follow-up: %w", err)
		}
		c.logTask(task.Title, false)
		trackTask(c, task.Project, required, task.Title, false)
		return bus.TaskResultPayload{
			TaskID: taskID, AttemptID: attemptID, State: StateReview, OK: true, ExitCode: res.ExitCode, Stdout: res.Stdout,
			Tokens: res.Tokens, Cost: res.Cost, OutputArtifact: outputArtifact,
		}, nil
	}

	// Terminal routing. An accepted irreversible (Tier-2) agent task whose run
	// was already consented to — via --authorize at submit, or by approving the
	// refusal's review, which re-queues the task carrying consent — completes
	// directly: that consent is the single explicit approval, and a second
	// sign-off on the finished result would add friction without adding
	// information, since the side effects already happened under it. Only the
	// anomalous case — a Tier-2 agent result that reached this branch without
	// authorization (the executor's gate refuses those, so this is defense in
	// depth) — still parks in review for human sign-off.
	if plan.Kind == "agent" && plan.Tier >= defense.TierIrreversible {
		parked := !task.Authorized
		// — Trace: the terminal Tier-2 agent outcome. Wire shape matches
		// design doc §3.1.1: operations: [{op,target,risk}].
		c.EvTrace(ctx, taskID, EvTier2Triggered, map[string]any{
			"operations":       []map[string]any{{"op": plan.Agent, "target": "", "risk": "medium"}},
			"kind":             plan.Kind,
			"tier":             plan.Tier,
			"result":           "authorized",
			"parked_in_review": parked,
		})
		if parked {
			if err := c.store.PauseWithResult(ctx, taskID, c.nodeID, map[string]any{
				"ok": true, "exit_code": res.ExitCode, "stdout": res.Stdout, "agent": res.Agent,
			}); err != nil {
				if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
					return bus.TaskResultPayload{}, ErrCancelled
				}
				return bus.TaskResultPayload{}, fmt.Errorf("pause for approval: %w", err)
			}
			c.logTask(task.Title, true)
			trackTask(c, task.Project, required, task.Title, true)
			return bus.TaskResultPayload{
				TaskID: taskID, AttemptID: attemptID, State: StateReview, OK: true, ExitCode: res.ExitCode, Stdout: res.Stdout,
				Tokens: res.Tokens, Cost: res.Cost, OutputArtifact: outputArtifact,
			}, nil
		}
		// Consent already on record: audit the auto-acceptance the way an
		// authorized native Tier-2 run is audited, then fall through to
		// Complete.
		c.audit(ctx, taskID, "agent:tier2", plan.Agent, "authorized", "completed under submit-time consent")
	}

	if err := c.store.Complete(ctx, taskID, c.nodeID, map[string]any{
		"ok": true, "exit_code": res.ExitCode, "stdout": res.Stdout,
	}); err != nil {
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
			return bus.TaskResultPayload{}, ErrCancelled
		}
		return bus.TaskResultPayload{}, fmt.Errorf("complete: %w", err)
	}
	c.logTask(task.Title, true)
	trackTask(c, task.Project, required, task.Title, true)
	return bus.TaskResultPayload{
		TaskID: taskID, AttemptID: attemptID, State: StateDone, OK: true, ExitCode: res.ExitCode, Stdout: res.Stdout,
		Tokens: res.Tokens, Cost: res.Cost, OutputArtifact: outputArtifact,
	}, nil
}

// taskScope extracts the declared scope from a task's persisted spec JSON
// (entry.TaskSpecDetail.Scope). A parse failure or absent field yields an
// empty scope, which disables drift checking for that task.
func taskScope(specJSON string) string {
	if specJSON == "" {
		return ""
	}
	var spec struct {
		Scope string `json:"scope"`
	}
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		return ""
	}
	return spec.Scope
}

// buildAgentPrompt assembles the full agent execution prompt — the memory
// file manifest (A3 selective loading) plus the task intent plus any matched
// skills (design §8.5) — and returns the skills that were actually loaded, so
// the caller can record their use. Skill matching keys off the short title,
// not the full intent, so a long instruction does not over-match on common
// words.
//
// Project memory (MEMORY.md) is deliberately NOT packed into the agent prompt
// anymore (A1 decision): it used to be prepended wholesale here, burning
// tokens on every task. Its replacement is the A3 manifest: outside a project
// the agent receives an index of the personal memory files (paths + one-line
// summaries) and reads what it needs with its own file tools; inside a project
// no memory is injected at all (D3 isolation wall).
func buildAgentPrompt(c *Core, intent, project, title string) (string, []*skills.Skill) {
	prompt, used := withSkills(c, intent, project, title)
	if project == "" && c.memory != nil {
		if files, err := c.memory.Manifest(); err == nil {
			if manifest := memory.RenderManifest(files); manifest != "" {
				prompt = manifest + "\n\n" + prompt
			}
		}
	}
	// Output style rider: the agent's final message is shown to the user
	// (often verbatim in a terminal) and may be spoken aloud by the voice
	// pipeline, so it must read as a direct answer — not a transcript of
	// the agent's exploration. Execution details stay in the task's event
	// stream (panda task <id>) for anyone who wants the full trail.
	prompt += "\n\n输出要求：最后用简洁的自然语言直接给出结果（做了什么、答案是什么）。不要罗列你的执行步骤、中间输出或思考过程；不要使用表情符号；标题/表格/代码块仅在内容确有需要时使用。"
	return prompt, used
}

// withSkills prepends matched active skills to the intent via the lightweight
// index (design §8.5 progressive loading): only a matched skill's full body is
// loaded, never the whole bank. Global skills apply everywhere; project skills
// apply within their own project. The returned slice is the set actually loaded.
func withSkills(c *Core, intent, project, query string) (string, []*skills.Skill) {
	if c.skills == nil {
		return intent, nil
	}
	if query == "" {
		query = intent // degenerate task with no title: fall back to intent
	}
	index, err := c.skills.Index()
	if err != nil {
		c.logger.Warn("load skill index", "err", err)
		return intent, nil
	}
	var matched []skills.IndexEntry
	matched = append(matched, skills.Match(index, skills.ScopeGlobal, "", query)...)
	if project != "" {
		matched = append(matched, skills.Match(index, skills.ScopeProject, project, query)...)
	}
	if len(matched) == 0 {
		return intent, nil
	}
	var b strings.Builder
	b.WriteString("可用技能（按需参考）：\n")
	var used []*skills.Skill
	for _, e := range matched {
		sk, err := c.skills.Load(e.Scope, e.Key, e.Name)
		if err != nil || sk == nil {
			continue
		}
		used = append(used, sk)
		fmt.Fprintf(&b, "## %s\n%s\n", sk.Name, sk.Body)
	}
	if len(used) == 0 {
		return intent, nil
	}
	return b.String() + "\n任务指令：\n" + intent, used
}

// logTask appends one daily-log line recording a task outcome. The daily log is
// the warm layer the Dreaming engine (design §17.3) consolidates from, so this
// is the point where task history becomes candidate long-term memory.
//
// The title originates from user/entry-model text, so the line goes through
// AppendExternal: its provenance is tainted and the Dreaming provenance gate
// will never promote it into MEMORY.md (P1-22) — otherwise instruction-shaped
// task text appearing 3 times in 3 days would be "consolidated" into the
// long-term memory that every future system prompt injects.
func (c *Core) logTask(title string, ok bool) {
	if c.daily == nil {
		return
	}
	status := "成功"
	if !ok {
		status = "失败"
	}
	if err := c.daily.AppendExternal(time.Now(), fmt.Sprintf("任务「%s」%s", title, status)); err != nil {
		c.logger.Warn("append daily log", "err", err)
	}
}

// recordSkillUse bumps the use counters of the skills that were loaded into an
// agent's prompt, so the skill lifecycle (dormant/expired) reflects real usage.
func recordSkillUse(c *Core, used []*skills.Skill, ok bool) {
	for _, sk := range used {
		sk.RecordUse(ok, time.Now())
		if err := c.skills.Save(sk); err != nil {
			c.logger.Warn("save skill use", "name", sk.Name, "err", err)
		}
	}
}

// trackTask feeds the skill tracker with one task outcome, so a recurring task
// class can eventually clear the quality gate and generate a skill (design §8.2).
func trackTask(c *Core, project string, required []string, title string, ok bool) {
	if c.tracker == nil {
		return
	}
	if _, err := c.tracker.Record(project, required, title, ok); err != nil {
		c.logger.Warn("track task for skill", "err", err)
	}
}

// audit records a high-risk operation in the audit log (P3-32). It never fails
// the execution path: a write error is logged and dropped, because audit must
// not break the hot loop.
func (c *Core) audit(ctx context.Context, taskID, what, target, result, detail string) {
	if c.auditLog == nil {
		return
	}
	if err := c.auditLog.Record(ctx, security.Entry{
		Who:    c.nodeID,
		What:   what,
		Target: target,
		Result: result,
		Detail: detail,
	}); err != nil {
		c.logger.Warn("audit record", "err", err)
	}
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
	if !c.isCurrentExecutor(ctx, t, env.From) {
		// Only the recorded dispatch target may claim the lease; otherwise any
		// authenticated peer could steal a dispatched task (P1-3).
		c.logger.Warn("accept from non-target ignored", "task", p.TaskID,
			"from", env.From, "owner", t.OwnerNode)
		return
	}
	// The executor claims the lease; the parent records the new state and
	// owner so a later cancel/transfer routes correctly.
	if err := c.store.Accept(ctx, p.TaskID, env.From); err != nil {
		c.logger.Debug("accept apply failed", "task", p.TaskID, "err", err)
	}
	c.relayToParent(ctx, bus.MsgTaskAccept, t.Chain, p)
}

// handleProgress processes a liveness beat from the node executing a task. The
// delegator's own copy of the task carries a lease stamped once at dispatch, so
// without this the origin node would expire a task whose executor is still
// legitimately working — and re-route the same work to a second node. The beat
// refreshes that lease and is relayed one hop further up, keeping every node on
// a multi-hop chain (Pi → Mac → Windows) in agreement that the work is alive.
func (c *Core) handleProgress(ctx context.Context, env bus.Envelope) {
	var p bus.TaskProgressPayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad task_progress", "err", err)
		return
	}
	t, err := c.store.Get(ctx, p.TaskID)
	if err != nil {
		c.logger.Debug("progress for unknown task", "task", p.TaskID)
		return
	}
	if Terminal(t.State) {
		return
	}
	if !c.isCurrentExecutor(ctx, t, env.From) {
		// Same authorization rule as accept/decline/result: only the node this
		// task was handed to may claim its work is alive, or any authenticated
		// peer could hold another node's task open indefinitely.
		c.logger.Warn("progress from non-executor ignored", "task", p.TaskID, "from", env.From)
		return
	}
	if p.AttemptID != "" && t.AttemptID != "" && p.AttemptID != t.AttemptID {
		// A beat from a superseded attempt (after a retry/transfer) must not
		// extend the current one's lease.
		return
	}
	if err := c.store.SetLease(ctx, p.TaskID, c.lease().Milliseconds()); err != nil {
		c.logger.Warn("refresh lease from progress", "task", p.TaskID, "err", err)
	}
	c.relayToParent(ctx, bus.MsgTaskProgress, t.Chain, p)
}

// isCurrentExecutor reports whether from is the node expected to report on
// this task: the recorded dispatch target (read from the EvDelegate audit
// event), or — once acceptance has moved the lease — the stored owner. Wire
// handlers use it as the post-authentication authorization check for
// task_accept / task_decline / task_result (P1-1/2/3).
func (c *Core) isCurrentExecutor(ctx context.Context, t Task, from string) bool {
	if from == t.OwnerNode && t.OwnerNode != c.nodeID {
		return true
	}
	target, err := c.store.DispatchTarget(ctx, t.TaskID)
	if err != nil {
		c.logger.Warn("dispatch target lookup", "task", t.TaskID, "err", err)
		return false
	}
	return from == target
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
	t, err := c.store.Get(ctx, p.TaskID)
	if err != nil {
		c.logger.Debug("decline for unknown task", "task", p.TaskID)
		return
	}
	if !c.isCurrentExecutor(ctx, t, env.From) {
		// Only the current executor may decline; otherwise any authenticated
		// peer could bounce someone else's dispatched task back to queued
		// forever — a cheap DoS (P1-2).
		c.logger.Warn("decline from non-executor ignored", "task", p.TaskID,
			"from", env.From, "owner", t.OwnerNode)
		return
	}
	c.logger.Info("task declined", "task", p.TaskID, "reason", p.Reason, "by", env.From)
	// Parent returns the task to queued for re-routing elsewhere.
	if err := c.store.Decline(ctx, p.TaskID, c.nodeID, p.Reason, env.From); err != nil {
		c.logger.Debug("decline apply failed", "task", p.TaskID, "err", err)
	}
	// P1-5: before giving up, try to re-route to the next-best node. A
	// successful re-dispatch means the outcome arrives via the new executor, so
	// the decline is neither relayed up nor signalled to a waiting Submit.
	if c.rerouteDeclined(ctx, p.TaskID) {
		return
	}
	c.relayToParent(ctx, bus.MsgTaskDecline, t.Chain, p)
	// Unblock a synchronous Submit that forwarded this task: it sees a failed
	// result (exit 1) rather than hanging forever with no capability anywhere.
	c.signalResult(p.TaskID, bus.TaskResultPayload{
		TaskID: p.TaskID, State: StateFailed, OK: false, ExitCode: 1, Stderr: "declined: " + p.Reason,
	})
}

// rerouteDeclined attempts to dispatch a declined task to the next-best node
// (P1-5, design §2.4 capacity-driven delegation). It returns true when the
// task was handed to a new executor; false means no route exists and the
// caller should propagate the decline upstream.
//
// Loop safety: every node that has ever declined the task (recorded on the
// EvDecline audit events) is excluded from candidacy, in addition to the
// delegation chain. Each re-route therefore strictly shrinks the candidate
// set, so a task cannot bounce between two declining nodes forever. The
// exclusion set is routing-local — the wire chain forwarded onward is left
// untouched, keeping relayToParent's predecessor walk intact.
func (c *Core) rerouteDeclined(ctx context.Context, taskID string) bool {
	t, err := c.store.Get(ctx, taskID)
	if err != nil {
		c.logger.Warn("reroute: load task", "task", taskID, "err", err)
		return false
	}
	// Only the node holding the (requeued) lease may re-route, and only a
	// queued task is re-routable — anything else means a concurrent
	// cancel/expire/accept already won.
	if t.State != StateQueued || t.OwnerNode != c.nodeID {
		return false
	}
	if len(t.Requires) == 0 {
		// Tasks persisted before requires_json have no routing key; fall back
		// to the previous propagate-the-decline behaviour.
		return false
	}

	excluded, err := c.store.DeclinedBy(ctx, taskID)
	if err != nil {
		c.logger.Warn("reroute: declined-by", "task", taskID, "err", err)
		return false
	}
	// Route's seen-set doubles as the exclusion list: chain nodes plus every
	// past decliner. Suffixing the chain keeps the persisted/wire chain clean.
	seenChain := append(slices.Clone(t.Chain), excluded...)

	decision := scheduler.Route(c.nodeID, seenChain, c.onlineEmployees(ctx), c.localMatch(), t.Requires,
		resourceRequirement(t.ResourceJSON), "")
	if decision.Action != scheduler.ActionForward {
		c.logger.Info("reroute: no alternate node", "task", taskID, "action", decision.Action)
		return false
	}

	// Rebuild a minimal delegate payload from the persisted row. Context is
	// carried by pointer: the new executor fetches the snapshot from the
	// context source if it does not already hold it.
	payload := bus.TaskDelegatePayload{
		TaskID:       t.TaskID,
		ParentID:     t.ParentID,
		Project:      t.Project,
		Title:        t.Title,
		Intent:       t.Intent,
		SpecJSON:     t.SpecJSON,
		Requires:     t.Requires,
		Complexity:   t.Complexity,
		Risk:         t.Risk,
		ResourceJSON: t.ResourceJSON,
		ContextType:  t.ContextType,
		ContextHash:  t.ContextHash,
		AttemptID:    t.AttemptID,
		Authorized:   t.Authorized,
	}
	if t.ContextHash != "" {
		payload.ContextLevel = "pointer"
	}
	if err := c.dispatchDelegated(ctx, taskID, decision.Target, payload, t.Chain); err != nil {
		c.logger.Warn("reroute: forward failed", "task", taskID, "target", decision.Target, "err", err)
		return false
	}
	c.logger.Info("rerouted declined task", "task", taskID, "to", decision.Target)
	return true
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
		//
		// The chain is a guess ([self, sender]): the result payload does not
		// carry the original delegation chain, so we cannot reconstruct the
		// real upstream path. Consequence: relayToParent sees self as the root
		// and does not relay this result further up. This is acceptable for the
		// MVP reconstruction path (a node that never persisted the task is, in
		// practice, the origin); a later phase should echo the original chain
		// on the wire and use it here.
		if p.AttemptID == "" {
			c.logger.Warn("result with empty attempt rejected", "task", p.TaskID, "from", env.From)
			return
		}
		if _, err := c.store.CreateFromRemote(ctx, p.TaskID, p.TaskID, c.nodeID, p.AttemptID, []string{c.nodeID, env.From}); err != nil {
			c.logger.Warn("create task from result", "task", p.TaskID, "err", err)
			return
		}
		t, _ = c.store.Get(ctx, p.TaskID)
	} else if err != nil {
		c.logger.Warn("load task for result", "task", p.TaskID, "err", err)
		return
	} else if !c.isCurrentExecutor(ctx, t, env.From) {
		// Authorization after authentication (P1-1): only the node this task
		// was dispatched to (or the current lease holder) may report its
		// result. Without this, any authenticated peer could forge a
		// task_result and terminate someone else's task with fake output.
		c.logger.Warn("result from non-executor ignored", "task", p.TaskID,
			"from", env.From, "owner", t.OwnerNode)
		return
	}
	if p.AttemptID == "" || (t.AttemptID != "" && t.AttemptID != p.AttemptID) {
		c.logger.Info("stale attempt result ignored", "task", p.TaskID,
			"stored", t.AttemptID, "got", p.AttemptID)
		return
	}
	state := p.State
	if state == "" {
		// Backward compatibility for nodes that predate the explicit state field.
		if p.OK {
			state = StateDone
		} else {
			state = StateFailed
		}
	}
	switch state {
	case StateDone:
		if !p.OK {
			c.logger.Warn("inconsistent task_result state", "task", p.TaskID, "state", state, "ok", p.OK)
			return
		}
		if err := c.store.CompleteFromRemote(ctx, p.TaskID, c.nodeID, p); err != nil {
			c.logger.Warn("complete from result", "task", p.TaskID, "err", err)
		}
	case StateReview:
		if err := c.store.ReviewFromRemote(ctx, p.TaskID, c.nodeID, p); err != nil {
			c.logger.Warn("review from result", "task", p.TaskID, "err", err)
		}
	case StateFailed:
		if err := c.store.FailFromRemote(ctx, p.TaskID, c.nodeID, p.Stderr); err != nil {
			c.logger.Warn("fail from result", "task", p.TaskID, "err", err)
		}
	case StateCancelled:
		if err := c.store.Cancel(ctx, p.TaskID); err != nil && !errors.Is(err, ErrConflict) {
			c.logger.Warn("cancel from result", "task", p.TaskID, "err", err)
		}
	default:
		c.logger.Warn("unknown task_result state ignored", "task", p.TaskID, "state", state)
		return
	}

	// Plan plane: the executor's artifact is the successors' input, so it is
	// adopted into this node's pool and recorded on the local row before deciding
	// what became ready. It is recorded for a review result too — the tree exists
	// either way, and the hash would otherwise be lost by the time a human
	// approves the stage.
	if t.PlanID != "" {
		c.adoptStageOutput(ctx, t, env.From, p.OutputArtifact)
	}

	// Record delegation outcome for scheduling analysis (B2). Only record when
	// this node actually delegated the task to the sender of the result.
	if target, err := c.store.DispatchTarget(ctx, p.TaskID); err == nil && target == env.From {
		delegateTs, err := c.store.LastDelegateTime(ctx, p.TaskID)
		if err != nil {
			c.logger.Warn("last delegate time", "task", p.TaskID, "err", err)
		}
		var latencyMs int64
		if delegateTs > 0 {
			latencyMs = (storage.Now() - delegateTs) * 1000
		}
		if err := c.store.RecordDelegationMetric(ctx, p.TaskID, string(c.nodeID), env.From,
			t.Requires, state == StateDone, latencyMs, p.Tokens); err != nil {
			c.logger.Warn("record delegation metric", "task", p.TaskID, "err", err)
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
	msgID, err := newUUID()
	if err != nil {
		c.logger.Warn("mint message id", "type", typ, "err", err)
		return
	}
	env, err := bus.NewEnvelope(typ, c.nodeID, msgID, payload)
	if err != nil {
		c.logger.Warn("build relay", "type", typ, "err", err)
		return
	}
	env.To = parent
	if err := c.sendTo(parent, env); err != nil {
		c.logger.Warn("relay", "type", typ, "to", parent, "err", err)
		// A terminal result must survive a disconnected parent (review P0-2):
		// park it for redelivery on the next hello instead of dropping it.
		if typ == bus.MsgTaskResult {
			if rp, ok := payload.(bus.TaskResultPayload); ok {
				c.outboxPersist(ctx, parent, rp)
			}
		}
		return
	}
	if typ == bus.MsgTaskResult {
		// Delivered now: clear any copy parked from an earlier failed attempt.
		if rp, ok := payload.(bus.TaskResultPayload); ok {
			c.outboxDrop(ctx, parent, rp.TaskID)
		}
	}
}

// handleCancel processes a task_cancel request. Only the task's current owner
// or its immediate parent in the delegation chain may cancel it; a cancel from
// any other node is an unauthorized cross-node cancel and is dropped.
func (c *Core) handleCancel(ctx context.Context, env bus.Envelope) {
	var p bus.TaskCancelPayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad task_cancel", "err", err)
		return
	}
	t, err := c.store.Get(ctx, p.TaskID)
	if err != nil {
		c.logger.Debug("cancel for unknown task", "task", p.TaskID)
		return
	}
	parent := scheduler.Predecessor(t.Chain, c.nodeID)
	if env.From != t.OwnerNode && env.From != parent {
		c.logger.Warn("unauthorized cancel ignored", "task", p.TaskID,
			"from", env.From, "owner", t.OwnerNode, "parent", parent)
		return
	}
	cancelled, err := c.store.CancelCascade(ctx, p.TaskID)
	if err != nil {
		c.logger.Warn("cancel failed", "task", p.TaskID, "err", err)
	}
	c.finishCancel(ctx, cancelled)
}

// finishCancel runs the post-cascade cleanup for a cancelled task set: abort the
// local execution so a cancelled task stops doing work instead of only losing
// its database row, drop paused-context entries so a waiting_context task
// cancelled mid-fetch does not leak in pendingCtx (P2-7), and propagate the
// cancel to any remote executors holding dispatch leases (P2-3).
func (c *Core) finishCancel(ctx context.Context, cancelled []string) {
	for _, id := range cancelled {
		c.cancelRunning(id)
		c.pendingCtx.Delete(id)
		c.forwardCancelDownstream(ctx, id)
	}
}

// CancelTree is the local-entry cancel: cascade locally, then notify
// downstream executors. The CLI and any in-process caller share the exact
// post-cancel behaviour of the wire handler.
func (c *Core) CancelTree(ctx context.Context, taskID string) error {
	cancelled, err := c.store.CancelCascade(ctx, taskID)
	if err != nil {
		return err
	}
	c.finishCancel(ctx, cancelled)
	return nil
}

// forwardCancelDownstream propagates a cancel to the remote executor holding
// the dispatch lease, if any (P2-3). Without it, cancelling a delegated task
// cancelled only the delegator's local copy: the executor kept running to
// completion and its eventual result landed on an already-cancelled task.
// The receiver's handleCancel re-runs its own cascade-and-forward, so the
// cancel walks the whole downstream chain hop by hop.
func (c *Core) forwardCancelDownstream(ctx context.Context, taskID string) {
	target, err := c.store.DispatchTarget(ctx, taskID)
	if err != nil {
		c.logger.Warn("cancel: dispatch target lookup", "task", taskID, "err", err)
		return
	}
	if target == "" || target == c.nodeID {
		return // never dispatched, or dispatched to ourselves
	}
	msgID, err := newUUID()
	if err != nil {
		c.logger.Warn("cancel: mint message id", "task", taskID, "err", err)
		return
	}
	env, err := bus.NewEnvelope(bus.MsgTaskCancel, c.nodeID, msgID, bus.TaskCancelPayload{
		TaskID: taskID, Reason: "cancelled by delegator",
	})
	if err != nil {
		c.logger.Warn("cancel: build envelope", "task", taskID, "err", err)
		return
	}
	env.To = target
	if err := c.sendTo(target, env); err != nil {
		// The executor may be gone; its lease expiry will clean up regardless.
		c.logger.Warn("cancel: forward downstream", "task", taskID, "to", target, "err", err)
		return
	}
	c.logger.Info("cancel forwarded downstream", "task", taskID, "to", target)
}

// handleResume processes an incoming task_resume: the delegator's user
// approved the tier-2 consent this node's defense layer refused to run
// without, and the re-run belongs here — on the node that holds the task
// (its capability match, context snapshot, and worktree) rather than
// wherever the approval was given. Only the task's immediate predecessor in
// the delegation chain may grant that consent (authorization after
// authentication, the same rule handleCancel applies); a resume from any
// other node is dropped.
func (c *Core) handleResume(ctx context.Context, env bus.Envelope) {
	var p bus.TaskResumePayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad task_resume", "err", err, "from", env.From)
		return
	}
	t, err := c.store.Get(ctx, p.TaskID)
	if err != nil {
		c.logger.Debug("resume for unknown task", "task", p.TaskID, "from", env.From)
		return
	}
	if parent := scheduler.Predecessor(t.Chain, c.nodeID); env.From != parent {
		c.logger.Warn("resume from non-delegator ignored", "task", p.TaskID,
			"from", env.From, "parent", parent)
		return
	}
	if p.AttemptID != "" && t.AttemptID != "" && t.AttemptID != p.AttemptID {
		c.logger.Info("stale attempt resume ignored", "task", p.TaskID,
			"stored", t.AttemptID, "got", p.AttemptID)
		return
	}
	if t.State != StateReview {
		// Answer honestly instead of leaving the delegator waiting on its
		// lease timeout: a task that moved on (timed out, cancelled, already
		// re-run) cannot take the consent, and a failed result is the one
		// state every delegator path already renders.
		c.logger.Warn("resume for task not in review", "task", p.TaskID, "state", t.State)
		c.relayToParent(ctx, bus.MsgTaskResult, t.Chain, bus.TaskResultPayload{
			TaskID: t.TaskID, AttemptID: t.AttemptID, State: StateFailed, OK: false, ExitCode: 1,
			Stderr: "resume: task state is " + t.State,
		})
		return
	}
	// Re-run asynchronously so the message loop stays responsive to
	// task_cancel while the (potentially long) agent run proceeds. The
	// outcome travels back over the normal task_result path.
	go func() {
		final, result, rerr := c.ResumeApproved(ctx, p.TaskID)
		if rerr != nil {
			result = bus.TaskResultPayload{
				TaskID: p.TaskID, AttemptID: t.AttemptID, State: StateFailed, OK: false, ExitCode: 1,
				Stderr: "resume: " + rerr.Error(),
			}
		}
		// ResumeApproved can return without a wire-shaped outcome when a
		// local queue scheduler raced the just-approved task (its dispatch
		// lost the CAS): report the row's state so the delegator's wait ends
		// with the truth instead of an empty result it must guess at.
		switch result.State {
		case StateDone, StateReview, StateFailed, StateCancelled:
		default:
			result = bus.TaskResultPayload{
				TaskID: p.TaskID, AttemptID: t.AttemptID, State: StateFailed, OK: false, ExitCode: 1,
				Stderr: "resume: task claimed by " + final.State + " before re-run",
			}
		}
		c.relayToParent(ctx, bus.MsgTaskResult, t.Chain, result)
	}()
}

// reply sends a message back to the sender of env.
func (c *Core) reply(ctx context.Context, env bus.Envelope, typ string, payload any) error {
	msgID, err := newUUID()
	if err != nil {
		return err
	}
	envOut, err := bus.NewEnvelope(typ, c.nodeID, msgID, payload)
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

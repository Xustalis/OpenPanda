package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xenith/panda/internal/bus"
	"github.com/xenith/panda/internal/defense"
	"github.com/xenith/panda/internal/ledger"
	"github.com/xenith/panda/internal/scheduler"
	"github.com/xenith/panda/internal/security"
	"github.com/xenith/panda/internal/skills"
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
	decision := scheduler.Route(c.nodeID, chain, c.onlineEmployees(ctx), c.localMatch(), required, p.PreferredNode)

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

	prompt, usedSkills := buildAgentPrompt(c, intent, task.Project, task.Title)

	// Scope drift (design §14.2 signal A): for an agent task that declares a
	// scope, snapshot the working directory before execution so changes outside
	// the scope can be intercepted rather than silently committed. The agent
	// runs inside c.workDir, which the sandbox also confines it to.
	scope := defense.NewScope(taskScope(task.SpecJSON))
	var before defense.Snapshot
	if plan.Kind == "agent" && !scope.Empty() {
		var err error
		if before, err = defense.SnapshotDir(c.workDir); err != nil {
			c.logger.Warn("snapshot workdir before agent", "task", taskID, "err", err)
		}
	}

	res := c.router.Execute(ctx, plan, prompt, c.workDir, task.Authorized)
	if plan.Kind == "agent" {
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

	// Scope-drift intercept: a successful agent that touched files outside its
	// declared scope has overstepped the task. Pause it for human analysis
	// (design §14.2 "拦截 → 暂停 → 分析") rather than mark it done or fail it
	// into the retry loop — a deterministic intercept will not improve on retry.
	if plan.Kind == "agent" && !scope.Empty() && res.OK {
		after, err := defense.SnapshotDir(c.workDir)
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
				TaskID: taskID, AttemptID: attemptID, OK: false, ExitCode: 1, Stderr: msg,
			}, nil
		}
	}

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
		c.logTask(task.Title, true)
		trackTask(c, task.Project, required, task.Title, true)
		return bus.TaskResultPayload{TaskID: taskID, AttemptID: attemptID, OK: true, ExitCode: 0, Stdout: res.Stdout}, nil
	}

	if !res.OK {
		if err := c.store.Fail(ctx, taskID, c.nodeID, res.Stderr); err != nil {
			if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
				return bus.TaskResultPayload{}, ErrCancelled
			}
			return bus.TaskResultPayload{}, fmt.Errorf("fail: %w", err)
		}
		c.logTask(task.Title, false)
		trackTask(c, task.Project, required, task.Title, false)
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
	c.logTask(task.Title, true)
	trackTask(c, task.Project, required, task.Title, true)
	return bus.TaskResultPayload{
		TaskID: taskID, AttemptID: attemptID, OK: true, ExitCode: res.ExitCode, Stdout: res.Stdout,
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

// withProjectMemory prepends a project's own MEMORY.md to the agent intent so
// the execution context carries the project's conventions (design §17.2). Only
// project memory is packed here — Hermes memory never crosses the isolation
// wall. A nil injector, an empty project, a load error, or empty memory all
// leave the intent unchanged.
func withProjectMemory(c *Core, intent, project string) string {
	if c.memory == nil || project == "" {
		return intent
	}
	mem, err := c.memory.ContextPack(project)
	if err != nil {
		c.logger.Warn("load project memory", "project", project, "err", err)
		return intent
	}
	if mem == "" {
		return intent
	}
	return "以下为本项目记忆（仅本项目执行参考）：\n" + mem + "\n\n任务指令：\n" + intent
}

// buildAgentPrompt assembles the full agent execution prompt — the task intent
// plus, in order, the project's own memory (design §17.2) and any matched
// skills (design §8.5) — and returns the skills that were actually loaded, so
// the caller can record their use. Skill matching keys off the short title, not
// the full intent, so a long instruction does not over-match on common words.
func buildAgentPrompt(c *Core, intent, project, title string) (string, []*skills.Skill) {
	intent = withProjectMemory(c, intent, project)
	return withSkills(c, intent, project, title)
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
func (c *Core) logTask(title string, ok bool) {
	if c.daily == nil {
		return
	}
	status := "成功"
	if !ok {
		status = "失败"
	}
	if err := c.daily.Append(time.Now(), fmt.Sprintf("任务「%s」%s", title, status)); err != nil {
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
		//
		// The chain is a guess ([self, sender]): the result payload does not
		// carry the original delegation chain, so we cannot reconstruct the
		// real upstream path. Consequence: relayToParent sees self as the root
		// and does not relay this result further up. This is acceptable for the
		// MVP reconstruction path (a node that never persisted the task is, in
		// practice, the origin); a later phase should echo the original chain
		// on the wire and use it here.
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
	// Drop paused-context entries for the cancelled tasks so a waiting_context
	// task cancelled mid-fetch does not leak in pendingCtx (P2-7).
	for _, id := range cancelled {
		c.pendingCtx.Delete(id)
	}
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

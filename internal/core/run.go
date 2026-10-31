// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/contract"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/providers"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
	"github.com/Xustalis/OpenPanda/internal/scheduler/queue"
	"github.com/Xustalis/OpenPanda/internal/skills"
)

// progressInterval is the minimum spacing between EvProgress recordings:
// dense enough to feel live in `panda task` / the panel timeline, sparse
// enough that a chatty adapter cannot flood the event chain.
const progressInterval = 2 * time.Second

// maxAgentTranscriptEvents bounds how many structured agent_event rows one
// task records: a transcript beyond that is pathological, not informative —
// past the cap one transcript_truncated marker notes the cut.
const maxAgentTranscriptEvents = 2000

// execute runs the shared post-creation pipeline for a task that needs no
// context fetch: queue → dispatch → run. Both the WebSocket delegation path and
// the local entry path (SubmitLocal/Submit) funnel through here so there is
// exactly one execution implementation. The task must already be persisted
// (with detail) by the caller.
//
// releaseSlot, when non-nil, frees the capacity reservation the delegator
// admission took for this task; run consumes it the moment the row claims a
// countable slot of its own. Local submissions pass nil — they never reserved.
func (c *Core) execute(ctx context.Context, taskID, intent string, required []string, releaseSlot func()) (bus.TaskResultPayload, error) {
	if err := c.prepare(ctx, taskID); err != nil {
		if releaseSlot != nil {
			releaseSlot()
		}
		return bus.TaskResultPayload{}, err
	}
	return c.run(ctx, taskID, intent, required, releaseSlot)
}

// prepare records a freshly-created task in the local queue and dispatches it
// to this node, so the queue reflects it even if the process dies mid-run.
// The transitions tolerate a row another path already moved — a plan sweep
// that raced the delegate, a duplicate delivery's first invocation: a task
// already queued/dispatched/running is one this path can still adopt, and
// declining it over a bookkeeping race is how a delegated task "never
// completes". A terminal row is the one real conflict (the delegator
// cancelled, or another verdict won) and reports as ErrCancelled so the
// caller stands down without a decline.
func (c *Core) prepare(ctx context.Context, taskID string) error {
	if err := c.store.Queue(ctx, taskID, c.nodeID); err != nil && !errors.Is(err, ErrConflict) {
		return fmt.Errorf("queue: %w", err)
	}
	if err := c.store.Dispatch(ctx, taskID, c.nodeID, c.nodeID); err != nil && !errors.Is(err, ErrConflict) {
		return fmt.Errorf("dispatch: %w", err)
	}
	t, err := c.store.Get(ctx, taskID)
	if err != nil {
		return fmt.Errorf("load task: %w", err)
	}
	switch t.State {
	case StateQueued, StateDispatched, StateRunning, StateWaitingCtx:
		return nil
	}
	if Terminal(t.State) {
		return ErrCancelled
	}
	return fmt.Errorf("queue: %w: task %s state=%s, want submitted", ErrConflict, taskID, t.State)
}

// storeWriteCtx returns a context bounded to 5 seconds that survives cancellation
// of ctx, ensuring task terminal status and event writes are committed to SQLite.
func (c *Core) storeWriteCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

// lockActuator takes the per-actuator execution mutex for id and returns its
// release. See Core.actuatorLocks for why a lock — not only a queue resource
// key — is the right primitive for physical-device exclusivity.
func (c *Core) lockActuator(id string) func() {
	v, _ := c.actuatorLocks.LoadOrStore(id, &sync.Mutex{})
	m := v.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

// run accepts (or resumes) a dispatched task into running, executes it, and
// records the outcome. The task may already be running (a context-fetch resume
// moved it there), dispatched (the normal path), or waiting_context (resumed
// here after the snapshot arrived).
//
// releaseSlot frees the delegated-admission capacity reservation. It fires the
// moment the row occupies a countable slot (running) — from then on CountActive
// accounts for it and holding the reservation too would double-count. On any
// early exit the defer releases it instead, so a run that never reaches running
// still frees its slot.
func (c *Core) run(ctx context.Context, taskID, intent string, required []string, releaseSlot func()) (out bus.TaskResultPayload, rerr error) {
	if releaseSlot == nil {
		releaseSlot = func() {}
	}
	defer releaseSlot()
	// Structured attribution: every result payload constructed below is
	// stamped on the way out with who executed it, under which delegation
	// chain, and how long it took on this node's clock. Relay hops rewrite
	// env.From but never these fields, so the origin can consume a
	// cross-device result like an ordinary sub-agent's (S1-3).
	start := time.Now()
	var taskChain []string
	// execWorkDir is the workspace the run actually derived; stamped onto
	// every result on the way out so the delegator can see WHERE the work
	// happened, not just who reported it (set after prepareRunDir below).
	var execWorkDir string
	// attemptID is bound once claimForRun succeeds; the stranded-run cleanup
	// below scopes its verdict to it so a superseded attempt's late write can
	// never land on a rotated row. claimed records that the running state —
	// if the row shows one — belongs to THIS run: a failure before the claim
	// must never touch a row someone else legitimately runs.
	var attemptID string
	var claimed bool
	defer func() {
		if out.TaskID != "" {
			out.DurationMS = time.Since(start).Milliseconds()
			out.Executor = c.nodeID
			out.WorkDir = execWorkDir
			if len(out.Chain) == 0 {
				out.Chain = taskChain
			}
		} else if rerr != nil && !errors.Is(rerr, ErrAlreadyRunning) && claimed {
			// If run failed or cancelled before reaching a terminal state,
			// fail the task in the store so it never lingers stranded in
			// 'running'. ErrAlreadyRunning is excluded: the row is legitimately
			// running under another goroutine, and failing it here would kill
			// that owner's work mid-flight.
			wCtx, cancel := c.storeWriteCtx(ctx)
			defer cancel()
			// Attempt-scoped: if the row was lease-rerouted and already
			// re-ran to running under a rotated attempt_id, this stranded-run
			// cleanup must not touch it — the fresh runner owns the verdict.
			if t, err := c.store.Get(wCtx, taskID); err == nil && t.State == StateRunning &&
				t.AttemptID == attemptID {
				if ferr := c.store.FailAttempt(wCtx, taskID, c.nodeID, attemptID, rerr.Error()); ferr != nil {
					c.logger.Error("fail stranded run", "task", taskID, "err", ferr)
				}
			}
		}
	}()

	plan, router, breakerKey, err := c.resolveRunPlan(ctx, taskID, required)
	if err != nil {
		return bus.TaskResultPayload{}, err
	}
	task, unlockActuator, err := c.claimForRun(ctx, taskID, intent, &plan, releaseSlot)
	if err != nil {
		return bus.TaskResultPayload{}, err
	}
	claimed = true
	// Bind the attempt at claim time — before any fallible step — so the
	// stranded-run cleanup above and every verdict write below stay scoped
	// to exactly the dispensation this run owns.
	attemptID = task.AttemptID
	defer unlockActuator()
	taskChain = task.Chain

	// Trust-zone executor-side credential guard (experimental): the task's
	// admission tier — folded over its whole stored chain — decides whether
	// THIS node's model credentials may reach the agent subprocess env. The
	// send-side clamp strips what leaves the origin; this is the inbound
	// mirror: a sub-mesh path's task text is untrusted input, and an env
	// carrying an injected API key (or a forwarded adapter credential) is
	// one `printenv` away from the wire.
	withholdCreds := c.modelCredsWithheld(ctx, task)
	if withholdCreds {
		c.audit(ctx, taskID, "trust:creds_withheld", c.nodeID, "applied",
			"sub-mesh admission tier: model credentials kept out of adapter env")
	}

	// Notify the delegator that this node has accepted the task so its copy
	// transitions dispatched -> running and can time the execution (D3). A local
	// task (chain = [self]) has no predecessor and relayToParent is a no-op.
	//
	// Trust zones sign the acceptance bilaterally (experimental): the frame
	// binds task + attempt + this node id + a timestamp to our identity key,
	// so a relay further up the chain can verify who actually took the work
	// instead of trusting whoever happens to forward the frame. Peers that
	// predate the fields send an unsigned accept, which verifyTaskAccept
	// accepts for backward compatibility.
	accept := bus.TaskAcceptPayload{TaskID: taskID, AttemptID: task.AttemptID}
	if pub, priv, ok := c.nodeKeyPair(); ok && priv != nil {
		accept.Ts = time.Now().Unix()
		accept.Pub = hex.EncodeToString(pub)
		// The signed identity is the stable key id, not the instance id:
		// every hop can derive "k:"+pub from the frame itself and verify
		// without knowing which instance id the executor runs under today.
		accept.Sig = bus.SignAcceptance(priv, taskID, task.AttemptID,
			stableIDPrefix+accept.Pub, accept.Ts)
	}
	c.relayToParent(ctx, bus.MsgTaskAccept, task.Chain, accept)

	// The result payload stamps this attempt id; the delegator uses it to
	// reject stale results after a transfer/retry. (Bound at claim above.)

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
	execCtx = commander.WithTaskID(execCtx, taskID)
	if withholdCreds {
		execCtx = commander.WithModelCredsWithheld(execCtx)
	}

	// Model-injection policy check (A1): decided once before the supervision
	// loop — the adapter (and therefore the plan) is identical across rounds,
	// so the decision does not vary between an initial run and a re-delegation.
	var injection commander.InjectionDecision
	if plan.Kind == "agent" {
		if router != nil {
			injection = router.InjectionDecision(plan.Agent, plan.Adapter)
			if withholdCreds && injection.Inject {
				injection = commander.InjectionDecision{
					Inject: false,
					Reason: "trust zone: model credentials withheld from sub-mesh origin",
				}
			}
		}
	}

	prep, err := c.prepareRunDir(execCtx, ctx, task, taskID, plan)
	if err != nil {
		return bus.TaskResultPayload{}, err
	}
	execWorkDir = prep.workDir
	// Persist where this run happens so the queue board, `task show` and the
	// panel can answer "where did it run?" — the row's work_dir is only a
	// submitter pin and is empty for ordinary tasks.
	if execWorkDir != "" {
		if werr := c.store.SetExecWorkDir(context.WithoutCancel(ctx), taskID, execWorkDir); werr != nil {
			c.logger.Warn("persist exec work dir", "task", taskID, "err", werr)
		}
	}
	if prep.negoRelease != nil {
		defer prep.negoRelease()
	}
	var execCleanup func()
	execCtx, execCleanup = c.decorateExecCtx(execCtx, ctx, taskID, task, plan)
	defer execCleanup()

	// Delivery contract (P0): run the declared acceptance checks before the
	// agent touches the tree. The baseline is evidence, not a gate — it is
	// what lets the final verdict say "this was already failing" instead of
	// blaming the run for a red suite it inherited.
	var acceptanceChecks []contract.Check
	var acceptanceBaseline []contract.Result
	if plan.Kind == "agent" || plan.Kind == "native" {
		acceptanceChecks = taskAcceptance(task.SpecJSON)
		if len(acceptanceChecks) > 0 {
			c.EvTrace(execCtx, taskID, EvContractSet, map[string]any{"checks": acceptanceChecks})
			acceptanceBaseline = c.runAcceptance(execCtx, taskID, acceptancePhaseBaseline, prep.workDir, acceptanceChecks)
		}
	}

	work := &runWork{
		c: c, taskID: taskID, intent: intent, required: required,
		task: task, plan: plan, router: router, breakerKey: breakerKey, injection: injection,
		execCtx: execCtx, attemptID: attemptID,
		workDir: prep.workDir, oscKey: prep.oscKey, scope: prep.scope,
		before: prep.before, beforeOK: prep.beforeOK,
		shadowConflicts: prep.shadowConflicts,
		checks:          acceptanceChecks, baseline: acceptanceBaseline,
	}
	outcome, err := work.executeRounds(ctx)
	if err != nil {
		return bus.TaskResultPayload{}, err
	}
	if outcome.done {
		return outcome.payload, nil
	}
	return c.finalizeRun(ctx, taskID, task, work.plan, required, attemptID, prep.workDir,
		outcome.res, outcome.lastChanged, outcome.filesTruncated, outcome.verdict,
		outcome.sessionID, outcome.sessionAdapter,
		acceptanceChecks, acceptanceBaseline)
}

// taskSpec reads a task's persisted spec once for the routing decisions that
// need it — the roster and the permission mode both live there, and two point
// reads of the same row answered one question twice. A missing row or an
// unreadable spec yields the zero value: no roster, auto permission.
func (c *Core) taskSpec(ctx context.Context, taskID string) entry.TaskSpecDetail {
	t, err := c.store.Get(ctx, taskID)
	if err != nil || t.SpecJSON == "" {
		return entry.TaskSpecDetail{}
	}
	var spec entry.TaskSpecDetail
	if err := json.Unmarshal([]byte(t.SpecJSON), &spec); err != nil {
		return entry.TaskSpecDetail{}
	}
	return spec
}

// planModeAgent picks the agent a plan-mode task can actually be staffed on.
// The commander refuses a restricted request to an adapter with no read-only
// tool face (fail closed), so a plan-mode primary that lacks one would fail
// every run with a hint — "authorize the task" — that cannot help: the
// conversation itself is what demands read-only. Swap in the first capable
// alternate; if the route has none, say so in terms the user can act on.
func planModeAgent(router *commander.Router, plan commander.Plan) (commander.Plan, error) {
	if commander.AdapterSupportsRestricted(plan.Adapter) {
		return plan, nil
	}
	for i, alt := range plan.Alternates {
		ag, ok := router.Agent(alt)
		if !ok || !commander.AdapterSupportsRestricted(ag.Adapter) {
			continue
		}
		rest := make([]string, 0, len(plan.Alternates))
		rest = append(rest, plan.Agent)
		rest = append(rest, plan.Alternates[:i]...)
		rest = append(rest, plan.Alternates[i+1:]...)
		plan.Alternates = rest
		plan.Agent = alt
		plan.Ability = alt
		plan.Adapter = ag.Adapter
		plan.Tier = ag.Tier
		if plan.Tier == 0 {
			plan.Tier = defense.TierReversible
		}
		return plan, nil
	}
	return plan, fmt.Errorf(
		"plan mode needs an agent that can run read-only, and %s cannot on this node; pick another agent for the thread or change the permission mode",
		plan.Agent)
}

// tightenForPermission raises the plan's tier to what the conversation's
// permission mode demands. It runs after every failover and affinity
// promotion, so a promoted alternate cannot hand back the autonomy the mode
// took away. Bypass needs nothing here — the ask engine already consented —
// and auto/edits keep the tier the command itself scored.
//
//   - manual: every native command and every agent delegation waits for a
//     foreground answer; only reads and pure answers stay free.
//   - plan: native commands wait too, and the agent runs with the read-only
//     tool face (applied on the exec context, see execContextFor).
func tightenForPermission(plan *commander.Plan, mode string) {
	switch defense.NormalizeMode(mode) {
	case defense.ModeManual:
		if plan.Kind == "native" || plan.Kind == "agent" {
			plan.Tier = defense.TierIrreversible
		}
	case defense.ModePlan:
		if plan.Kind == "native" {
			plan.Tier = defense.TierIrreversible
		}
	}
}

// rosterHonoursPins refuses a task whose requires pin an agent the thread's
// roster excludes.
//
// The roster is the user's own pick and still outranks a model-emitted pin —
// but a pin is also the only place a task says WHICH employee it is for. When
// the two disagree the task is contradictory, not merely mis-routed: running it
// on a roster member leaves the target text ("output a line proving
// claude_code works") pointing at an agent that never started, and the agent
// that did run has no way to know the line is a lie. Failing closed keeps the
// roster authoritative and keeps a pass attributable to the agent that earned
// it.
//
// A pin naming an agent this node does not carry at all is refused too: the
// roster cannot honour it, and the substitute would be just as unrelated. The
// raw name is compared after the same normalisation resolveAgentName applies
// (case, "-" vs "_"), so "claude-code" and "claude_code" are one agent.
func rosterHonoursPins(router *commander.Router, roster, required []string) error {
	for _, req := range required {
		name, pinned, known := router.PinnedAgentName(req)
		if !pinned {
			continue
		}
		if !known {
			name = strings.TrimPrefix(req, "agent:")
		}
		if !rosterIncludes(roster, name) {
			return fmt.Errorf("task pins agent %q, which this thread's roster (%s) does not include",
				name, strings.Join(roster, ", "))
		}
	}
	return nil
}

// rosterIncludes matches a pin against the roster the way routing does: exact
// key first, then the case/dash-normalised form.
func rosterIncludes(roster []string, name string) bool {
	want := strings.ReplaceAll(strings.ToLower(name), "-", "_")
	for _, n := range roster {
		if n == name {
			return true
		}
		if strings.ReplaceAll(strings.ToLower(n), "-", "_") == want {
			return true
		}
	}
	return false
}

// resolveRunPlan turns a task's requires into an executable plan: route
// selection, the just-in-time usability and circuit-breaker gates, and the
// failover/alternate pruning those gates imply. It is run()'s first phase —
// everything that can still be decided without touching the store.
func (c *Core) resolveRunPlan(ctx context.Context, taskID string, required []string) (commander.Plan, *commander.Router, string, error) {
	router := c.currentRouter()
	if router == nil {
		// No capability card loaded: nothing to execute.
		return commander.Plan{}, nil, "", fmt.Errorf("no commander configured")
	}
	// A thread's roster (the composer's "who works on this" pick) narrows the
	// agent pool before anything is routed: a conversation told to use these
	// hands must not quietly hand the work to someone else. The roster rides
	// spec_json, so a task delegated to another node arrives with the same
	// restriction instead of being re-opened to every agent there.
	spec := c.taskSpec(ctx, taskID)
	if roster := spec.AllowAgents; len(roster) > 0 {
		// "The roster wins" used to mean "rank the roster instead", which
		// swapped the employee while leaving the task's own text intact. A task
		// that names an agent is a task ABOUT that agent: three "test
		// agent:<name>" tasks pinned to claude_code, codex and deepseek_harness
		// ran on a [deepseek_harness, opencode] roster, all three executed on
		// deepseek_harness, and each printed "<name> 调用成功" for an agent that
		// never started — three green passes, two of them fiction. Refuse the
		// mismatch instead: nothing runs, and the reason names both sides.
		if err := rosterHonoursPins(router, roster, required); err != nil {
			return commander.Plan{}, nil, "", err
		}
		// A roster naming only agents this node does not carry cannot be
		// staffed here. WithAgents degrades to the unrestricted card in that
		// case (by design: the pick is mesh-wide and the task may have been
		// meant for a peer), but this node has already been chosen to execute,
		// so degrading means running on an agent the user explicitly did not
		// pick. Say so instead — "grok was never tested" reads very differently
		// once the reason is "grok_build is not installed on xin".
		if len(router.RosterOnCard(roster)) == 0 {
			return commander.Plan{}, nil, "", fmt.Errorf(
				"this thread's roster (%s) names no agent installed on this node; install one of them or clear the team",
				strings.Join(roster, ", "))
		}
		router = router.WithAgents(roster)
	}
	plan, err := router.Route(required)
	if err != nil {
		return commander.Plan{}, nil, "", fmt.Errorf("route: %w", err)
	}
	// Re-check dispatch readiness at execution time: a task can be accepted
	// while its harnesses were healthy and run after the provider went dark
	// (or after a queue park). Failing here — like a route miss — declines a
	// delegated task so the parent re-routes it, and fails a local task fast
	// with an actionable reason instead of parking it inside a dead adapter.
	if plan.Kind == "agent" && !router.PlanUsable(plan) {
		reason := router.PlanUnusableReason(plan)
		if reason == "" {
			reason = "cli/credentials/endpoint check failed"
		}
		return commander.Plan{}, nil, "", fmt.Errorf("route: no usable agent for %v on this node (%s)", required, reason)
	}
	// Circuit breaker (P2-27): refuse to run an agent that has been failing
	// repeatedly, before the task leaves its dispatched state, so the parent
	// can re-route it elsewhere instead of it stalling in running.
	var breakerKey string
	if plan.Kind == "agent" {
		breakerKey = "agent:" + plan.Agent
		if !c.breaker.Allow(breakerKey) {
			// If the primary agent is circuit-open, try to fail over to the first healthy alternate.
			promoted := false
			for i, alt := range plan.Alternates {
				if c.breaker.Allow("agent:" + alt) {
					if altAg, ok := router.Agent(alt); ok {
						c.audit(ctx, taskID, "agent:spawn", plan.Agent, "open", "circuit open; failing over to "+alt)
						plan.Agent = alt
						plan.Ability = alt
						plan.Adapter = altAg.Adapter
						plan.Tier = altAg.Tier
						if plan.Tier == 0 {
							plan.Tier = defense.TierReversible
						}
						newAlts := make([]string, 0, len(plan.Alternates)-1)
						newAlts = append(newAlts, plan.Alternates[:i]...)
						newAlts = append(newAlts, plan.Alternates[i+1:]...)
						plan.Alternates = newAlts
						breakerKey = "agent:" + alt
						promoted = true
						break
					}
				}
			}
			if !promoted {
				c.audit(ctx, taskID, "agent:spawn", plan.Agent, "open", "circuit open")
				return commander.Plan{}, nil, "", fmt.Errorf("agent %s circuit open", plan.Agent)
			}
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
		// Checkpoint affinity: a task carrying a live agent session resumes
		// that conversation ONLY on the adapter that minted it — the offer
		// is adapter-affine (buildAdapterRequest drops a foreign handle).
		// Route scores from scratch every attempt, so a checkpoint retry or
		// a yield/restart can name a different primary and the resume dies
		// on the floor: the new agent cold-starts and re-pays the whole
		// orientation the session exists to save. When a routed alternate
		// owns the minting adapter, promote it to primary so the checkpoint
		// actually resumes; a candidate that is not on the route at all is
		// not forced — a stale handle cannot resurrect a dead adapter.
		if t, terr := c.store.Get(ctx, taskID); terr == nil &&
			t.AgentSessionID != "" && t.AgentSessionAdapter != "" {
			if cur, ok := router.Agent(plan.Agent); ok && cur.Adapter != t.AgentSessionAdapter {
				for i, alt := range plan.Alternates {
					ag, ok := router.Agent(alt)
					if !ok || ag.Adapter != t.AgentSessionAdapter || !c.breaker.Allow("agent:"+alt) {
						continue
					}
					prev := plan.Agent
					newAlts := make([]string, 0, len(plan.Alternates))
					newAlts = append(newAlts, prev)
					newAlts = append(newAlts, plan.Alternates[:i]...)
					newAlts = append(newAlts, plan.Alternates[i+1:]...)
					plan.Alternates = newAlts
					plan.Agent = alt
					plan.Ability = alt
					plan.Adapter = ag.Adapter
					plan.Tier = ag.Tier
					if plan.Tier == 0 {
						plan.Tier = defense.TierReversible
					}
					breakerKey = "agent:" + alt
					c.EvTrace(ctx, taskID, "agent_session_affinity", map[string]any{
						"agent": alt, "adapter": ag.Adapter, "was": prev,
					})
					c.audit(ctx, taskID, "agent:session_affinity", alt, "promoted",
						"checkpoint resume prefers minting adapter "+t.AgentSessionAdapter)
					break
				}
			}
		}
	}
	perm := defense.NormalizeMode(spec.PermissionMode)
	tightenForPermission(&plan, perm)
	if perm == defense.ModePlan && plan.Kind == "agent" {
		was := plan.Agent
		p, perr := planModeAgent(router, plan)
		if perr != nil {
			return commander.Plan{}, nil, "", perr
		}
		// The breaker records failures by agent name; a swap that left the key
		// pointing at the agent this task will never run would blame the wrong
		// CLI for the outcome.
		if p.Agent != was {
			breakerKey = "agent:" + p.Agent
		}
		plan = p
	}
	return plan, router, breakerKey, nil
}

// claimForRun moves the task row into running and returns whatever the
// caller must hold while it runs: the task snapshot and the actuator unlock
// (a no-op for non-hardware plans). On error the lock is already released —
// the success path hands ownership to the caller's defer.
func (c *Core) claimForRun(ctx context.Context, taskID, intent string, plan *commander.Plan, releaseSlot func()) (Task, func(), error) {
	unlock := func() {}
	task, err := c.store.Get(ctx, taskID)
	if err != nil {
		unlock()
		return Task{}, nil, fmt.Errorf("load task: %w", err)
	}
	// A row that is queued but owned by this node is a claim whose runner is
	// still alive: task recovery's dispatched → queued branch moved it back
	// underneath us (Recover runs at startup, and the scheduler's first pass
	// can claim a row before recovery has finished). The claim is still ours
	// — the scheduler's resource-key registry holds the task's keys for the
	// run's duration, so no other runner can take it — so restore the state
	// the run expects rather than refusing the row ("cannot run task in state
	// queued") and losing the task.
	if task.State == StateQueued {
		if rerr := c.store.ReclaimClaimed(ctx, taskID, c.nodeID); rerr == nil {
			if fresh, gerr := c.store.Get(ctx, taskID); gerr == nil {
				task = fresh
			}
		} else {
			c.logger.Warn("queue: reclaim claimed task", "task", taskID, "err", rerr)
		}
	}
	// §7.2: an actuator plan's argv is a template — fill {intent}/{action}/
	// {param:<name>} from the task's action_spec before anything runs. A
	// substitution failure means the spec and the card disagree, which no
	// retry fixes: fail at the gate, not inside the driver.
	//
	// The mirror check comes first: a spec that names a target but reached a
	// NON-actuator plan (its id never made requires, or requires matched a
	// plain ability first) would otherwise be dropped silently while the raw
	// intent runs as a shell command — the task asked for hardware and got a
	// prompt-shaped command. Fail the dispatch instead.
	spec, serr := commander.ParseActionSpec(task.SpecJSON)
	if serr != nil {
		// Malformed spec_json only hard-fails an actuator plan — the field is
		// what that plan substitutes from. A non-actuator plan never reads it,
		// so an old or foreign row carrying odd spec text keeps the
		// pre-gate behavior rather than inheriting a new fatal parse.
		if plan.ActuatorID != "" {
			unlock()
			return Task{}, nil, fmt.Errorf("route actuator: %w", serr)
		}
		spec = nil
	}
	if spec != nil && spec.TargetActuator != "" && plan.ActuatorID == "" {
		unlock()
		return Task{}, nil, fmt.Errorf("route actuator: action_spec targets %q but the resolved plan (%s %q) is not an actuator — requires %v matched no hardware:* capability",
			spec.TargetActuator, plan.Kind, plan.Ability, task.Requires)
	}
	// Cross-process execution lease (execlock.go): the keys this run occupies
	// in the shared exec_locks table, taken before the row transitions, so a
	// same-project (or same-actuator) task arriving by ANY path or core
	// process waits in dispatched behind this run instead of writing the same
	// worktree or driving the same device beside it. dance is the worktree
	// subset the delegate wait releases and re-takes, mirroring the queue
	// registry's dance exactly; the actuator row stays held through a fan-out.
	// The wait holds the delegated capacity reservation (releaseSlot defers
	// in run): a blocked sibling counts against MaxConcurrent for the whole
	// wait, the same accounting the context-fetch wait already used — excess
	// delegations decline and route elsewhere rather than queuing here.
	keys := execLockKeys(task, plan.ActuatorID)
	dance := queue.DefaultKeys(task.ResourceKeys, task.Project, task.WorkDir, task.TaskID)
	// The run nonce stamped on this acquisition's exec_locks rows — releases
	// predicate on it so a late unlock can only drop this run's own rows,
	// never the fresh rows of a re-execution (see execlock.go).
	runID, err := newUUID()
	if err != nil {
		unlock()
		return Task{}, nil, fmt.Errorf("exec lock run id: %w", err)
	}
	if err := c.execLockAcquire(ctx, taskID, runID, keys, dance, true); err != nil {
		unlock()
		return Task{}, nil, err
	}
	unlock = func() { c.execLockUnlock(taskID) }
	if plan.ActuatorID != "" {
		if err := commander.SubstituteActionSpec(plan, spec, intent); err != nil {
			unlock()
			return Task{}, nil, fmt.Errorf("route actuator: %w", err)
		}
		// Screen sensing resolved: stamp the row before anything runs. The
		// executor's card is the only authority for operator-defined actuator
		// ids, so the mark must originate here — upstream hops learn it from
		// the result's screen_output flag and stamp their own rows.
		if c.actuatorIsScreen(plan.ActuatorID) {
			if err := c.store.SetScreenOutput(ctx, taskID); err != nil {
				c.logger.Warn("mark screen output", "task", taskID, "err", err)
			}
		}
		// The resolved actuator is a physical device: serialize its driver
		// across every execution path this run() serves — inline submits and
		// delegated accepts never pass through the queue's resource-key
		// registry, and a queue task's keys only decide when it starts, not
		// what its driver may overlap with. Blocking here, before Accept,
		// keeps the row in dispatched while a sibling finishes on the same
		// device rather than running two drivers on one pin concurrently.
		// The in-process mutex is the fast path; the actuator id also rides
		// the exec lease above, which extends the same exclusion to other
		// core processes on this database.
		unlockActuator := c.lockActuator(plan.ActuatorID)
		unlockExec := unlock
		unlock = func() { unlockActuator(); unlockExec() }
	}
	switch task.State {
	case StateDispatched:
		if err := c.store.Accept(ctx, taskID, c.nodeID); err != nil {
			// The accept can lose the race with a concurrent transition landing
			// between the pre-check above and the guarded write. Reporting a
			// plain error here would turn into a task_decline, bouncing a task
			// that is already owned back into re-routing; re-read and
			// distinguish: running means another runner claimed it first (yield
			// the task to that owner), terminal means the task was closed under
			// us (cancelled/expired — execution acknowledges, no result is
			// reported).
			if errors.Is(err, ErrConflict) {
				fresh, gerr := c.store.Get(ctx, taskID)
				if gerr == nil && fresh.State == StateRunning {
					// The row is running because another runner claimed it
					// first (a duplicated resume/context_ack raced ahead of
					// this invocation). Falling through would spawn a second
					// executor on the same task: bail out quietly and let the
					// owner report the outcome.
					unlock()
					return Task{}, nil, ErrAlreadyRunning
				}
				if gerr == nil && Terminal(fresh.State) {
					unlock()
					return Task{}, nil, ErrCancelled
				}
			}
			unlock()
			return Task{}, nil, fmt.Errorf("accept: %w", err)
		}
		// Accept succeeded: the row is running and countable, so the admission
		// reservation is freed now — holding it through execution would
		// double-count the slot, the same judgement the waiting_context resume
		// below makes. The deferred releaseSlot() above still fires at return;
		// reserveCapacity's once makes that second call a no-op.
		releaseSlot()
	case StateWaitingCtx:
		if err := c.store.Resume(ctx, taskID, c.nodeID); err != nil {
			unlock()
			return Task{}, nil, fmt.Errorf("resume: %w", err)
		}
		// Resume succeeded: the row is running and countable, so the
		// reservation is freed now — holding it through execution would
		// double-count the slot. The deferred releaseSlot() above still fires
		// at return; reserveCapacity's once makes that second call a no-op.
		releaseSlot()
	case StateRunning:
		// Already running: another run() invocation owns this task. Executing
		// here would spawn a duplicate executor racing the first one's
		// subprocess on the same work dir.
		unlock()
		return Task{}, nil, ErrAlreadyRunning
	default:
		unlock()
		return Task{}, nil, fmt.Errorf("cannot run task in state %s", task.State)
	}
	return task, unlock, nil
}

// runPrep is what prepareRunDir leaves behind: the working directory the
// task executes in, the scope/oscillation bookkeeping the supervision loop
// consults, the pre-run snapshot files_changed diffs against, and the
// negotiation release the caller owns until the run ends.
type runPrep struct {
	workDir         string
	scope           *defense.Scope
	oscKey          string
	before          defense.Snapshot
	beforeOK        bool
	shadowConflicts []string
	negoRelease     func()
}

// prepareRunDir builds the task's workspace: the workdir resolution rules
// (pinned, stage, project, attached), the artifact inputs it unpacks, the
// shadow merges a preempted task resumes through, scope negotiation, and the
// before-snapshot. Nothing here executes the plan — it only makes executing
// honest.
func (c *Core) prepareRunDir(execCtx, ctx context.Context, task Task, taskID string, plan commander.Plan) (runPrep, error) {
	var negoRelease func()
	// workDir is normally the node-wide execution directory; a queued task may
	// pin its own (a panel session's worktree) so concurrent tasks never share
	// a working directory (queue redesign — SetWorkDir is process-global and
	// only the synchronous paths may swap it).
	workDir := c.workDir
	if task.WorkDir != "" {
		workDir = task.WorkDir
		// A pinned directory is created when missing. Both `--workdir` and a
		// delegate child's workdir mean "run in THIS tree", and every other
		// branch below mkdirs its own derived dir — leaving the pinned case to
		// fail on an absent directory would make the one mode that names a
		// destination by hand the only one that cannot set it up.
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			return runPrep{}, fmt.Errorf("create pinned work dir: %w", err)
		}
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
			return runPrep{}, err
		}
		workDir = wd
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			return runPrep{}, fmt.Errorf("create stage work dir: %w", err)
		}
		if err := c.fetchStageInputs(execCtx, task, workDir); err != nil {
			return runPrep{}, fmt.Errorf("stage inputs: %w", err)
		}
	} else if projectInputs(task) {
		// A delegated project task: the tree it must start from arrived as an
		// artifact reference, and it unpacks into this node's own directory for the
		// project. Failing rather than running on an absent tree is the same
		// judgement the stage path makes — an agent let loose in an empty directory
		// reports success over work it never had the inputs to do.
		wd, err := c.projectWorkDir(task.Project)
		if err != nil {
			return runPrep{}, err
		}
		workDir = wd
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			return runPrep{}, fmt.Errorf("create project work dir: %w", err)
		}
		if err := c.fetchStageInputs(execCtx, task, workDir); err != nil {
			return runPrep{}, fmt.Errorf("project inputs: %w", err)
		}
	} else if attachedInputs(task) {
		// A file task whose work tree shipped as an artifact input: it unpacks
		// into a private per-task dir — the shared node work dir would race
		// with every other anonymous task — and runs there with the real code.
		wd, err := c.attachedWorkDir(task.TaskID)
		if err != nil {
			return runPrep{}, err
		}
		workDir = wd
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			return runPrep{}, fmt.Errorf("create task work dir: %w", err)
		}
		if err := c.fetchStageInputs(execCtx, task, workDir); err != nil {
			return runPrep{}, fmt.Errorf("worktree inputs: %w", err)
		}
	} else if task.Project != "" && task.WorkDir == "" {
		// A project task with nothing to pull still belongs to its project:
		// run it in the project's own work dir so project-anchored scopes and
		// the worktree that --project names mean what the user meant. A
		// submitter-pinned worktree still wins — the pin is explicit.
		wd, err := c.projectWorkDir(task.Project)
		if err != nil {
			return runPrep{}, err
		}
		workDir = wd
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			return runPrep{}, fmt.Errorf("create project work dir: %w", err)
		}
	} else if task.WorkDir == "" {
		// A plain anonymous task: no stage, no project, no attached inputs,
		// no submitter pin. Running it in the node work dir mixes task
		// output with the node's own live state — on the default layout
		// that dir is the data dir, so agent files landed next to the open
		// database — and makes every drift snapshot walk the node's whole
		// state tree. Give it the same private per-task dir an attached
		// worktree gets: concurrent anonymous tasks never share files, and
		// files_changed diffs only the task's own footprint.
		wd, err := c.attachedWorkDir(task.TaskID)
		if err != nil {
			return runPrep{}, err
		}
		workDir = wd
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			return runPrep{}, fmt.Errorf("create task work dir: %w", err)
		}
	}

	// Isolated delegation (delegateRequest.Isolate): the child declared it
	// wants a private copy of the project tree instead of the shared one —
	// a git worktree when the project is a repository, a copy otherwise.
	// Populated from the tree as it is right now (HEAD plus the working
	// tree's uncommitted delta); the run below executes entirely inside it,
	// and run's terminal path merges the changes back. A setup failure is
	// fatal rather than a silent fallback: the task's lock set is already
	// narrowed to itself, and dropping it onto the shared tree unlocked
	// would let siblings stomp each other.
	if task.Isolate && task.Project != "" && task.PlanID == "" {
		iso, ierr := c.setupIsolatedWorkDir(execCtx, task, workDir)
		if ierr != nil {
			return runPrep{}, fmt.Errorf("isolated workspace: %w", ierr)
		}
		c.EvTrace(execCtx, taskID, "isolated_workspace", map[string]any{
			"workspace": iso, "source": workDir,
		})
		workDir = iso
	}

	// §5.2 shadow resume: a task preempted mid-edit parked its scoped files
	// before the cancel landed (negoInterruptLocal). Merging happens before
	// the drift snapshot so restored paths count as pre-existing state, not
	// agent writes — and before anything runs, so the agent resumes on top
	// of its interrupted work instead of an empty tree. Contested paths stay
	// with the winner's bytes and are named for the agent below.
	var shadowConflicts []string
	if restored, conflicts, serr := defense.MergeShadow(workDir, taskID); serr != nil {
		c.logger.Warn("shadow merge", "task", taskID, "err", serr)
	} else if len(restored)+len(conflicts) > 0 {
		shadowConflicts = append(shadowConflicts, conflicts...)
		c.EvTrace(execCtx, taskID, "shadow_merge", map[string]any{
			"restored":  len(restored),
			"conflicts": conflicts,
		})
		c.audit(ctx, taskID, "shadow:merge", "", "merged",
			fmt.Sprintf("restored %d preempted files; %d contested", len(restored), len(conflicts)))
	}

	if gitRestored, gitConflicts, gerr := defense.MergeGitShadow(workDir, taskID); gerr != nil {
		c.logger.Warn("git shadow merge", "task", taskID, "err", gerr)
	} else if len(gitRestored)+len(gitConflicts) > 0 {
		shadowConflicts = append(shadowConflicts, gitConflicts...)
		c.EvTrace(execCtx, taskID, "git_shadow_merge", map[string]any{
			"restored":  len(gitRestored),
			"conflicts": gitConflicts,
		})
		c.audit(ctx, taskID, "git_shadow:merge", "", "merged",
			fmt.Sprintf("restored %d preempted git files; %d contested", len(gitRestored), len(gitConflicts)))
	}

	// §6.2 monotonic-progress bookkeeping: the oscillation window is keyed by
	// the project or plan whose tree this task works on — the unit a mesh of
	// agents "revisits". Anonymous tasks get a private per-task dir, so no
	// shared tree exists to track oscillation across; they are exempt.
	oscKey := task.Project
	if oscKey == "" {
		oscKey = task.PlanID
	}

	// Scope drift (design §14.2 signal A): for an agent task that declares a
	// scope, snapshot the working directory before execution so changes outside
	// the scope can be intercepted rather than silently committed. The snapshot
	// is taken once and reused across supervision rounds.
	scope := defense.NewScope(taskScope(task.SpecJSON))
	if plan.Kind == "agent" && !scope.Empty() {
		// §5.1 conflict negotiation: before the agent touches the declared
		// scope, arbitrate it against every peer's lock table. A denial fails
		// the run and the retry loop re-negotiates later — the mesh's "wait".
		if err := c.negotiateTaskScope(execCtx, task, scope, plan.Agent); err != nil {
			return runPrep{}, fmt.Errorf("scope negotiation: %w", err)
		}
		negoRelease = func() { c.negoReleaseTask(task.TaskID) }
	}
	// The before-snapshot rides every agent plan, not only scoped ones: the
	// scope check consumes it for drift detection, and the result contract
	// (files_changed) diffs it against the after snapshot — a delegator that
	// never declared a scope still wants to see what the task touched.
	var before defense.Snapshot
	beforeOK := false
	if plan.Kind == "agent" {
		var err error
		if before, err = defense.SnapshotDir(workDir); err != nil {
			c.logger.Warn("snapshot workdir before agent", "task", taskID, "err", err)
		} else {
			beforeOK = true
			if before.Truncated() || before.Skipped() > 0 {
				c.logger.Info("workdir snapshot partial", "task", taskID,
					"truncated", before.Truncated(), "skipped", before.Skipped())
			}
		}
		// Git baseline: on a real checkout (a project workspace, a submitter
		// pin) the run gets an undo point — refs/heads/panda-base-<id> —
		// before the agent touches anything, so `panda task revert` can put
		// the tree back wholesale. It runs after the shadow merges above, so
		// a resumed task's baseline contains its restored mid-flight work.
		// Cost is one scratch-index write-tree — cheap next to the run, and
		// skipped entirely when workDir is not a repository.
		if defense.IsGitRepo(workDir) {
			if _, berr := defense.SaveGitBaseline(workDir, taskID); berr != nil {
				c.logger.Warn("git baseline", "task", taskID, "err", berr)
			}
		}
	}
	return runPrep{
		workDir: workDir, scope: scope, oscKey: oscKey,
		before: before, beforeOK: beforeOK,
		shadowConflicts: shadowConflicts, negoRelease: negoRelease,
	}, nil
}

// decorateExecCtx layers the agent-only context machinery onto execCtx:
// the throttled progress sink, the bounded transcript-event tap, the session
// pool, and the per-task timeout/tools/turn overrides. It returns the
// decorated context and the pool's cleanup (a no-op when no pool was made)
// so the caller can hold them for the run's lifetime.
func (c *Core) decorateExecCtx(execCtx, ctx context.Context, taskID string, task Task, plan commander.Plan) (context.Context, func()) {
	cleanup := func() {}
	// Live progress (A5) is per-task, not per-round: the throttled progress
	// sink is built once and reused across supervision rounds. It decorates
	// execCtx, which already carries the cancellation the abort registry holds.
	if plan.Kind == "agent" {
		var lastNote atomic.Value // string
		lastNote.Store("")
		execCtx = commander.WithProgress(execCtx, func(note, kind string) {
			// Sub-agent events are always recorded (never throttled):
			// the delegation chain is too important to rate-limit.
			if kind == "subagent" {
				if err := c.store.RecordEvent(context.WithoutCancel(ctx), taskID,
					EvSubagentEvent, map[string]any{"note": note}); err != nil {
					c.logger.Warn("record sub-agent event", "task", taskID, "err", err)
				}
				return
			}
			// Agent-switch notes are lifecycle truth, not progress chatter:
			// the card started the round under the plan's primary name and
			// must not keep wearing it while a fallback candidate actually
			// runs. Recorded unthrottled and relayed like a progress note —
			// the origin's copy of a delegated task deserves the same
			// correction.
			if kind == "agent_switch" {
				if err := c.store.RecordEvent(context.WithoutCancel(ctx), taskID,
					EvAgentSwitch, map[string]any{"note": note}); err != nil {
					c.logger.Warn("record agent switch", "task", taskID, "err", err)
				}
				if len(task.Chain) > 0 {
					c.relayToParent(context.WithoutCancel(ctx), bus.MsgTaskProgress, task.Chain,
						bus.TaskProgressPayload{TaskID: taskID, AttemptID: task.AttemptID, Note: note})
				}
				return
			}
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
			// Delegated task: the origin's own copy of this row shows only
			// accept→result without the note, so `panda task <id>`/logs there
			// look frozen for the whole agent run. Ride the same text on the
			// existing task_progress beat — every hop relays it upward, and a
			// missing peer just drops the note (the lease beat still fires on
			// its own tick).
			if len(task.Chain) > 0 {
				c.relayToParent(context.WithoutCancel(ctx), bus.MsgTaskProgress, task.Chain,
					bus.TaskProgressPayload{TaskID: taskID, AttemptID: task.AttemptID, Note: note})
			}
		})

		// Structured transcript (event protocol v2): every typed block the
		// adapter streams — assistant text, thinking, tool_use/tool_result,
		// sub-agent blocks keyed by the parent tool_use id — lands as an
		// agent_event row so the display layer renders the delegation tree,
		// not just a progress line. The count is bounded: a pathological
		// adapter cannot grow the task's event table without limit, and past
		// the cap one truncation marker records that the stream was cut.
		var evCount atomic.Int64
		execCtx = commander.WithAgentEvents(execCtx, func(ev commander.AgentEvent) {
			if ev.Ev == "session" && ev.ID != "" {
				// The adapter minted (or resumed under) a conversation id:
				// persist it NOW, not at round end — a kill mid-round still
				// leaves a consumable checkpoint, which is what lets the
				// startup Recover requeue the run instead of failing a
				// 25-minute exploration that had a live session all along.
				if err := c.store.SetAgentSession(context.WithoutCancel(ctx),
					taskID, ev.ID, c.nodeID, ev.Adapter); err != nil {
					c.logger.Warn("persist mid-run agent session", "task", taskID, "err", err)
				}
				c.EvTrace(context.WithoutCancel(ctx), taskID, "adapter_session_mint", map[string]any{
					"session": ev.ID, "adapter": ev.Adapter,
				})
				return
			}
			if n := evCount.Add(1); n > maxAgentTranscriptEvents {
				if n == maxAgentTranscriptEvents+1 {
					c.EvTrace(context.WithoutCancel(ctx), taskID, EvAgentEvent, map[string]any{
						"ev": "transcript_truncated", "count": n - 1,
					})
				}
				return
			}
			data := map[string]any{"ev": ev.Ev}
			if ev.ID != "" {
				data["id"] = ev.ID
			}
			if ev.Parent != "" {
				data["parent"] = ev.Parent
			}
			if ev.Name != "" {
				data["name"] = ev.Name
			}
			if ev.ToolUseID != "" {
				data["tool_use_id"] = ev.ToolUseID
			}
			if ev.Text != "" {
				data["text"] = ev.Text
			}
			if ev.Thinking != "" {
				data["thinking"] = ev.Thinking
			}
			if ev.Content != "" {
				data["content"] = ev.Content
			}
			if len(ev.Input) > 0 {
				data["input"] = ev.Input // RawMessage embeds as JSON, not a string
			}
			if ev.IsError {
				data["is_error"] = true
			}
			if err := c.store.RecordEvent(context.WithoutCancel(ctx), taskID,
				EvAgentEvent, data); err != nil {
				c.logger.Warn("record agent event", "task", taskID, "err", err)
			}
		})

		// Session pool (Phase 6): under a supervisor, session-capable
		// adapters keep their CLI alive across rounds — a continue verdict
		// costs one stdin line instead of a spawn + transcript reload. The
		// pool dies with the task; adapters that never take a session simply
		// never populate it.
		if c.supervisor != nil {
			var pool *commander.AgentSessions
			execCtx, pool = commander.WithSessionPool(execCtx)
			cleanup = pool.CloseAll
			execCtx = commander.WithSessionMode(execCtx)
		}
	}

	// Per-task agent timeout by plan kind: a training stage gets a larger
	// budget than a code edit without the operator having to retune the
	// global default. The override rides the context so runAdapterProcess
	// picks it up without the execution-path signatures changing.
	if plan.Kind == "agent" {
		// A conversation in plan mode hands the agent the read-only tool
		// face: it can look, reason and write a plan, but "produce a plan"
		// must not quietly become "do it". The tier tightening in
		// resolveRunPlan holds native commands at the same time.
		if taskPermissionMode(task.SpecJSON) == defense.ModePlan {
			execCtx = commander.WithRestricted(execCtx)
		}
		if d := c.agentTimeoutForKind(plan.Kind); d > 0 {
			execCtx = commander.WithAgentTimeout(execCtx, d)
		}
		// Per-task tools policy: the entry model can request "extended"
		// for high-complexity tasks that need the agent's full capability
		// set (native skills, MCP, sub-agents). A task-level override wins
		// over the router's global policy: the Router applies its global
		// policy only when the context carries no task-level policy yet.
		if tp := taskToolsPolicy(task.SpecJSON); tp != "" {
			execCtx = commander.WithToolsPolicy(execCtx, tp)
		}
		// Per-task turn cap: a task that needs a long build-test loop declares
		// it in spec.max_turns rather than inheriting the adapter default.
		if mt := taskMaxTurns(task.SpecJSON); mt > 0 {
			execCtx = commander.WithMaxTurns(execCtx, mt)
		}
		// The entry classifier's 0–1 score rides to model-selecting adapters
		// (opencode): a simple task prefers the provider's free tier instead
		// of the capability-first pick.
		execCtx = commander.WithComplexity(execCtx, task.Complexity)
	}
	return execCtx, cleanup
}

// roundOutcome is superviseRounds' answer: either a terminal payload a
// mid-loop branch already decided (done), or the post-loop state finalizeRun
// turns into one. The loop's review parks and refusals already build the
// whole result envelope, so wrapping — rather than re-deriving — keeps them
// byte-identical.
type roundOutcome struct {
	payload        bus.TaskResultPayload
	done           bool
	res            commander.Result
	lastChanged    []string
	filesTruncated bool
	verdict        entry.SuperviseVerdict
	sessionID      string
	sessionAdapter string
}

// runWork bundles the mutable state run()'s phases share, so each phase stays
// a method with a short signature instead of a dozen threaded parameters.
type runWork struct {
	c               *Core
	taskID          string
	intent          string
	required        []string
	task            Task
	plan            commander.Plan
	router          *commander.Router
	breakerKey      string
	injection       commander.InjectionDecision
	execCtx         context.Context
	attemptID       string
	workDir         string
	oscKey          string
	scope           *defense.Scope
	before          defense.Snapshot
	beforeOK        bool
	shadowConflicts []string
	// checks is the task's delivery contract and baseline its pre-run
	// evidence. Empty checks mean the task has no contract, so it can be done
	// but never "verified".
	checks   []contract.Check
	baseline []contract.Result
}

// executeRounds is the supervision loop: an agent task under a supervisor
// executes once and is then judged; a "continue" verdict re-delegates the
// follow-up to the same plan (whose fallback chain may pick a different
// agent) until the judge accepts or the round budget runs out. Every
// non-agent plan — and every agent plan without a supervisor — converges in
// one round.
func (w *runWork) executeRounds(ctx context.Context) (roundOutcome, error) {
	c := w.c
	task := w.task
	taskID := w.taskID
	intent := w.intent
	required := w.required
	plan := &w.plan
	router := w.router
	breakerKey := w.breakerKey
	injection := w.injection
	execCtx := w.execCtx
	workDir := w.workDir
	oscKey := w.oscKey
	scope := w.scope
	before := w.before
	beforeOK := w.beforeOK
	attemptID := w.attemptID
	shadowConflicts := w.shadowConflicts
	// The injection decision is per task, not per round: a supervised task
	// re-runs the same adapter with the same env, so recording the identical
	// model_injection event once keeps the detail view from showing the same
	// footnote on every round. A round that switches (policy injection, then a
	// rescue) differs in the key and is recorded.
	lastInjectionKey := ""
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

	baseIntent := intent + taskSpecEnvelope(task.SpecJSON)
	if shadowConflicts != nil {
		// The winner's bytes won on these paths: tell the resumed agent
		// exactly which of its preempted edits died so it re-applies them
		// deliberately instead of assuming the shadow restored everything.
		baseIntent += "\n\n[system] this task was preempted and resumed from a shadow work copy; " +
			"the files it edited that the preempting task also changed kept the OTHER version — " +
			"re-apply your intended edits to: " + strings.Join(shadowConflicts, ", ")
	}
	// intentSteers/intentNotes accumulate what the rounds fold into the
	// prompt on top of the task definition — a user directive leads (ahead
	// of the spec), runtime notes trail in arrival order. They stay
	// separate from baseIntent so a context-overflow retry can compress
	// the accretion (compactRunNotes) without touching the spec itself.
	var intentSteers, intentNotes []string
	fullIntent := func() string {
		var b strings.Builder
		for _, s := range intentSteers {
			b.WriteString(s)
			b.WriteString("\n\n")
		}
		b.WriteString(baseIntent)
		for _, n := range intentNotes {
			b.WriteString("\n\n")
			b.WriteString(n)
		}
		return b.String()
	}
	var res commander.Result
	var usedSkills []*skills.Skill
	// sessionID threads the agent's own conversation across supervision
	// rounds: a "continue" verdict resumes the session that produced the
	// work instead of cold-starting on the bare follow-up text (the agent
	// keeps its reasoning trail; multi-round supervision stops paying the
	// full re-orientation cost every round). Adapters without session
	// support leave it empty and every round starts fresh, as before.
	// The session id is also the task's resume checkpoint (§5.2): the row
	// carries the last round's handle, so a run that comes back after a
	// yield/restart/redelegation continues the same agent conversation rather
	// than cold-starting on the shadow copy. The shadow path still runs —
	// adapters without session support leave the field empty and behave
	// exactly as before.
	sessionID := task.AgentSessionID
	// sessionAdapter records which adapter file minted the handle. The resume
	// offer is adapter-affine: a round whose plan (or in-flight fallback)
	// lands on a different adapter must not see the foreign id — opencode
	// rejects a claude-shaped UUID with "Expected a string starting with
	// ses", and a bounded retry then re-burns a whole cold-start run.
	sessionAdapter := task.AgentSessionAdapter
	if sessionID != "" {
		c.EvTrace(execCtx, taskID, "adapter_session_resume", map[string]any{
			"session": sessionID, "agent": plan.Agent, "adapter": sessionAdapter,
		})
	}
	var lastAgent, lastOutput, lastStderr string
	// lastChanged carries the last completed round's workdir footprint out of
	// the loop: the judge sees it as evidence (an agent claiming done while
	// touching nothing is the classic failure), and the result contract below
	// reports it as files_changed. filesTruncated marks a workdir too large
	// to snapshot fully: the diff covers only the lexical prefix, which the
	// result flags so consumers don't read partial evidence as "no work".
	var lastChanged []string
	var filesTruncated bool
	verdict := entry.SuperviseVerdict{Status: entry.VerdictDone}
	// delegations bounds how many PANDA_DELEGATE promotions one task may make
	// (§4.2): the configured delegation.max_children, with the schema's
	// maxItems as the protocol ceiling. Each promotion re-runs the round
	// with the child's result folded into the intent, so the counter — not
	// the judge budget — is the guard against a marker-happy agent; the
	// mesh budget bounds the spawned tree itself.
	delegLimits := c.delegateLimits()
	maxDelegations := delegLimits.MaxChildren
	if maxDelegations < 1 {
		maxDelegations = 1
	}
	// canSpawn gates the whole sub-agent surface for this task: a task at
	// the configured depth limit (or on a node whose operator disabled
	// delegation) gets no protocol hint, and any delegate marker it still
	// emits is refused rather than spawned.
	canSpawn := c.delegationEnabled() && task.SpawnDepth < c.effectiveSpawnCap(task)
	// Seed the spend counter from the rows, not from this episode: a re-driven
	// or resumed run that started at zero would get to mint a whole second
	// budget on top of the children it already spawned, laundering the cap
	// once per restart. Counting every child row (terminal included) matches
	// the config's "across all its rounds" meaning and also covers children
	// the agent minted itself via `task add --parent-id`.
	delegations := 0
	if kids, kerr := c.store.Children(execCtx, taskID); kerr == nil {
		delegations = len(kids)
	}
	// steerRounds bounds how many extra rounds user injects (steer.go) may
	// grant: the steer's round is on the house (a single-round task would
	// otherwise end empty when its only round is aborted), but a spamming
	// sender must not extend the loop without bound.
	const maxSteerRounds = 4
	steerRounds := 0
	// pendingChildren are delegate children still in flight — a request whose
	// result had not landed when its wait window closed. Rebuilding the set
	// from the rows (not just this episode's memory) means a resumed run also
	// folds children spawned before the interruption.
	pendingChildren := c.liveChildren(execCtx, taskID)
	defer func() {
		// Every exit path drops the waiters of still-pending children: the
		// row, not the channel, is what a late result needs to land on.
		for _, pc := range pendingChildren {
			c.waiters.Delete(pc.id)
		}
	}()
	// boardSeen tracks which board slots have already been folded into the
	// intent, so each round injects only what changed; msgsSent meters this
	// episode's posts against the per-task budget.
	boardSeen := msgSeen{}
	msgsSent := 0
	for round := 0; round < maxRounds; round++ {
		// Fold delegate children that resolved since the last round before
		// the agent runs again: a result that arrived after its wait window
		// still reaches the prompt, so "the child answered too late" stops
		// meaning "the answer was lost".
		if len(pendingChildren) > 0 {
			for _, note := range c.foldLateChildren(execCtx, &pendingChildren) {
				intentNotes = append(intentNotes, "[delegated child result arrived]\n"+note)
			}
		}
		// Sibling board: posts from same-scope tasks (plan stages, a parent's
		// children) land between rounds the same way late child results do —
		// the agent reads them next round rather than blocking on them.
		if lines := c.drainTaskMessages(execCtx, task, boardSeen); len(lines) > 0 {
			intentNotes = append(intentNotes, "[sibling board]\n"+strings.Join(lines, "\n"))
		}
		// User inject (steer.go): a message sent in "inject" mode leads this
		// round's intent — ahead of the task spec and any folded notes — so
		// the user's directive sets the course instead of reading as context.
		if steer := c.takeSteer(execCtx, taskID, boardSeen); steer != "" {
			intentSteers = append([]string{"[user directive]\n" + steer}, intentSteers...)
		}
		// The agent can spawn children itself (a CLI `task add
		// --parent-id $PANDA_TASK_ID`), which the episode's opening snapshot
		// never saw: re-read the rows so a mid-run child enters the pending
		// set and later rounds fold its result like any other delegate's.
		c.mergeLiveChildren(execCtx, taskID, &pendingChildren)
		// §6.1 token budget: a task whose quota is already spent — by an
		// earlier round's judge charge, or before it ever reached this
		// executor — must not run another metered round. The check reads the
		// persisted row because the task snapshot predates any spend, and it
		// parks for every plan kind: a human tops up the quota or closes the
		// task out, the way a scope-drift pause already works.
		if left, serr := c.store.SpendTokens(ctx, taskID, 0); serr != nil {
			// Fail open, but not silently: a broken read must not look like a
			// healthy budget. The round proceeds — refusing work on a transient
			// database error would strand tasks that a retry drains normally —
			// and the log names the reason the cap was not enforced.
			c.logger.Warn("token budget check failed, allowing round", "task", taskID, "err", serr)
		} else if left < 0 {
			msg := "token budget exhausted"
			if err := c.store.PauseAttempt(ctx, taskID, c.nodeID, attemptID, msg); err != nil {
				if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
					return roundOutcome{}, ErrCancelled
				}
				return roundOutcome{}, fmt.Errorf("pause on token exhaustion: %w", err)
			}
			c.logTask(task.Title, false)
			trackTask(c, task.Project, required, task.Title, false)
			return roundOutcome{done: true, payload: bus.TaskResultPayload{
				TaskID: taskID, AttemptID: attemptID, State: StateReview,
				ApprovalDisposition: string(ApprovalNeedsChangedInput),
				OK:                  false, ExitCode: 1, Stderr: msg,
				Tokens: res.Tokens, Cost: res.Cost, Agent: res.Agent, Model: res.Model, Injected: res.Injected,
			}}, nil
		}
		// §6.2 monotonic progress: hashing the work tree at each round's head
		// means a later round that regresses to a state an earlier round (or
		// an earlier task on this project) already produced is caught before
		// more tokens are spent — agent B silently undoing agent A's fix can
		// never converge, so it fails fast with a trace event instead.
		if oscKey != "" {
			h, n, herr := defense.HashDir(workDir, stateHashMaxFiles)
			if herr == nil && n > stateHashMaxFiles {
				// Over the walk cap: a giant work tree still gets the check
				// via the repo fingerprint — git reads its index, not every
				// file. A non-repo tree keeps the old behaviour (skip).
				if gh, gerr := defense.GitFingerprint(execCtx, workDir); gerr == nil {
					h, n = gh, 1
				}
			}
			if herr == nil && n <= stateHashMaxFiles && n > 0 {
				if c.stateOscillates(oscKey, h) {
					c.EvTrace(execCtx, taskID, "state_oscillation", map[string]any{
						"key":   oscKey,
						"hash":  h,
						"round": round + 1,
					})
					return roundOutcome{}, fmt.Errorf(
						"state oscillation on %s: work tree regressed to a previously seen state (round %d)",
						oscKey, round+1)
				}
			}
		}
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
		activeModel := ""
		if injection.Inject {
			activeModel = injection.Model
		}
		if activeModel == "" {
			activeModel = plan.Agent
		}
		if activeModel == "" {
			activeModel = c.model.Model
		}
		if activeModel == "" {
			activeModel = c.model.Provider
		}

		geo := providers.DetectRegion(activeModel)
		policy := PromptLanguagePolicy{
			UserLocale:     task.GetUserLocale(),
			ModelGeoRegion: geo,
		}
		promptLang := policy.RecommendedPromptLang()
		outputLang := policy.RecommendedOutputLang()

		// assemblePrompt builds this round's prompt; a context-overflow retry
		// rebuilds it under the shrunken overflowPromptBudget so every matched
		// skill degrades to its index line.
		assemblePrompt := func(promptBudget int) string {
			p, used := buildAgentPrompt(c, fullIntent(), task.Project, task.Title, workDir, promptBudget, promptLang, outputLang)
			usedSkills = used
			// §4.2 Sub-MainAgent protocol hint: only a task with depth budget
			// left learns the marker format — a task at delegation.max_depth
			// gets no rope it cannot use.
			if canSpawn {
				p += i18n.T(promptLang, "prompt.delegate.hint")
			}
			// The sibling-board protocol hint rides only when the task actually
			// shares a scope — a standalone task has nobody to post to, so the
			// hint would be prompt noise. A parent with live children is the
			// exception: it hosts their task:<self> board and can address
			// "children", so orchestrating parents learn the protocol too.
			if messageScope(task) != "" || len(pendingChildren) > 0 {
				p += i18n.T(promptLang, "prompt.msgboard.hint")
			}
			// A scoped task must hear the lane it claims at the prompt level —
			// the lock table only keeps OTHER tasks out; nothing inside the run
			// tells the agent which subtree is its own. Delegate children carry
			// this in their intent already (skip them); a manually queued
			// `task add --paths` row does not, and neither do reruns whose intent
			// was rewritten.
			if !task.Isolate && len(task.ScopePaths) > 0 && !strings.Contains(task.Intent, "[scope]") {
				p += "\n\n[scope] you own paths: " + strings.Join(task.ScopePaths, ", ") +
					" — write only inside your own scope; sibling tasks may own other paths in this tree."
			}
			if task.Isolate && task.Project != "" && !strings.Contains(task.Intent, "[isolated]") {
				p += "\n\n[isolated] you work in a private copy of the project tree; changes merge back automatically when you finish — do not expect sibling edits to appear live."
			}
			return p
		}
		prompt := assemblePrompt(0)

		// A task stamped remote at intake carries off-node intent: commander
		// holds its agent run to the restricted tool face unless the origin's
		// consent grant authorized it. The persisted flag is authoritative —
		// the chain fallback only catches rows written before the flag
		// existed; a peer can claim chain[0]=us but cannot unset remote.
		remoteOrigin := task.Remote || (len(task.Chain) > 0 && task.Chain[0] != c.nodeID)
		restricted := remoteOrigin && !task.Authorized

		// — Trace: exec_agent_start (orbit Step-3 "starting this stage on N").
		// We report before Execute so the orbit paints the stage bar as
		// running immediately. Best-effort only. The restricted flag lands
		// here — not in the throttled progress stream — so the tool face a
		// remote task ran under is auditable even when the adapter's own
		// "restricted mode" note loses the progress-rate race.
		c.EvTrace(execCtx, taskID, EvExecAgentStart, map[string]any{
			"round":      round + 1,
			"budget":     maxRounds,
			"plan_kind":  plan.Kind,
			"agent":      plan.Agent,
			"adapter":    plan.Adapter,
			"model":      activeModel,
			"node_name":  scheduler.NodeNamePart(c.nodeID),
			"authorized": task.Authorized,
			"restricted": restricted,
			"tier":       plan.Tier,
			"workdir":    workDir,
		})

		// The agent runs inside an inner attempt loop so a context-window
		// overflow can retry once in the same round instead of parking
		// outright: the prompt is ours to shrink and the resumed agent
		// session — its transcript replayed into the window — ours to drop.
		interrupted := false
		overflowRetried := false
		var extraTokens int
		var extraCost float64
		for {
			runCtx := execCtx
			// Attach the resume hint whenever this task carries one. Which
			// attempt may actually use it is decided one level down, per
			// attempt, by buildAdapterRequest's adapter affinity: the minting
			// adapter gets the handle, a foreign one ignores it. The gate must
			// not run here on plan.Adapter — when the plan's CLI is unavailable
			// and the fallback chain lands on the adapter that minted the
			// session, that attempt IS the conversation's owner, and a
			// round-level drop cold-starts the very checkpoint a timeout retry
			// exists to resume.
			if sessionID != "" {
				runCtx = commander.WithResume(execCtx, sessionID, sessionAdapter)
			}
			if remoteOrigin {
				runCtx = commander.WithRemoteTask(runCtx)
			}
			// Structured result contract: a schema-capable adapter (registry
			// SupportsStructuredOutput) gets the protocol schema, so the result
			// arrives parsed — status/question/delegate_requests — instead of
			// relying on text markers alone. Marker parsing below stays as the
			// fallback for every adapter without the flag. The schema carries
			// this node's declared ability ids (a hallucinated child request
			// cannot route) and, for a read-only session, the instruction to ask
			// the user for authorization instead of delegating for it.
			if plan.Kind == "agent" {
				if k, ok := agents.Lookup(plan.Agent, plan.Adapter); ok && k.Capabilities.SupportsStructuredOutput {
					runCtx = commander.WithResultSchema(runCtx, agentResultSchemaFor(c.abilityIDs(), restricted, delegLimits.MaxChildren))
				}
			}
			// The round runs under its own cancel so a user inject (steer.go) can
			// abort the in-flight agent turn: the kill drops the live session
			// (its result envelope would desync the next turn) and the loop
			// re-runs carrying the steer as the leading directive. execCtx
			// cancellation (task cancel) rides through unchanged. Past the steer
			// budget the round is not steerable: an inject then waits for the
			// natural boundary (delivered at the next round, or as a durable
			// note if the task ends first).
			roundCtx := runCtx
			var roundCancel context.CancelFunc
			steerable := steerRounds < maxSteerRounds
			if steerable {
				roundCtx, roundCancel = context.WithCancel(runCtx)
				c.registerInterrupt(taskID, roundCancel)
			}
			res = router.Execute(roundCtx, *plan, prompt, workDir, task.Authorized)
			if steerable {
				c.clearInterrupt(taskID)
				interrupted = roundCtx.Err() != nil && c.steerUnseen(execCtx, taskID, boardSeen)
				roundCancel()
			}
			if interrupted {
				break
			}
			// A context-window overflow cannot be retried verbatim, but the
			// prompt is ours to shrink: compress the folded run notes, drop
			// the resumed session (its replayed transcript is the usual bulk),
			// and re-run once under the tight budget. A second overflow — or
			// any non-overflow failure — lands on the park path below as
			// before.
			if plan.Kind == "agent" && !res.OK && !overflowRetried &&
				commander.ContextOverflow(res.Stderr) {
				overflowRetried = true
				extraTokens += res.Tokens
				extraCost += res.Cost
				c.EvTrace(execCtx, taskID, EvContextOverflow, map[string]any{
					"agent": res.Agent, "round": round, "detail": res.Stderr,
					"action": "compact_retry",
				})
				sessionID = ""
				sessionAdapter = ""
				// A session-mode adapter also keeps the overflowed
				// conversation alive in this task's pool — evict it or the
				// retry lands on the same transcript. One-shot adapters lose
				// nothing; the cleared resume handle already starts them cold.
				commander.DropAgentSession(execCtx, res.Agent, res.Adapter)
				// The persisted checkpoint pointed at the session that just
				// overflowed; leaving it would resume the same transcript on
				// the next interruption and fail identically.
				_ = c.store.SetAgentSession(context.WithoutCancel(execCtx), taskID, "", "", "")
				intentNotes = c.compactRunNotes(execCtx, intentNotes)
				prompt = assemblePrompt(overflowPromptBudget)
				continue
			}
			break
		}
		// Bill the failed overflow attempt too: the provider spent those
		// tokens even though the round's surviving result is the retry's.
		res.Tokens += extraTokens
		res.Cost += extraCost
		if interrupted {
			steerRounds++
			maxRounds++ // the steer round is on the house — it must not eat the task's budget
			c.EvTrace(execCtx, taskID, "user_inject_interrupt", map[string]any{"round": round + 1})
			continue
		}
		structured := parseStructuredResult(res.Structured)
		// The schema's status field is the model's self-report. question and
		// delegate are consumed below; "failed" is honored as a failure rather
		// than traced and forgotten — an adapter can exit 0 with a clean
		// envelope while the model itself says it could not do the work (a
		// CLI-authenticated agent whose turn failed is the common case), and
		// settling that as done tells the user and the delegator a lie. The
		// retry/judge path above this loop is the corrective continuation.
		if structured != nil && structured.Status != "" && structured.Status != "done" {
			c.EvTrace(execCtx, taskID, "structured_status", map[string]any{
				"round": round + 1, "agent": res.Agent, "status": structured.Status,
			})
			applyStructuredFailure(&res, structured)
		}
		if res.SessionID != "" && res.SessionID != sessionID {
			sessionID = res.SessionID
			sessionAdapter = res.Adapter
			// Persist immediately — not at the loop's end — because the
			// interruption this exists for (a yield cancel, a crash) does
			// not wait for a tidy exit. WithoutCancel rides past the very
			// cancellation that makes the row worth writing.
			if err := c.store.SetAgentSession(context.WithoutCancel(execCtx), taskID, sessionID, c.nodeID, res.Adapter); err != nil {
				c.logger.Warn("persist agent session", "task", taskID, "err", err)
			}
		}

		// §6.1 token budget: the adapter's reported usage is billed against
		// the task's mesh-wide quota as soon as the round lands. The spend is
		// atomic in the row, so this charge and a judge charge cannot both
		// draw on the same remainder. An exhausted task stops spending — the
		// judge call below is itself metered, so it must not run either; the
		// result parks in review for a human to top up or close out.
		tokenExhausted := false
		if res.Tokens > 0 {
			left, serr := c.store.SpendTokens(context.WithoutCancel(ctx), taskID, int64(res.Tokens))
			if serr != nil {
				c.logger.Warn("spend task tokens", "task", taskID, "err", serr)
			} else if left < 0 {
				tokenExhausted = true
			}
		}

		// §4.2 Sub-MainAgent promotion: an agent that hits a resource it lacks
		// (GPU, tool, hardware actuator) may emit PANDA_DELEGATE markers — one
		// turn can request several children at once, and they dispatch in
		// parallel rather than serializing round trips. This node — now
		// acting as the children's Sub-Main — spawns each causal child,
		// waits for their results, and re-runs the round with the products
		// folded into the intent. The delegation never reaches a judge: the
		// round is re-driven, not verified.
		if plan.Kind == "agent" {
			// Sibling-board posts (§inter-task messages) parse FIRST: the
			// delegation branch below may re-run the round (round--), and
			// any PANDA_MSG that shared the same output would be lost with
			// the discarded stdout if it waited its turn. A post never
			// re-drives the round; its send receipt folds into next round.
			mreqs, stdoutAfterMsgs := parseMessages(res.Stdout)
			if structured != nil && len(structured.Messages) > 0 {
				mreqs = structured.Messages
			}
			if len(mreqs) > 0 {
				res.Stdout = stdoutAfterMsgs
				for _, note := range c.sendAgentMessages(execCtx, task, mreqs, &msgsSent, c.msgBudget()) {
					intentNotes = append(intentNotes, "[message board] "+note)
				}
			}
			drs, cleaned := parseDelegateRequests(res.Stdout)
			// The structured contract wins when it carries requests: an
			// agent that filled delegate_requests went through the schema,
			// so its field is the parsed truth — while marker cleaning
			// above still strips any stray protocol lines it also printed.
			if structured != nil && len(structured.Delegates) > 0 {
				drs = structured.Delegates
			}
			if len(drs) > 0 {
				res.Stdout = cleaned
				if !canSpawn {
					// The depth cap never gave this task the protocol hint,
					// but a schema-emitted delegate_requests (or a marker it
					// saw in a sibling's board post) still parses — answer
					// with the same refusal vocabulary the budget path uses.
					intentNotes = append(intentNotes, "[system] delegation depth limit reached — this task may not spawn sub-agents; finish with local resources and report.")
				} else if delegations < maxDelegations {
					take := maxDelegations - delegations
					if take > len(drs) {
						take = len(drs)
					}
					delegations += take
					notes := c.delegateChildren(execCtx, task, drs[:take], &pendingChildren)
					for i, note := range notes {
						if len(notes) == 1 {
							intentNotes = append(intentNotes, "[delegated child result]\n"+note)
						} else {
							intentNotes = append(intentNotes, fmt.Sprintf("[delegated child result %d/%d]\n%s", i+1, len(notes), note))
						}
					}
					if take < len(drs) {
						intentNotes = append(intentNotes, "[system] delegation budget for this task is exhausted; remaining requests were not dispatched — finish with local resources and report.")
					}
					// Only a delegation that actually happened re-drives the
					// round; an exhausted budget falls through to the judge so
					// a marker-happy agent cannot loop forever past the cap.
					round--
					continue
				} else {
					intentNotes = append(intentNotes, "[system] delegation budget for this task is exhausted; finish with local resources and report.")
				}
			}
		}

		// §4.3 clarification loop: an agent blocked on input only the user
		// has ends its turn with a PANDA_QUESTION line. Parking in review —
		// not retrying — is what hands the question to the origin: no retry
		// can produce the answer, and the delegator's task_resume.answer is
		// what the re-run folds into the intent. The marker text is stripped
		// from stdout so the protocol line never reaches the user as prose.
		if plan.Kind == "agent" && res.OK {
			q, cleaned, ask := parseQuestionRequest(res.Stdout)
			// The structured contract wins when it carries a question: the
			// schema field is parsed truth, while the marker pass above
			// still strips any protocol line the agent also printed.
			if structured != nil && strings.TrimSpace(structured.Question) != "" &&
				!isNoQuestionSentinel(structured.Question) {
				q, ask = strings.TrimSpace(structured.Question), true
			}
			// Also remove a recognized no-question protocol line when the
			// successful run continues to completion instead of parking.
			res.Stdout = cleaned
			if ask {
				if beforeOK {
					if after, serr := defense.SnapshotDir(workDir); serr == nil {
						filesTruncated = after.Truncated()
						lastChanged = c.filterHostDrift(workDir, defense.DropVCSBookkeeping(before.Changed(after)))
					}
				}
				park := map[string]any{
					"question": q, "stdout": res.Stdout, "files_changed": lastChanged,
				}
				if restricted {
					// The question exists because the session is read-only:
					// record it so the origin's view can say why approving
					// (which re-runs with consent) is the fix.
					park["restricted"] = true
				}
				if err := c.store.PauseForAnswerAttempt(ctx, taskID, c.nodeID, attemptID, park); err != nil {
					if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
						return roundOutcome{}, ErrCancelled
					}
					return roundOutcome{}, fmt.Errorf("pause for clarification: %w", err)
				}
				c.EvTrace(execCtx, taskID, EvClarification, map[string]any{
					"question": q, "round": round + 1, "agent": res.Agent,
				})
				c.logTask(task.Title, false)
				trackTask(c, task.Project, required, task.Title, false)
				fc := lastChanged
				if len(fc) > maxFilesChangedReport {
					fc = fc[:maxFilesChangedReport]
				}
				return roundOutcome{done: true, payload: bus.TaskResultPayload{
					TaskID: taskID, AttemptID: attemptID, State: StateReview,
					ApprovalDisposition: string(ApprovalResumeExecution),
					Question:            q,
					OK:                  true, ExitCode: 0, Stdout: res.Stdout,
					FilesChanged: fc, FilesTruncated: filesTruncated,
					Tokens: res.Tokens, Cost: res.Cost, Agent: res.Agent, Model: res.Model, Injected: res.Injected,
				}}, nil
			}
		}

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
		// verdict pill always reflect a decided round. The judge runs on ANY
		// successful agent round under a supervisor — the final round included —
		// so the predicate is the same one that gates the Supervise call below,
		// minus the token gate (an exhausted round parks on "review" instead).
		// Gated: an authorization refusal parks before agent execution; emitting
		// a failed supervision round would falsely mark that a judge round took place.
		judgeWillRun := plan.Kind == "agent" && c.supervisor != nil && res.OK && !tokenExhausted
		if !judgeWillRun && !commander.IsAuthorizationRefusal(res.Stderr) {
			status := string(verdict.Status)
			if !res.OK {
				// A failed round is never judged (the failure branch below
				// returns before Supervise runs), so reporting the verdict
				// variable — its zero value, or the previous round's — would paint
				// "done" over a failed attempt. The execution outcome is the only
				// truthful status here.
				status = "failed"
			} else if tokenExhausted {
				// The verdict-review assignment below parks this round before a
				// judge call the quota can no longer afford; report the status it
				// lands on, not the stale verdict left over from the prior round.
				status = string(entry.VerdictReview)
			}
			emitRound(status)
		}

		if plan.Kind == "agent" {
			// Structured usage (when the adapter speaks it): the flat Tokens
			// total rides the existing pipeline; the breakdown lands as its
			// own event so input/output/cache traffic can be told apart.
			// One usage event carries both meters when present: the token
			// breakdown and the harness's own delegation fan-out summary
			// (claude result.subagent_stats) — how many sub-agents the run
			// spawned, by type; observability for the everything-is-a-node
			// view.
			if res.Usage != nil || len(res.SubagentStats) > 0 {
				usageData := map[string]any{"agent": res.Agent, "round": round}
				if res.Usage != nil {
					usageData["input_tokens"] = res.Usage.InputTokens
					usageData["output_tokens"] = res.Usage.OutputTokens
					usageData["cache_read_tokens"] = res.Usage.CacheReadTokens
					usageData["cache_write_tokens"] = res.Usage.CacheWriteTokens
				}
				if len(res.SubagentStats) > 0 {
					usageData["subagent_stats"] = res.SubagentStats
				}
				c.EvTrace(execCtx, taskID, EvAgentUsage, usageData)
			}
			// Fallback chain visibility (A2): res.Agent names the agent that
			// actually executed; a mismatch means the scored primary's CLI was
			// unavailable and a runner-up took over. The breaker tracks the agent
			// that really ran.
			if res.Agent != "" && res.Agent != plan.Agent {
				breakerKey = "agent:" + res.Agent
				c.audit(ctx, taskID, "agent:fallback", plan.Agent, "fallback", "primary unavailable, fell back to "+res.Agent)
				// The reason rides the event, not just the audit line: the
				// thread prints one sentence for the swap, and "claude_code
				// 未执行" without a cause is the same dead end as no sentence
				// at all.
				fallback := map[string]any{"from": plan.Agent, "to": res.Agent}
				if res.FallbackReason != "" {
					fallback["reason"] = res.FallbackReason
				}
				if err := c.store.RecordEvent(ctx, taskID, EvAgentFallback, fallback); err != nil {
					c.logger.Warn("record agent fallback event", "task", taskID, "err", err)
				}
			}
			// Explicit injection reminder (A1): when panda overrode the agent's
			// model endpoint, say so at the top of the output, in the audit log,
			// and in the task event stream the Web task detail replays. Gated on
			// the adapter actually running (a tier refusal spawns nothing).
			if res.Injected {
				// res.Injected is set by two different things: the policy
				// decision (inject up front) or the failure-time rescue inside
				// runAdapterDefault (the agent's own model failed). A rescue
				// means the decision itself said "don't inject", so its reason
				// describes the opposite of what happened — name the rescue
				// instead of repeating "the agent ships its own model".
				rescued := !injection.Inject
				injection.Inject = true
				if res.Model != "" {
					injection.Model = res.Model
				}
				if rescued {
					injection.Kind = commander.InjectionKindRescue
					// The retry injected the per-agent pin (injection.agents),
					// not the node model, so resolve the endpoint the same way
					// the adapter did. Reporting the node's URL here would
					// announce an endpoint the run never touched whenever the
					// agent pins its own (e.g. deepseek_harness on the
					// Anthropic-compatible endpoint). The adapter comes from
					// the agent that actually ran: a fallback swaps res.Agent
					// without rewriting the plan.
					if router != nil {
						adapter := plan.Adapter
						if res.Agent != plan.Agent {
							if ag, ok := router.Agent(res.Agent); ok {
								adapter = ag.Adapter
							}
						}
						model, endpoint := router.InjectedModelEndpoint(res.Agent, adapter)
						if injection.Model == "" {
							injection.Model = model
						}
						injection.BaseURL = endpoint
					}
				}
				if injection.BaseURL == "" {
					injection.BaseURL = commander.EffectiveBaseURL(c.model)
				}
				if rescued || injection.Reason == "" {
					injection.Reason = "credential rescue: the agent's own model failed, panda's model was injected for the retry"
				}
			}
			if injection.Inject && res.Agent != "" {
				notice := commander.InjectionNotice(injection, res.Agent)
				if res.Stdout != "" {
					res.Stdout = notice + "\n" + res.Stdout
				} else {
					res.Stdout = notice
				}
				c.audit(ctx, taskID, "model:injected", res.Agent, "injected",
					"model="+injection.Model+" endpoint="+injection.BaseURL+" reason="+injection.Reason)
				key := res.Agent + "|" + injection.Model + "|" + injection.BaseURL + "|" + string(injection.Kind)
				if key != lastInjectionKey {
					lastInjectionKey = key
					if err := c.store.RecordEvent(ctx, taskID, EvModelInjection, map[string]any{
						"agent": res.Agent, "model": injection.Model, "base_url": injection.BaseURL,
						"reason": injection.Reason, "kind": string(injection.Kind),
					}); err != nil {
						c.logger.Warn("record model injection event", "task", taskID, "err", err)
					}
				}
			}
			if res.OK {
				c.breaker.RecordSuccess(breakerKey)
			} else if ctx.Err() != nil || execCtx.Err() != nil {
				// A failure born of a cancelled/expired context is the caller's
				// deadline or an operator cancel, not the agent's fault — feeding
				// it to the breaker would trip a healthy agent's circuit on
				// someone else's timeout (M18).
				c.logger.Debug("breaker: failure attributed to context, not agent",
					"agent", breakerKey, "task", taskID)
			} else if res.ExitCode == 124 && res.SessionID != "" {
				// A timeout/watchdog kill carrying a live session is interrupted
				// progress — the retry resumes the checkpoint — not evidence the
				// agent is broken. Charging the breaker here is what tripped a
				// healthy agent's circuit after three timeouts on a long task.
				c.logger.Debug("breaker: timeout checkpoint is progress, not a failure",
					"agent", breakerKey, "task", taskID)
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

		// The round's workdir diff is computed once for every agent that came
		// back OK: the scope check diffs it for drift, the judge reads it as
		// evidence, and the result contract reports it as files_changed.
		// Host-noise paths are filtered here so all three consumers agree.
		if plan.Kind == "agent" && res.OK && beforeOK {
			after, err := defense.SnapshotDir(workDir)
			if err != nil {
				c.logger.Warn("snapshot workdir after agent", "task", taskID, "err", err)
			} else {
				filesTruncated = after.Truncated()
				if after.Truncated() || after.Skipped() > 0 {
					c.logger.Info("workdir snapshot partial", "task", taskID,
						"truncated", after.Truncated(), "skipped", after.Skipped())
				}
				lastChanged = c.filterHostDrift(workDir, defense.DropVCSBookkeeping(before.Changed(after)))
			}
		}

		// Scope-drift intercept: a successful agent that touched files outside
		// its declared scope has overstepped the task. Pause it for human
		// analysis rather than mark it done, fail it into the retry loop, or
		// re-delegate — a deterministic intercept will not improve on retry.
		// The roots are re-anchored to workDir first: a spec written from the
		// project root ("proj/sub" while workDir is ".../proj") names the same
		// tree, and comparing it verbatim drifted every in-scope change.
		// A truncated diff is partial evidence: churn in the unrecorded tail
		// cannot convict, and parking on prefix noise is a false positive —
		// the intercept only runs on a complete diff.
		if plan.Kind == "agent" && !scope.Empty() && res.OK && filesTruncated {
			c.logger.Info("scope drift check skipped: workdir snapshot truncated",
				"task", taskID, "dir", workDir)
		}
		if plan.Kind == "agent" && !scope.Empty() && res.OK && !filesTruncated {
			if drift := scope.DriftUnder(workDir, lastChanged); len(drift) > 0 {
				msg := "scope drift: agent changed files outside declared scope: " + strings.Join(drift, ", ")
				c.audit(ctx, taskID, "scope:drift", plan.Agent, "denied", msg)
				if err := c.store.PauseDriftAttempt(ctx, taskID, c.nodeID, attemptID, msg, drift); err != nil {
					if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
						return roundOutcome{}, ErrCancelled
					}
					return roundOutcome{}, fmt.Errorf("pause on scope drift: %w", err)
				}
				c.logTask(task.Title, false)
				trackTask(c, task.Project, required, task.Title, false)
				return roundOutcome{done: true, payload: bus.TaskResultPayload{
					TaskID: taskID, AttemptID: attemptID, State: StateReview,
					ApprovalDisposition: string(ApprovalAcceptWork),
					OK:                  false, ExitCode: 1, Stderr: msg,
					Tokens: res.Tokens, Cost: res.Cost, Agent: res.Agent, Model: res.Model, Injected: res.Injected,
				}}, nil
			}
		}

		if res.NeedManual {
			// Manual tasks park in review, not done: the human has not acted yet,
			// and only a task meeting its success definition may enter done. The
			// notify text is preserved as the result so whoever picks it up sees
			// what is being asked of them; Approve/Reject moves it on from there.
			if err := c.store.PauseWithResultAttempt(ctx, taskID, c.nodeID, attemptID, map[string]any{
				"manual": true, "notify": res.Stdout,
			}); err != nil {
				if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
					return roundOutcome{}, ErrCancelled
				}
				return roundOutcome{}, fmt.Errorf("pause manual: %w", err)
			}
			c.logTask(task.Title, false)
			trackTask(c, task.Project, required, task.Title, false)
			return roundOutcome{done: true, payload: bus.TaskResultPayload{
				TaskID: taskID, AttemptID: attemptID, State: StateReview,
				ApprovalDisposition: string(ApprovalAcceptWork),
				OK:                  true, ExitCode: 0, Stdout: res.Stdout,
				Tokens: res.Tokens, Cost: res.Cost, Agent: res.Agent, Model: res.Model, Injected: res.Injected,
			}}, nil
		}

		if !res.OK {
			// A context-window overflow is deterministic: the same prompt will
			// not fit on retry. The in-round compact retry above already got
			// its one shot — surviving here means the intent/manifest alone
			// exceeds the agent's window, so park the task for a human to
			// compress/split it rather than burning another round.
			if plan.Kind == "agent" && commander.ContextOverflow(res.Stderr) {
				msg := "agent context window overflow: " + res.Stderr
				c.EvTrace(context.WithoutCancel(ctx), taskID, EvContextOverflow, map[string]any{
					"agent": res.Agent, "round": round, "detail": res.Stderr,
					"action": "parked",
				})
				wCtx, cancel := c.storeWriteCtx(ctx)
				defer cancel()
				if err := c.store.FailAttempt(wCtx, taskID, c.nodeID, attemptID, msg); err != nil {
					if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
						return roundOutcome{}, ErrCancelled
					}
					return roundOutcome{}, fmt.Errorf("fail: %w", err)
				}
				if err := c.store.ReviewAttempt(wCtx, taskID, c.nodeID, attemptID, msg); err != nil {
					if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
						return roundOutcome{}, ErrCancelled
					}
					return roundOutcome{}, fmt.Errorf("park overflowed task: %w", err)
				}
				c.logTask(task.Title, false)
				trackTask(c, task.Project, required, task.Title, false)
				return roundOutcome{done: true, payload: bus.TaskResultPayload{
					TaskID: taskID, AttemptID: attemptID, State: StateReview,
					ApprovalDisposition: string(ApprovalNeedsChangedInput),
					OK:                  false, ExitCode: res.ExitCode, Stderr: msg,
					Tokens: res.Tokens, Cost: res.Cost, Agent: res.Agent, Model: res.Model, Injected: res.Injected,
				}}, nil
			}
			// A tier-2 authorization refusal is deterministic policy, not a failed
			// attempt: retrying cannot produce consent. Park the task in review —
			// running -> failed -> review, the same shape the local retry loop
			// parks in — so an approval (inline at the ask prompt, or a
			// task_resume from the delegator) can re-run it with consent. Failing
			// here instead would strand the refusal as a dead-end failure on the
			// delegator: it surfaces the reason but offers no way to resolve it.
			if commander.IsAuthorizationRefusal(res.Stderr) {
				wCtx, cancel := c.storeWriteCtx(ctx)
				defer cancel()
				if err := c.store.FailAttempt(wCtx, taskID, c.nodeID, attemptID, res.Stderr); err != nil {
					if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
						return roundOutcome{}, ErrCancelled
					}
					return roundOutcome{}, fmt.Errorf("fail: %w", err)
				}
				if err := c.store.ReviewWithDispositionAttempt(wCtx, taskID, c.nodeID, attemptID, res.Stderr, ApprovalResumeExecution); err != nil {
					if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
						return roundOutcome{}, ErrCancelled
					}
					return roundOutcome{}, fmt.Errorf("park refused task: %w", err)
				}
				c.logTask(task.Title, false)
				trackTask(c, task.Project, required, task.Title, false)
				return roundOutcome{done: true, payload: bus.TaskResultPayload{
					TaskID: taskID, AttemptID: attemptID, State: StateReview,
					ApprovalDisposition: string(ApprovalResumeExecution),
					OK:                  false, ExitCode: res.ExitCode, Stderr: res.Stderr,
					Tokens: res.Tokens, Cost: res.Cost, Agent: res.Agent, Model: res.Model, Injected: res.Injected,
				}}, nil
			}
			wCtx, cancel := c.storeWriteCtx(ctx)
			defer cancel()
			if err := c.store.FailAttempt(wCtx, taskID, c.nodeID, attemptID, res.Stderr); err != nil {
				if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
					return roundOutcome{}, ErrCancelled
				}
				return roundOutcome{}, fmt.Errorf("fail: %w", err)
			}
			c.logTask(task.Title, false)
			trackTask(c, task.Project, required, task.Title, false)
			return roundOutcome{done: true, payload: bus.TaskResultPayload{
				TaskID: taskID, AttemptID: attemptID, State: StateFailed, OK: false, ExitCode: res.ExitCode, Stderr: res.Stderr,
				Tokens: res.Tokens, Cost: res.Cost, Agent: res.Agent, Model: res.Model, Injected: res.Injected,
				AgentSessionID: sessionID, AgentSessionAdapter: sessionAdapter,
			}}, nil
		}

		// Supervision applies only to agent tasks under a configured supervisor.
		if plan.Kind != "agent" || c.supervisor == nil {
			break
		}
		if tokenExhausted {
			// The metered spend above drained the quota: park what the round
			// produced rather than paying a judge call on top of it (§6.1).
			verdict.Status = entry.VerdictReview
			verdict.Reason = "token budget exhausted"
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
		judgeResult := res.Stdout
		if strings.TrimSpace(res.Stderr) != "" {
			judgeResult = judgeResult + "\n\n错误输出（stderr）：\n" + res.Stderr
		}
		// Deterministic evidence rides the judge payload: the exit code and the
		// declared scope are facts the prose cannot dress up, and a judge that
		// only reads the agent's own summary is judging the summary.
		judgeResult += "\n\n[执行退出码] " + strconv.Itoa(res.ExitCode)
		if scope := taskScope(task.SpecJSON); scope != "" {
			judgeResult += "\n[任务范围] " + scope
		}
		// The footprint is evidence the prose cannot fake: an agent reporting
		// success while its diff is empty is the classic delegation failure,
		// and a judge that only reads text cannot catch it.
		if len(lastChanged) > 0 {
			shown := lastChanged
			if len(shown) > 50 {
				shown = shown[:50]
			}
			judgeResult += "\n\n[本轮实际变更的文件] " + strings.Join(shown, ", ")
		} else if beforeOK {
			judgeResult += "\n\n[本轮实际变更的文件] （无 —— 工作目录没有任何文件改动）"
		}
		// Supervise under execCtx, not the handler's ctx: the judge call is part
		// of this execution, so it must honor the task's cancel (cancelRunning
		// kills execCtx on force-fail/cancel) — and must NOT die with the
		// delegator's websocket read loop, which is what the bare handler ctx is
		// scoped to. Using ctx here meant a dropped peer link aborted the judge
		// mid-call, parking work for review that a live judge would have
		// accepted (or rejected with a real verdict).
		v, serr := entry.Supervise(execCtx, c.supervisor, fullIntent(), judgeResult)
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
		// A "done" that leaves no trace is not done. When the working tree was
		// readable and the round produced neither a byte of output nor a single
		// changed file, the verdict is the model agreeing with itself — park it
		// for a human instead of closing the task on silence. This is the one
		// completion rule that needs no second model call.
		if verdict.Status == entry.VerdictDone && beforeOK &&
			strings.TrimSpace(res.Stdout) == "" && len(lastChanged) == 0 {
			verdict.Status = entry.VerdictReview
			verdict.Reason = "agent reported success with no output and no changed files"
		}
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
			// A user inject that landed while the judge deliberated outranks
			// the verdict: run one more round carrying it rather than
			// closing the task with the message unread.
			if c.steerUnseen(execCtx, taskID, boardSeen) && steerRounds < maxSteerRounds {
				steerRounds++
				maxRounds++
				continue
			}
			break
		}
		if v.Status == entry.VerdictReview {
			// No verdict obtainable: stop iterating and let the review branch
			// below hand the result to a human.
			break
		}

		// Stagnation detection: if this agent repeated identical output across rounds on a continue verdict,
		// it is stuck in a loop. Failover to an alternate agent if available, or stop early.
		if round > 0 && res.Agent == lastAgent && isOutputStagnant(res.Stdout, lastOutput, res.Stderr, lastStderr) {
			promoted := false
			for i, alt := range plan.Alternates {
				if c.breaker.Allow("agent:" + alt) {
					if altAg, ok := router.Agent(alt); ok {
						c.audit(ctx, taskID, "agent:stagnation_failover", lastAgent, "failover", "agent "+lastAgent+" stagnated across rounds; failing over to "+alt)
						if recErr := c.store.RecordEvent(context.WithoutCancel(ctx), taskID, EvAgentFallback, map[string]any{
							"from": lastAgent, "to": alt, "reason": "stagnation",
						}); recErr != nil {
							c.logger.Warn("record agent stagnation fallback event", "task", taskID, "err", recErr)
						}
						plan.Agent = alt
						plan.Ability = alt
						plan.Adapter = altAg.Adapter
						plan.Tier = altAg.Tier
						if plan.Tier == 0 {
							plan.Tier = defense.TierReversible
						}
						newAlts := make([]string, 0, len(plan.Alternates)-1)
						newAlts = append(newAlts, plan.Alternates[:i]...)
						newAlts = append(newAlts, plan.Alternates[i+1:]...)
						plan.Alternates = newAlts
						sessionID = ""
						sessionAdapter = ""
						// A different agent owns no handle to the old one's
						// conversation; leaving it persisted would resume the
						// wrong adapter's session on the next interruption.
						_ = c.store.SetAgentSession(context.WithoutCancel(execCtx), taskID, "", "", "")
						promoted = true
						break
					}
				}
			}
			if !promoted {
				c.audit(ctx, taskID, "agent:stalled", res.Agent, "stalled", "no progress across rounds and no alternate agents available")
				verdict.Status = entry.VerdictReview
				verdict.Reason = fmt.Sprintf("agent %s stalled: identical output across consecutive rounds with no alternate agents", res.Agent)
				break
			}
		}

		lastAgent = res.Agent
		lastOutput = res.Stdout
		lastStderr = res.Stderr

		if strings.TrimSpace(v.Followup) == "" {
			intentNotes = append(intentNotes, "上一轮未能完整完成，请继续完成剩余工作，并汇报最终结果。")
		} else {
			intentNotes = append(intentNotes, "[上级补充指令]\n"+v.Followup)
		}
	}
	// §4.2 final fold: a run must not report a verdict while children it
	// delegated are still outstanding — that is the "declared done while a
	// sub-task is still running" lie from the orchestrator's side. Hold until
	// they land (folded into the reported output), park (annotated), or the
	// task's own deadline arrives. One last row re-read first: a child
	// spawned during the final round is not in the loop's snapshot.
	c.mergeLiveChildren(execCtx, taskID, &pendingChildren)
	if len(pendingChildren) > 0 {
		if notes := c.awaitPendingChildren(execCtx, task, pendingChildren); len(notes) > 0 {
			res.Stdout += "\n\n[delegated child results]\n" + strings.Join(notes, "\n")
		}
	}
	return roundOutcome{res: res, lastChanged: lastChanged, filesTruncated: filesTruncated,
		verdict: verdict, sessionID: sessionID, sessionAdapter: sessionAdapter}, nil
}

// annotateAgentSubstitution prefixes the result with a note whenever the
// harness that actually ran differs from the one the plan chose. An agent
// pin ("agent:<name>") picks the plan's primary harness; when that harness
// turns out unusable on the executor, the fallback chain runs the next
// available one. The fallback is deliberate — refusing the whole task serves
// nobody — but a substitution nobody sees reads as a silent bait-and-switch,
// so the result names both agents.
func annotateAgentSubstitution(plan commander.Plan, res commander.Result) commander.Result {
	if plan.Kind != "agent" || res.Agent == "" || plan.Agent == "" || res.Agent == plan.Agent {
		return res
	}
	note := fmt.Sprintf("[panda] agent substitution: %q was unavailable on the executor; %q ran this task. / 代理替换：%q 不可用，实际由 %q 执行。",
		plan.Agent, res.Agent, plan.Agent, res.Agent)
	if strings.TrimSpace(res.Stdout) == "" {
		res.Stdout = note
	} else {
		res.Stdout = note + "\n\n" + res.Stdout
	}
	return res
}

// finalizeRun is run()'s terminal routing: pack the workdir into its
// output artifact, then land the row in review (judge reject, unauthorized
// Tier-2) or done. Every return is already the wire payload run reports.
func (c *Core) finalizeRun(ctx context.Context, taskID string, task Task, plan commander.Plan,
	required []string, attemptID, workDir string, res commander.Result,
	lastChanged []string, filesTruncated bool, verdict entry.SuperviseVerdict,
	sessionID, sessionAdapter string,
	checks []contract.Check, baseline []contract.Result) (bus.TaskResultPayload, error) {
	// A stage hands its work-dir to its successors as a content-addressed
	// artifact. It is packed before the terminal branches below, not inside one of
	// them, because a stage that parks in review still produced a tree and the
	// hash has to travel with the result either way. A pack failure fails the
	// stage: reporting done without an artifact would block every successor
	// forever on something that was never produced.
	// The result's footprint: every agent outcome below — done, review, parked —
	// reports the workdir-relative paths the last completed round changed, so
	// the delegator sees what was touched instead of trusting the agent's prose.
	filesChanged := lastChanged
	if len(filesChanged) > maxFilesChangedReport {
		filesChanged = filesChanged[:maxFilesChangedReport]
	}
	res = annotateAgentSubstitution(plan, res)
	var outputArtifact string
	if task.PlanID != "" {
		var perr error
		if outputArtifact, perr = c.packStageOutput(ctx, task, workDir); perr != nil {
			return bus.TaskResultPayload{}, perr
		}
	} else if projectInputs(task) || attachedInputs(task) {
		// A task executed on someone else's behalf hands its tree back the
		// same way a stage does, so the changes it made reach the machine that
		// asked. A pack failure is a warning rather than a failure here: the work
		// itself succeeded, and reporting it failed would be a worse lie than
		// reporting it without the tree.
		if hash, perr := c.packStageOutput(ctx, task, workDir); perr != nil {
			c.logger.Warn("pack task output", "task", taskID, "err", perr)
		} else {
			outputArtifact = hash
		}
	} else if task.Project == "" && task.WorkDir == "" && c.artifacts != nil && dirHasFiles(workDir) {
		// A bare task runs in its private per-task dir (prepareRunDir's
		// attachedWorkDir branch): anything it left there IS the output —
		// an actuator's screenshot, an agent's report file. Packing it is
		// what lets `panda task artifact` and the panel preview reach the
		// produced files instead of stranding them under work_path. An
		// empty dir (a pure-stdout run) packs nothing; a project/pinned
		// dir is shared ground and is never bulk-packed by this branch.
		if hash, perr := c.packStageOutput(ctx, task, workDir); perr != nil {
			c.logger.Warn("pack task output", "task", taskID, "err", perr)
		} else {
			outputArtifact = hash
		}
	}
	// Display content travels under the mesh-only rule: flag the result so
	// every delegator hop stamps its row. The claim-time row stamp covers
	// actuator runs resolved on this card; taskScreenSensitive covers
	// requires/spec-declared capture and screen-marked inputs on top.
	screenOut := outputArtifact != "" &&
		((plan.ActuatorID != "" && c.actuatorIsScreen(plan.ActuatorID)) || c.taskScreenSensitive(task))
	if screenOut && !task.ScreenOutput {
		// Keep the executor's own row consistent with the result flag:
		// resumeOutcomeFor re-serves the stored payload under the row's
		// stamp, and the serving verdicts for the produced artifact read
		// this row long after the run that packed it.
		if err := c.store.SetScreenOutput(ctx, taskID); err != nil {
			c.logger.Warn("stamp screen output on executor row", "task", taskID, "err", err)
		} else {
			task.ScreenOutput = true
		}
	}

	// verificationStatus records the contract verdict for every terminal
	// payload below. Empty means the task carried no contract at all.
	verificationStatus := ""

	// The supervisor did not accept the result: either work still remains after
	// the round budget ("continue"), or the model answered without a usable
	// verdict ("review" — unparsable output, or an unreachable supervisor that
	// parks rather than accepts unverified work, review P1-6). Both park in
	// review with the latest result and a marker so a human sees what was done
	// and what remains, rather than looping indefinitely or silently accepting
	// unverified work.
	if plan.Kind == "agent" && c.supervisor != nil &&
		(verdict.Status == entry.VerdictContinue || verdict.Status == entry.VerdictReview) {
		wCtx, cancel := c.storeWriteCtx(ctx)
		defer cancel()
		if err := c.store.PauseWithResultAttempt(wCtx, taskID, c.nodeID, attemptID, map[string]any{
			"ok": true, "exit_code": res.ExitCode, "stdout": res.Stdout, "agent": res.Agent,
			"needs_followup": verdict.Status == entry.VerdictContinue,
			"verdict":        verdict.Status, "verdict_reason": verdict.Reason,
			"files_changed": filesChanged,
		}); err != nil {
			if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
				return bus.TaskResultPayload{}, ErrCancelled
			}
			return bus.TaskResultPayload{}, fmt.Errorf("pause for follow-up: %w", err)
		}
		c.logTask(task.Title, false)
		trackTask(c, task.Project, required, task.Title, false)
		return bus.TaskResultPayload{
			TaskID: taskID, AttemptID: attemptID, State: StateReview,
			ApprovalDisposition: string(ApprovalAcceptWork),
			OK:                  true, ExitCode: res.ExitCode, Stdout: res.Stdout,
			Tokens: res.Tokens, Cost: res.Cost, OutputArtifact: outputArtifact, ScreenOutput: screenOut, FilesChanged: filesChanged, FilesTruncated: filesTruncated, Agent: res.Agent, Model: res.Model, Injected: res.Injected,
			AgentSessionID: sessionID, AgentSessionAdapter: sessionAdapter,
		}, nil
	}

	// Delivery contract verdict. This is the orchestrator's own judgment of
	// the work, independent of anything the agent said about itself: the
	// checks run here, in this workspace, after the run. A failed contract
	// fails the task before an isolated worktree is merged back, so an
	// unmet contract can never land in the shared tree. A run that already
	// failed needs no verdict — the task is failed either way, and a full
	// suite after it would only spend minutes confirming that.
	if len(checks) > 0 && res.OK && ctx.Err() == nil {
		final := c.runAcceptance(ctx, taskID, acceptancePhaseFinal, workDir, checks)
		v := contract.Evaluate(checks, baseline, final)
		verificationStatus = v.Status
		c.EvTrace(context.WithoutCancel(ctx), taskID, EvVerdict, map[string]any{
			"status":       v.Status,
			"passed":       v.Passed,
			"failed":       v.Failed,
			"regressions":  v.Regressions,
			"pre_existing": v.PreExisting,
			"skipped":      v.Skipped,
			"flaky":        v.Flaky,
			"reasons":      v.Reasons,
		})
		if err := c.store.SetVerification(context.WithoutCancel(ctx), taskID, v.Status); err != nil {
			c.logger.Warn("persist verification", "task", taskID, "err", err)
		}
		// The summary is part of the result's stdout so an origin node without
		// this task's detail table still sees the evidence next to the work.
		if summary := acceptanceSummary(v, task.GetUserLocale()); summary != "" {
			if res.Stdout != "" {
				res.Stdout += "\n\n"
			}
			res.Stdout += summary
		}
		if v.Status == contract.VerdictFailed {
			reason := acceptanceFailureReason(v)
			wCtx, cancel := c.storeWriteCtx(ctx)
			defer cancel()
			if err := c.store.FailAttempt(wCtx, taskID, c.nodeID, attemptID, reason); err != nil {
				if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
					return bus.TaskResultPayload{}, ErrCancelled
				}
				return bus.TaskResultPayload{}, fmt.Errorf("fail on acceptance: %w", err)
			}
			c.logTask(task.Title, false)
			trackTask(c, task.Project, required, task.Title, false)
			return bus.TaskResultPayload{
				TaskID: taskID, AttemptID: attemptID, State: StateFailed,
				OK: false, ExitCode: 1, Stdout: res.Stdout, Stderr: reason,
				Tokens: res.Tokens, Cost: res.Cost,
				OutputArtifact: outputArtifact, ScreenOutput: screenOut,
				FilesChanged: filesChanged, FilesTruncated: filesTruncated,
				Agent: res.Agent, Model: res.Model, Injected: res.Injected,
				AgentSessionID: sessionID, AgentSessionAdapter: sessionAdapter,
				Verification: v.Status, NoRetry: true,
			}, nil
		}
	}

	// Terminal routing. An accepted irreversible (Tier-2) agent task whose run
	// was already consented to — via --authorize at submit, or by approving and
	// resuming the refusal's review — completes directly: that consent is the single explicit approval, and a second
	// sign-off on the finished result would add friction without adding
	// information, since the side effects already happened under it. Only the
	// anomalous case — a Tier-2 agent result that reached this branch without
	// authorization (the executor's gate refuses those, so this is defense in
	// depth) — still parks in review for human sign-off.
	if plan.Kind == "agent" && plan.Tier >= defense.TierIrreversible {
		parked := !task.Authorized
		// — Trace: the terminal Tier-2 agent outcome. Wire shape matches
		// design doc §3.1.1: operations: [{op,target,risk}].
		c.EvTrace(context.WithoutCancel(ctx), taskID, EvTier2Triggered, map[string]any{
			"operations":       []map[string]any{{"op": plan.Agent, "target": "", "risk": "medium"}},
			"kind":             plan.Kind,
			"tier":             plan.Tier,
			"result":           "authorized",
			"parked_in_review": parked,
		})
		if parked {
			wCtx, cancel := c.storeWriteCtx(ctx)
			defer cancel()
			if err := c.store.PauseWithResultAttempt(wCtx, taskID, c.nodeID, attemptID, map[string]any{
				"ok": true, "exit_code": res.ExitCode, "stdout": res.Stdout, "agent": res.Agent,
				"files_changed": filesChanged,
			}); err != nil {
				if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
					return bus.TaskResultPayload{}, ErrCancelled
				}
				return bus.TaskResultPayload{}, fmt.Errorf("pause for approval: %w", err)
			}
			c.logTask(task.Title, true)
			trackTask(c, task.Project, required, task.Title, true)
			return bus.TaskResultPayload{
				TaskID: taskID, AttemptID: attemptID, State: StateReview,
				ApprovalDisposition: string(ApprovalAcceptWork),
				OK:                  true, ExitCode: res.ExitCode, Stdout: res.Stdout,
				Tokens: res.Tokens, Cost: res.Cost, OutputArtifact: outputArtifact, ScreenOutput: screenOut, FilesChanged: filesChanged, FilesTruncated: filesTruncated, Agent: res.Agent, Model: res.Model, Injected: res.Injected,
				AgentSessionID: sessionID, AgentSessionAdapter: sessionAdapter,
			}, nil
		}
		// Consent already on record: audit the auto-acceptance the way an
		// authorized native Tier-2 run is audited, then fall through to
		// Complete.
		c.audit(context.WithoutCancel(ctx), taskID, "agent:tier2", plan.Agent, "authorized", "completed under submit-time consent")
	}

	// An isolated child's workspace is a private copy: its changes land back
	// on the shared project tree before the task reports done, through the
	// three-way merge (project locks held for its duration). The note rides
	// the result so the parent's fold and the task row both carry the merge
	// outcome — applied count, or the conflicting paths left with the
	// project's bytes. Placement matters: after the output artifact above
	// packed the workspace, before Complete so the row's stdout is final.
	if task.Isolate && task.Project != "" && task.PlanID == "" {
		if note := c.mergeIsolatedRun(ctx, task); note != "" {
			if res.Stdout != "" {
				res.Stdout += "\n"
			}
			res.Stdout += note
		}
	}

	wCtx, cancel := c.storeWriteCtx(ctx)
	defer cancel()
	if err := c.store.CompleteAttempt(wCtx, taskID, c.nodeID, attemptID, map[string]any{
		"ok": true, "exit_code": res.ExitCode, "stdout": res.Stdout, "files_changed": filesChanged,
		// Who actually ran the work rides the row, not only the wire payload:
		// a fallback can swap the agent, and `panda task show` (or the panel)
		// must be able to name the one that executed instead of reporting the
		// agent the plan intended.
		"agent": res.Agent, "model": res.Model, "injected": res.Injected,
		// The contract verdict travels with the row so "done" and "verified"
		// can never be confused after the fact.
		"verified": verificationStatus == contract.VerdictVerified, "verification": verificationStatus,
	}); err != nil {
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
			return bus.TaskResultPayload{}, ErrCancelled
		}
		return bus.TaskResultPayload{}, fmt.Errorf("complete: %w", err)
	}
	// A finished run's session handle is dead weight: the adapter may purge
	// it, and a later re-run must start fresh rather than resume a stale id.
	_ = c.store.SetAgentSessionID(wCtx, taskID, "")
	c.logTask(task.Title, true)
	trackTask(c, task.Project, required, task.Title, true)
	return bus.TaskResultPayload{
		TaskID: taskID, AttemptID: attemptID, State: StateDone, OK: true, ExitCode: res.ExitCode, Stdout: res.Stdout,
		Tokens: res.Tokens, Cost: res.Cost, OutputArtifact: outputArtifact, ScreenOutput: screenOut, FilesChanged: filesChanged, FilesTruncated: filesTruncated, Agent: res.Agent, Model: res.Model, Injected: res.Injected,
		// The contract verdict rides the wire so the origin node can show
		// "done, verified" instead of a bare done it cannot substantiate.
		Verified: verificationStatus == contract.VerdictVerified, Verification: verificationStatus,
		// Reported even though the local row cleared it: the delegator keeps
		// the handle so a re-dispatch back here can offer ResumeSessionID.
		AgentSessionID: sessionID, AgentSessionAdapter: sessionAdapter,
	}, nil
}

// maxFilesChangedReport caps the file list a result carries on the wire: the
// footprint's job is showing what the task touched, and the first few hundred
// paths show that fine — a node_modules-scale rewrite would only drown it.
const maxFilesChangedReport = 200

// applyStructuredFailure turns a model's self-reported failed status into a
// failed result. The adapter envelope's ok field only reports whether the CLI
// turn itself completed; it cannot know that the model answered "I could not
// reach the device". Without this, that run settles as done and the user and
// every upstream delegator are told work succeeded when it did not. The
// model's own answer becomes the failure reason when the adapter left stderr
// empty, so the retry loop and the task timeline carry something actionable.
func applyStructuredFailure(res *commander.Result, sr *structuredAgentResult) {
	if res == nil || sr == nil || sr.Status != "failed" {
		return
	}
	res.OK = false
	if strings.TrimSpace(res.Stderr) != "" {
		return
	}
	reason := strings.TrimSpace(sr.Answer)
	if reason == "" {
		reason = "agent reported status: failed"
	}
	res.Stderr = reason
}

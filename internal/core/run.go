// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/providers"
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
	defer func() {
		if out.TaskID != "" {
			out.DurationMS = time.Since(start).Milliseconds()
			out.Executor = c.nodeID
			out.WorkDir = execWorkDir
			if len(out.Chain) == 0 {
				out.Chain = taskChain
			}
		} else if rerr != nil && !errors.Is(rerr, ErrAlreadyRunning) {
			// If run failed or cancelled before reaching a terminal state,
			// fail the task in the store so it never lingers stranded in
			// 'running'. ErrAlreadyRunning is excluded: the row is legitimately
			// running under another goroutine, and failing it here would kill
			// that owner's work mid-flight.
			wCtx, cancel := c.storeWriteCtx(ctx)
			defer cancel()
			if t, err := c.store.Get(wCtx, taskID); err == nil && t.State == StateRunning {
				_ = c.store.Fail(wCtx, taskID, c.nodeID, rerr.Error())
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

	work := &runWork{
		c: c, taskID: taskID, intent: intent, required: required,
		task: task, plan: plan, router: router, breakerKey: breakerKey, injection: injection,
		execCtx: execCtx, attemptID: attemptID,
		workDir: prep.workDir, oscKey: prep.oscKey, scope: prep.scope,
		before: prep.before, beforeOK: prep.beforeOK,
		shadowConflicts: prep.shadowConflicts,
	}
	outcome, err := work.executeRounds(ctx)
	if err != nil {
		return bus.TaskResultPayload{}, err
	}
	if outcome.done {
		return outcome.payload, nil
	}
	return c.finalizeRun(ctx, taskID, task, work.plan, required, attemptID, prep.workDir,
		outcome.res, outcome.lastChanged, outcome.verdict, outcome.sessionID)
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
		return commander.Plan{}, nil, "", fmt.Errorf("route: no usable agent for %v on this node (cli/credentials/endpoint check failed)", required)
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
	if plan.ActuatorID != "" {
		if err := commander.SubstituteActionSpec(plan, spec, intent); err != nil {
			unlock()
			return Task{}, nil, fmt.Errorf("route actuator: %w", err)
		}
		// The resolved actuator is a physical device: serialize its driver
		// across every execution path this run() serves — inline submits and
		// delegated accepts never pass through the queue's resource-key
		// registry, and a queue task's keys only decide when it starts, not
		// what its driver may overlap with. Blocking here, before Accept,
		// keeps the row in dispatched while a sibling finishes on the same
		// device rather than running two drivers on one pin concurrently.
		unlock = c.lockActuator(plan.ActuatorID)
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
	// agents "revisits". Anonymous tasks share the node-wide workDir, so
	// keying them by directory would conflate unrelated work; they are exempt.
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
	}
	return execCtx, cleanup
}

// roundOutcome is superviseRounds' answer: either a terminal payload a
// mid-loop branch already decided (done), or the post-loop state finalizeRun
// turns into one. The loop's review parks and refusals already build the
// whole result envelope, so wrapping — rather than re-deriving — keeps them
// byte-identical.
type roundOutcome struct {
	payload     bus.TaskResultPayload
	done        bool
	res         commander.Result
	lastChanged []string
	verdict     entry.SuperviseVerdict
	sessionID   string
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

	currentIntent := intent + taskSpecEnvelope(task.SpecJSON)
	if shadowConflicts != nil {
		// The winner's bytes won on these paths: tell the resumed agent
		// exactly which of its preempted edits died so it re-applies them
		// deliberately instead of assuming the shadow restored everything.
		currentIntent += "\n\n[system] this task was preempted and resumed from a shadow work copy; " +
			"the files it edited that the preempting task also changed kept the OTHER version — " +
			"re-apply your intended edits to: " + strings.Join(shadowConflicts, ", ")
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
	if sessionID != "" {
		c.EvTrace(execCtx, taskID, "adapter_session_resume", map[string]any{
			"session": sessionID, "agent": plan.Agent,
		})
	}
	var lastAgent, lastOutput, lastStderr string
	// lastChanged carries the last completed round's workdir footprint out of
	// the loop: the judge sees it as evidence (an agent claiming done while
	// touching nothing is the classic failure), and the result contract below
	// reports it as files_changed.
	var lastChanged []string
	verdict := entry.SuperviseVerdict{Status: entry.VerdictDone}
	// delegations bounds how many PANDA_DELEGATE promotions one task may make
	// (§4.2). Each re-runs the round with the child's result folded into the
	// intent, so the counter — not the judge budget — is the guard against a
	// marker-happy agent; the mesh budget bounds the spawned tree itself.
	const maxDelegateRequests = 4
	delegations := 0
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
	for round := 0; round < maxRounds; round++ {
		// Fold delegate children that resolved since the last round before
		// the agent runs again: a result that arrived after its wait window
		// still reaches the prompt, so "the child answered too late" stops
		// meaning "the answer was lost".
		if len(pendingChildren) > 0 {
			for _, note := range c.foldLateChildren(execCtx, &pendingChildren) {
				currentIntent += "\n\n[delegated child result arrived]\n" + note
			}
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
		if left, serr := c.store.SpendTokens(ctx, taskID, 0); serr == nil && left < 0 {
			msg := "token budget exhausted"
			if err := c.store.Pause(ctx, taskID, c.nodeID, msg); err != nil {
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

		prompt, skillsUsed := buildAgentPrompt(c, currentIntent, task.Project, task.Title, workDir, promptLang, outputLang)
		usedSkills = skillsUsed

		// — Trace: exec_agent_start (orbit Step-3 "starting this stage on N").
		// We report before Execute so the orbit paints the stage bar as
		// running immediately. Best-effort only.
		c.EvTrace(execCtx, taskID, EvExecAgentStart, map[string]any{
			"round":      round + 1,
			"budget":     maxRounds,
			"plan_kind":  plan.Kind,
			"agent":      plan.Agent,
			"adapter":    plan.Adapter,
			"model":      activeModel,
			"authorized": task.Authorized,
			"tier":       plan.Tier,
			"workdir":    workDir,
		})

		runCtx := execCtx
		if sessionID != "" {
			runCtx = commander.WithResume(execCtx, sessionID)
		}
		// A task stamped remote at intake carries off-node intent: commander
		// holds its agent run to the restricted tool face unless the origin's
		// consent grant authorized it. The persisted flag is authoritative —
		// the chain fallback only catches rows written before the flag
		// existed; a peer can claim chain[0]=us but cannot unset remote.
		remoteOrigin := task.Remote || (len(task.Chain) > 0 && task.Chain[0] != c.nodeID)
		restricted := remoteOrigin && !task.Authorized
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
				runCtx = commander.WithResultSchema(runCtx, agentResultSchemaFor(c.abilityIDs(), restricted))
			}
		}
		res = router.Execute(runCtx, *plan, prompt, workDir, task.Authorized)
		structured := parseStructuredResult(res.Structured)
		// The schema's status field is the model's self-report. question and
		// delegate are consumed below; "failed" is traced as evidence for the
		// judge rather than flipping res.OK — a task kill here would bypass
		// the supervision round that could issue a corrective continuation.
		if structured != nil && structured.Status != "" && structured.Status != "done" {
			c.EvTrace(execCtx, taskID, "structured_status", map[string]any{
				"round": round + 1, "agent": res.Agent, "status": structured.Status,
			})
		}
		if res.SessionID != "" && res.SessionID != sessionID {
			sessionID = res.SessionID
			// Persist immediately — not at the loop's end — because the
			// interruption this exists for (a yield cancel, a crash) does
			// not wait for a tidy exit. WithoutCancel rides past the very
			// cancellation that makes the row worth writing.
			if err := c.store.SetAgentSession(context.WithoutCancel(execCtx), taskID, sessionID, c.nodeID); err != nil {
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
				if delegations < maxDelegateRequests {
					take := maxDelegateRequests - delegations
					if take > len(drs) {
						take = len(drs)
					}
					delegations += take
					notes := c.delegateChildren(execCtx, task, drs[:take], &pendingChildren)
					for i, note := range notes {
						if len(notes) == 1 {
							currentIntent += "\n\n[delegated child result]\n" + note
						} else {
							currentIntent += fmt.Sprintf("\n\n[delegated child result %d/%d]\n%s", i+1, len(notes), note)
						}
					}
					if take < len(drs) {
						currentIntent += "\n\n[system] delegation budget for this task is exhausted; remaining requests were not dispatched — finish with local resources and report."
					}
					// Only a delegation that actually happened re-drives the
					// round; an exhausted budget falls through to the judge so
					// a marker-happy agent cannot loop forever past the cap.
					round--
					continue
				}
				currentIntent += "\n\n[system] delegation budget for this task is exhausted; finish with local resources and report."
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
			if structured != nil && strings.TrimSpace(structured.Question) != "" {
				q, ask = strings.TrimSpace(structured.Question), true
			}
			if ask {
				res.Stdout = cleaned
				if beforeOK {
					if after, serr := defense.SnapshotDir(workDir); serr == nil {
						lastChanged = c.filterHostDrift(workDir, before.Changed(after))
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
				if err := c.store.PauseForAnswer(ctx, taskID, c.nodeID, park); err != nil {
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
					FilesChanged: fc,
					Tokens:       res.Tokens, Cost: res.Cost, Agent: res.Agent, Model: res.Model, Injected: res.Injected,
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
				if err := c.store.RecordEvent(ctx, taskID, EvAgentFallback, map[string]any{"from": plan.Agent, "to": res.Agent}); err != nil {
					c.logger.Warn("record agent fallback event", "task", taskID, "err", err)
				}
			}
			// Explicit injection reminder (A1): when panda overrode the agent's
			// model endpoint, say so at the top of the output, in the audit log,
			// and in the task event stream the Web task detail replays. Gated on
			// the adapter actually running (a tier refusal spawns nothing).
			if res.Injected {
				injection.Inject = true
				if res.Model != "" {
					injection.Model = res.Model
				}
				if injection.BaseURL == "" {
					injection.BaseURL = commander.EffectiveBaseURL(c.model)
				}
				if injection.Reason == "" {
					injection.Reason = "credential rescue fallback"
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
				if err := c.store.RecordEvent(ctx, taskID, EvModelInjection, map[string]any{
					"agent": res.Agent, "model": injection.Model, "base_url": injection.BaseURL, "reason": injection.Reason,
				}); err != nil {
					c.logger.Warn("record model injection event", "task", taskID, "err", err)
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
				lastChanged = c.filterHostDrift(workDir, before.Changed(after))
			}
		}

		// Scope-drift intercept: a successful agent that touched files outside
		// its declared scope has overstepped the task. Pause it for human
		// analysis rather than mark it done, fail it into the retry loop, or
		// re-delegate — a deterministic intercept will not improve on retry.
		// The roots are re-anchored to workDir first: a spec written from the
		// project root ("proj/sub" while workDir is ".../proj") names the same
		// tree, and comparing it verbatim drifted every in-scope change.
		if plan.Kind == "agent" && !scope.Empty() && res.OK {
			if drift := scope.DriftUnder(workDir, lastChanged); len(drift) > 0 {
				msg := "scope drift: agent changed files outside declared scope: " + strings.Join(drift, ", ")
				c.audit(ctx, taskID, "scope:drift", plan.Agent, "denied", msg)
				if err := c.store.PauseDrift(ctx, taskID, c.nodeID, msg, drift); err != nil {
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
			if err := c.store.PauseWithResult(ctx, taskID, c.nodeID, map[string]any{
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
			// not fit on retry, so park the task for a human to compress/split
			// it rather than burning rounds on a re-run that cannot succeed.
			if plan.Kind == "agent" && commander.ContextOverflow(res.Stderr) {
				msg := "agent context window overflow: " + res.Stderr
				c.EvTrace(context.WithoutCancel(ctx), taskID, EvContextOverflow, map[string]any{
					"agent": res.Agent, "round": round, "detail": res.Stderr,
				})
				wCtx, cancel := c.storeWriteCtx(ctx)
				defer cancel()
				if err := c.store.Fail(wCtx, taskID, c.nodeID, msg); err != nil {
					if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
						return roundOutcome{}, ErrCancelled
					}
					return roundOutcome{}, fmt.Errorf("fail: %w", err)
				}
				if err := c.store.Review(wCtx, taskID, c.nodeID, msg); err != nil {
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
				if err := c.store.Fail(wCtx, taskID, c.nodeID, res.Stderr); err != nil {
					if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
						return roundOutcome{}, ErrCancelled
					}
					return roundOutcome{}, fmt.Errorf("fail: %w", err)
				}
				if err := c.store.ReviewWithDisposition(wCtx, taskID, c.nodeID, res.Stderr, ApprovalResumeExecution); err != nil {
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
			if err := c.store.Fail(wCtx, taskID, c.nodeID, res.Stderr); err != nil {
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
				AgentSessionID: sessionID,
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
		v, serr := entry.Supervise(execCtx, c.supervisor, currentIntent, judgeResult)
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
						// A different agent owns no handle to the old one's
						// conversation; leaving it persisted would resume the
						// wrong adapter's session on the next interruption.
						_ = c.store.SetAgentSession(context.WithoutCancel(execCtx), taskID, "", "")
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
			currentIntent = currentIntent + "\n\n上一轮未能完整完成，请继续完成剩余工作，并汇报最终结果。"
		} else {
			currentIntent = currentIntent + "\n\n[上级补充指令]\n" + v.Followup
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
	return roundOutcome{res: res, lastChanged: lastChanged, verdict: verdict, sessionID: sessionID}, nil
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
	lastChanged []string, verdict entry.SuperviseVerdict, sessionID string) (bus.TaskResultPayload, error) {
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
		wCtx, cancel := c.storeWriteCtx(ctx)
		defer cancel()
		if err := c.store.PauseWithResult(wCtx, taskID, c.nodeID, map[string]any{
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
			Tokens: res.Tokens, Cost: res.Cost, OutputArtifact: outputArtifact, FilesChanged: filesChanged, Agent: res.Agent, Model: res.Model, Injected: res.Injected,
			AgentSessionID: sessionID,
		}, nil
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
			if err := c.store.PauseWithResult(wCtx, taskID, c.nodeID, map[string]any{
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
				Tokens: res.Tokens, Cost: res.Cost, OutputArtifact: outputArtifact, FilesChanged: filesChanged, Agent: res.Agent, Model: res.Model, Injected: res.Injected,
				AgentSessionID: sessionID,
			}, nil
		}
		// Consent already on record: audit the auto-acceptance the way an
		// authorized native Tier-2 run is audited, then fall through to
		// Complete.
		c.audit(context.WithoutCancel(ctx), taskID, "agent:tier2", plan.Agent, "authorized", "completed under submit-time consent")
	}

	wCtx, cancel := c.storeWriteCtx(ctx)
	defer cancel()
	if err := c.store.Complete(wCtx, taskID, c.nodeID, map[string]any{
		"ok": true, "exit_code": res.ExitCode, "stdout": res.Stdout, "files_changed": filesChanged,
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
		Tokens: res.Tokens, Cost: res.Cost, OutputArtifact: outputArtifact, FilesChanged: filesChanged, Agent: res.Agent, Model: res.Model, Injected: res.Injected,
		// Reported even though the local row cleared it: the delegator keeps
		// the handle so a re-dispatch back here can offer ResumeSessionID.
		AgentSessionID: sessionID,
	}, nil
}

// maxFilesChangedReport caps the file list a result carries on the wire: the
// footprint's job is showing what the task touched, and the first few hundred
// paths show that fine — a node_modules-scale rewrite would only drown it.
const maxFilesChangedReport = 200

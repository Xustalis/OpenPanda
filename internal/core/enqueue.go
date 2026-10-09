// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
	"github.com/Xustalis/OpenPanda/internal/scheduler/queue"
)

// QueueSpec carries the queue-scheduling metadata for an Enqueued task.
// Priority must be one of PriorityHigh/Normal/Low; use DefaultQueueSpec for
// the normal-priority defaults so a zero struct is never submitted by
// accident (PriorityHigh is 0).
type QueueSpec struct {
	Priority     int
	SessionID    string
	WorkDir      string
	ResourceKeys []string
}

// DefaultQueueSpec returns a spec with normal priority and no session/
// resource bindings.
func DefaultQueueSpec() QueueSpec {
	return QueueSpec{Priority: PriorityNormal}
}

// Enqueue creates a task and hands it to the local queue scheduler instead of
// executing it inline: the task lands in queued state (marked scheduled) and
// returns immediately; the scheduler starts it when its resources are free
// and an execution slot is available. It is the panel's task-submission path
// (queue redesign), contrasting Submit/SubmitLocal which block until done.
func (c *Core) Enqueue(ctx context.Context, in TaskInput, q QueueSpec) (Task, error) {
	if q.Priority < PriorityHigh || q.Priority > PriorityLow {
		return Task{}, fmt.Errorf("queue priority %d out of range", q.Priority)
	}
	if len(q.ResourceKeys) == 0 {
		q.ResourceKeys = deriveResourceKeys(in)
	} else {
		q.ResourceKeys = mergeActuatorKeys(q.ResourceKeys, in.Requires, in.SpecJSON)
	}
	t, _, _, err := c.createTask(ctx, in)
	if err != nil {
		return Task{}, fmt.Errorf("create task: %w", err)
	}
	if err := c.store.SetQueueMeta(ctx, t.TaskID, q.Priority, q.SessionID, q.WorkDir, q.ResourceKeys); err != nil {
		return Task{}, fmt.Errorf("set queue meta: %w", err)
	}
	if err := c.store.Queue(ctx, t.TaskID, c.nodeID); err != nil {
		return Task{}, fmt.Errorf("queue task: %w", err)
	}
	t.Priority = q.Priority
	t.SessionID = q.SessionID
	t.WorkDir = q.WorkDir
	t.ResourceKeys = q.ResourceKeys
	t.Scheduled = true
	t.State = StateQueued
	c.queueWake()
	c.logger.Info("task enqueued", "task", t.TaskID, "priority", q.Priority)
	return t, nil
}

// deriveResourceKeys computes the queue resource locks a task holds from the
// shape of its input: a project task serializes on the project (a shared
// worktree admits one writer), a preferred-node pin serializes on that node,
// and agent:/node: requires map to their resource ids. Physical actuators
// merge on top via mergeActuatorKeys — the spec's stated target serializes
// even when requires reached the device through a vaguer token.
func deriveResourceKeys(in TaskInput) []string {
	var keys []string
	if in.Project != "" {
		keys = append(keys, "project:"+in.Project)
	} else {
		if in.TargetNode != "" {
			keys = append(keys, "node:"+in.TargetNode)
		} else if in.PreferredNode != "" {
			keys = append(keys, "node:"+in.PreferredNode)
		}
		for _, req := range in.Requires {
			if strings.HasPrefix(req, "agent:") || strings.HasPrefix(req, "node:") {
				keys = append(keys, req)
			}
		}
	}
	return mergeActuatorKeys(keys, in.Requires, in.SpecJSON)
}

// mergeActuatorKeys adds the physical-device locks a task must hold: every
// "hardware:*" requires token, plus the action_spec's target actuator. Two
// driver processes on one GPIO pin is a fault, not parallelism, so the key
// merges on top of whatever else the task already holds — caller-named keys
// cannot waive it, only ordering around it changes.
func mergeActuatorKeys(keys []string, requires []string, specJSON string) []string {
	var hw []string
	for _, req := range requires {
		if strings.HasPrefix(req, "hardware:") {
			hw = append(hw, req)
		}
	}
	if spec, err := commander.ParseActionSpec(specJSON); err == nil && spec != nil {
		hw = append(hw, spec.TargetActuator)
	}
	if len(hw) == 0 {
		return keys
	}
	have := make(map[string]bool, len(keys))
	for _, k := range keys {
		have[k] = true
	}
	for _, k := range hw {
		if k != "" && !have[k] {
			have[k] = true
			keys = append(keys, k)
		}
	}
	return keys
}

// preferredNodeOf recovers the SOFT routing hint a task carries — spec's
// "preferred" key. A miss simply means no preference, never an error worth
// surfacing.
func preferredNodeOf(t Task) string {
	if t.SpecJSON == "" {
		return ""
	}
	var spec struct {
		Preferred string `json:"preferred"`
	}
	if err := json.Unmarshal([]byte(t.SpecJSON), &spec); err != nil {
		return ""
	}
	return spec.Preferred
}

// targetNodeOf recovers the HARD pin a task carries — spec's "node" key.
// The pin names the only acceptable destination: any caller considering a
// re-route, re-dispatch, or decline-propagation must check this first.
// A miss means no pin and normal scored routing applies.
func targetNodeOf(t Task) string {
	if t.SpecJSON == "" {
		return ""
	}
	var spec struct {
		Node string `json:"node"`
	}
	if err := json.Unmarshal([]byte(t.SpecJSON), &spec); err != nil {
		return ""
	}
	return spec.Node
}

// queueWake nudges the queue scheduler if one is running.
func (c *Core) queueWake() {
	c.mu.RLock()
	s := c.queueSched
	c.mu.RUnlock()
	if s != nil {
		s.Wake()
	}
}

// StartQueueScheduler starts the node-local queue scheduler loop: it adopts
// queued-and-scheduled tasks in policy order (drag seq → priority → FIFO),
// enforcing resource conflicts and the card's MaxConcurrent budget. Idempotent
// within one Core; runs until ctx ends.
func (c *Core) StartQueueScheduler(ctx context.Context) *queue.Scheduler {
	max := c.Card().Capacity.MaxConcurrent
	if max < 1 {
		max = 1
	}
	s := queue.New(queueStoreAdapter{c: c}, queueRunnerAdapter{c: c}, max, c.logger)
	c.mu.Lock()
	c.queueSched = s
	c.mu.Unlock()
	go s.Run(ctx)
	// A previous process instance may have left claimed/queued tasks behind;
	// nudge once so adoption happens without waiting for the next event.
	s.Wake()
	return s
}

// enrichDeclineReason attaches the recorded refusal trail to a route decline,
// so "no capability matches" stops masquerading as the verdict when capable
// nodes already refused — "declined by B (capacity full)" is the actionable
// truth the operator needs. No trail means a genuine capability miss: the
// base reason stands.
func (c *Core) enrichDeclineReason(ctx context.Context, taskID, base string) string {
	if c.store == nil {
		return base
	}
	trail, err := c.store.DeclineTrail(ctx, taskID)
	if err != nil || len(trail) == 0 {
		return base
	}
	parts := make([]string, 0, len(trail))
	for _, d := range trail {
		if d.Reason != "" {
			parts = append(parts, d.By+" ("+d.Reason+")")
		} else {
			parts = append(parts, d.By)
		}
	}
	return base + "; declined by " + strings.Join(parts, "; ")
}

// queueStoreAdapter implements queue.Store on the core's task store.
type queueStoreAdapter struct{ c *Core }

func (a queueStoreAdapter) ListReady(ctx context.Context) ([]queue.ReadyTask, error) {
	if a.c.node.draining(ctx) {
		// Drain is a full pause on new claims: queued rows stay queued (they
		// release on --off), while tasks already claimed finish their runs.
		return nil, nil
	}
	return a.c.store.ListReadySummaries(ctx)
}

func (a queueStoreAdapter) CountActive(ctx context.Context) (int, error) {
	return a.c.store.CountScheduledActive(ctx, a.c.nodeID)
}

func (a queueStoreAdapter) Claim(ctx context.Context, taskID string) error {
	return a.c.store.ClaimLocal(ctx, taskID, a.c.nodeID)
}

// queueRunnerAdapter implements queue.Runner: run one claimed task to a
// terminal state.
type queueRunnerAdapter struct{ c *Core }

func (a queueRunnerAdapter) Run(ctx context.Context, taskID string) {
	a.c.runScheduled(ctx, taskID)
}

// runScheduled executes a task the queue scheduler claimed (queued ->
// dispatched already done by ClaimLocal): accept it into running and drive
// the shared retry/review loop. Intent/requires come from the persisted
// detail so the scheduler needs no out-of-band state.
func (c *Core) runScheduled(ctx context.Context, taskID string) {
	t, err := c.store.Get(ctx, taskID)
	if err != nil {
		c.logger.Warn("queue: load claimed task", "task", taskID, "err", err)
		return
	}
	// Cross-device routing: when this node cannot serve the task's required
	// abilities, hand it to a capable peer — the same root-scheduler policy
	// Submit applies (design §2.4). The claim is re-targeted to the peer and
	// its task_result completes the local row via handleResult, so there is
	// nothing left to run here. Queued tasks are first-class network
	// citizens, not local-only work.
	if c.forwardScheduled(ctx, t) {
		return
	}
	result, err := c.run(ctx, taskID, t.Intent, t.Requires, nil)
	final, _, rerr := c.retryLoop(ctx, taskID, t.Intent, t.Requires, result, err)
	if rerr != nil && !errors.Is(rerr, ErrCancelled) {
		c.logger.Warn("queue: task run error", "task", taskID, "err", rerr)
		return
	}
	c.logger.Info("queue: task finished", "task", taskID, "state", final.State)
	// A finished stage may have released its successors. The plan node is the one
	// that decides, and for a locally-executed stage that is this node.
	c.advanceStagePlan(ctx, final)
	// Unblock any synchronous waiter (delegation-style waits on enqueued
	// tasks); a no-op when nobody is waiting.
	c.signalResult(taskID, result)
}

// forwardScheduled routes a claimed queue task to the node that should run it.
// It returns true when the task was handed off (the result arrives
// asynchronously via handleResult); false means the caller should proceed with
// local execution — either this node won the routing decision, or nobody did
// (the local run then fails with the standard capability error).
//
// The decision is scheduler.Route's, not this function's. It used to be "if I
// can do it, I do it", which is the short-circuit RouteAt's doc comment says was
// removed in v0.0.6 because it makes load balancing impossible by construction —
// but it was removed from Route only, and this is the path every plan stage and
// every panel-submitted task takes. Asking here meant a stage needing a GPU ran
// on the Orange Pi whenever the Pi happened to hold the ability, and a burst of
// queued tasks all stayed on the node that accepted them while idle peers
// watched. Both are the exact cases the hardware filter and the score exist for.
func (c *Core) forwardScheduled(ctx context.Context, t Task) bool {
	// A task with a work dir on this machine used to be local by definition:
	// the delegate payload carried no work dir, so a forwarded copy ran
	// blind. A file task's tree now travels as an artifact input
	// (attachWorktreeFrom below); a non-file task's work dir is just the
	// launch directory, which the executor replaces with its own. So the
	// workdir rule keeps only the unpinned non-file case home (the blind
	// copy still loses there), and a pinned file task whose tree cannot
	// ship fails honestly instead of falling back to local execution.
	pinRef := targetNodeOf(t)
	pinned := pinRef != ""
	// An unpinned task with a work dir stays home: forwarding it would run a
	// blind copy without the directory it names. A PINNED task forwards
	// anyway — the user named the destination, and for a non-file task the
	// work dir is the launch directory the executor replaces with its own,
	// not content that must travel (file tasks ship their tree below, and a
	// pin whose tree cannot ship fails there honestly).
	if t.WorkDir != "" && t.ContextType != "file" && !pinned {
		return false
	}
	chain := t.Chain
	if len(chain) == 0 {
		chain = []string{c.nodeID}
	}
	// Loop safety, same as rerouteDeclined (P1-5): exclude every node that
	// already declined this task so a re-queued task is not handed straight
	// back to its last decliner.
	excluded, err := c.store.DeclinedBy(ctx, t.TaskID)
	if err != nil {
		c.logger.Warn("queue forward: declined-by", "task", t.TaskID, "err", err)
	}
	seenChain := append(slices.Clone(chain), excluded...)
	// Routing: an unpinned task takes the scored path as before; a pinned
	// task bypasses scoring entirely — the pin names the only acceptable
	// destination and resolvePin decides where the task is allowed to land.
	var decision scheduler.Decision
	if pinned {
		decision = c.routePinned(ctx, pinRef, seenChain, t.Requires, resourceRequirement(t.ResourceJSON))
		switch decision.Action {
		case scheduler.ActionLocal:
			return false // the pin names this node: run here — a local work dir is fine
		case scheduler.ActionDecline:
			// Honest terminal failure — a pin that resolves nowhere (or to a
			// node that already declined, or to an ambiguous name) must not
			// degrade into "whoever scored best".
			c.failLocal(ctx, t.TaskID, fmt.Errorf("%s", decision.Reason))
			c.signalResult(t.TaskID, bus.TaskResultPayload{
				TaskID: t.TaskID, AttemptID: t.AttemptID,
				State: StateFailed, Stderr: decision.Reason,
			})
			return true
		}
	} else {
		decision = scheduler.RouteP(c.nodeID, seenChain, c.onlineEmployees(ctx), c.localMatch(), t.Requires,
			resourceRequirement(t.ResourceJSON), preferredNodeOf(t), t.Project)
	}
	if decision.Action != scheduler.ActionForward {
		c.logger.Info("queue: no peer for task", "task", t.TaskID,
			"action", string(decision.Action),
			"reason", c.enrichDeclineReason(ctx, t.TaskID, decision.Reason))
		return false
	}
	p := bus.TaskDelegatePayload{
		TaskID:       t.TaskID,
		ParentID:     t.ParentID,
		Project:      t.Project,
		Title:        t.Title,
		ContextType:  t.ContextType,
		ContextHash:  t.ContextHash,
		Intent:       t.Intent,
		SpecJSON:     t.SpecJSON,
		Requires:     t.Requires,
		Chain:        chain,
		Complexity:   t.Complexity,
		Risk:         t.Risk,
		ResourceJSON: t.ResourceJSON,
		AttemptID:    t.AttemptID,
		Authorized:   t.Authorized,
		// A stage travels with its plan identity and its inputs, so the executor
		// can pull the trees its predecessors produced. Empty on a plain task.
		PlanID:  t.PlanID,
		StageID: t.StageID,
		Inputs:  t.Inputs,
		// §6.1/§8.2: the queue path carries the same mesh contract as the
		// synchronous dispatch — depth, remaining budget, transport, deadline.
		Transport:        t.Transport,
		DeadlineUnix:     t.DeadlineUnix,
		Depth:            len(chain),
		DelegationBudget: &t.DelegationBudget,
		TokenBudget:      t.TokenBudget,
	}
	if pinned {
		// Stable identity on the wire: the executor's own pin re-check (and
		// any sub-scheduler hop in between) resolves by identity, immune to
		// name collisions and to the target's instance-id churn on restart.
		p.TargetNode = c.stablePeerID(ctx, decision.Target)
	}
	// Session resume hint (§5.2): the handle only means something to the node
	// that minted it, so it is offered only when the route leads back there.
	if t.AgentSessionID != "" && t.AgentSessionNode == decision.Target {
		p.ResumeSessionID = t.AgentSessionID
	}
	// Same project carriage as the synchronous path: a queued task delegated to a
	// peer must arrive with its project context or the peer cannot use it.
	c.attachProject(ctx, &p, t.Project)
	// The ad-hoc sibling of the same rule: a queued file task ships its work
	// tree or it does not ship at all — a forwarded blind copy loses to just
	// running it here.
	c.attachWorktreeFrom(ctx, &p, t)
	if t.ContextType == "file" && t.WorkDir != "" && len(p.Inputs) == 0 {
		if pinned {
			// The pinned destination cannot receive the tree it needs —
			// running the blind copy here instead would be the silent
			// substitution the pin forbids. Fail with the reason.
			reason := fmt.Sprintf("pinned node %q cannot receive the task's work tree", pinRef)
			c.failLocal(ctx, t.TaskID, fmt.Errorf("%s", reason))
			c.signalResult(t.TaskID, bus.TaskResultPayload{
				TaskID: t.TaskID, AttemptID: t.AttemptID,
				State: StateFailed, Stderr: reason,
			})
			return true
		}
		return false
	}
	// Hop-limited consent (S2-8): a queue forward is one direct dispatch, so
	// the consent covers exactly the receiving hop and must not walk further
	// through a forwarding sub-scheduler. The grant is minted here — after
	// the project/tree attaches above — because its digest binds the final
	// payload; a relay instead re-emits the origin's stored grant verbatim
	// (P2-8).
	if t.Authorized {
		p.AuthHops = 1
		c.grantConsentFor(t, &p)
	}
	if err := c.sendClaimedDelegate(ctx, t.TaskID, decision.Target, p); err != nil {
		c.logger.Warn("queue: forward to peer failed", "task", t.TaskID,
			"target", decision.Target, "err", err)
		if pinned {
			// A pinned forward that fails before the send path (budget spent,
			// retarget conflict, envelope error) is an honest terminal failure
			// — returning false would run the task here, the silent
			// local-fallback the pin forbids.
			c.failLocal(ctx, t.TaskID, fmt.Errorf("pinned forward to %s failed: %w", decision.Target, err))
			c.signalResult(t.TaskID, bus.TaskResultPayload{
				TaskID: t.TaskID, AttemptID: t.AttemptID,
				State:  StateFailed,
				Stderr: fmt.Sprintf("pinned forward to %s failed: %s", decision.Target, err),
			})
			return true
		}
		return false
	}
	c.logger.Info("queue: task forwarded to peer", "task", t.TaskID, "target", decision.Target)
	return true
}

// sendClaimedDelegate re-targets an already-claimed (dispatched-to-self) task
// to target and sends the delegate envelope. Unlike dispatchDelegated it does
// not transition state — the queue claim already moved the task to dispatched —
// it records the new delegation target on the audit trail (so isCurrentExecutor
// authenticates the peer's result/decline) and stamps a lease so a dead
// executor is detected (D3).
func (c *Core) sendClaimedDelegate(ctx context.Context, taskID, target string, p bus.TaskDelegatePayload) error {
	// §6.1: a queue forward spends the same budget a synchronous dispatch does
	// — the paths are interchangeable, so the bound must be too.
	remaining, err := c.delegationBudget(ctx, taskID, derefBudget(p.DelegationBudget))
	if err != nil {
		return err
	}
	p.DelegationBudget = &remaining
	tokens, err := c.tokenBudgetWire(ctx, taskID, p.TokenBudget)
	if err != nil {
		return err
	}
	p.TokenBudget = tokens
	if err := c.store.RetargetDelegation(ctx, taskID, target); err != nil {
		return fmt.Errorf("retarget: %w", err)
	}
	dtn := p.Transport == "dtn"
	// A hard-pinned task shares DTN's delivery contract: the named peer is the
	// only acceptable destination, so a dead link means park-and-wait, never
	// the unpinned fall-back-to-local corrective path.
	pinned := p.TargetNode != ""
	parkable := dtn || pinned
	pushDeadline := p.DeadlineUnix
	if pushDeadline <= 0 {
		pushDeadline = time.Now().Add(defaultDTNTTL).Unix()
	}
	park := parkable && !c.sendableTo(target)
	if !park && !dtn {
		// A DTN task is lease-exempt (§8.2): the absolute deadline is its bound.
		timeoutMS := p.TimeoutMS
		if timeoutMS <= 0 {
			timeoutMS = c.lease().Milliseconds()
			p.TimeoutMS = timeoutMS
		}
		if err := c.store.SetLease(ctx, taskID, timeoutMS); err != nil {
			return fmt.Errorf("set lease: %w", err)
		}
	} else if dtn {
		// Same fat-push split as dispatchDelegated: small artifacts ride the
		// envelope, larger ones become durable push custody for the flush.
		for _, h := range c.attachFatBundle(ctx, &p) {
			c.artifactPushEnqueue(ctx, target, taskID, h, pushDeadline)
		}
	}
	if !park {
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
			if parkable {
				// §8.2 store-and-forward, and the pinned variant: an
				// undeliverable task parks in the outbox for the peer's next
				// reconnect instead of falling back to local execution — the
				// retarget pointing at the peer stays true: the outbox flush
				// IS this node holding the task for that peer.
				kind := "dtn"
				if pinned && !dtn {
					kind = "pin"
				}
				if err := c.store.ClearLease(ctx, taskID); err != nil {
					c.logger.Warn("queue: clear lease for parked task", "task", taskID, "err", err)
				}
				c.taskOutboxPersist(ctx, target, p, kind, pushDeadline)
				c.logger.Info("queue: task parked in task_outbox awaiting peer link",
					"task", taskID, "target", target, "kind", kind, "err", err)
				// The parked bundle owns this hop — the budget is spent even
				// though no frame left the socket.
				if err := c.store.SetDelegationBudget(ctx, taskID, remaining); err != nil {
					c.logger.Warn("persist delegation budget", "task", taskID, "err", err)
				}
				return nil
			}
			// The send failed, so the peer never received the task — but the audit
			// trail already records IT as the delegation target. Leaving that in
			// place makes DispatchTarget authenticate a non-executor and makes the
			// task look remotely-owned to the orphan sweep, when in fact this node
			// still holds it (dispatched-to-self, lease ours). Write a corrective
			// delegate event pointing back at ourselves so the last EvDelegate
			// reflects the actual executor; the task stays queued for the next
			// scheduling pass instead of being orphaned on paper.
			if rerr := c.store.RetargetDelegation(ctx, taskID, c.nodeID); rerr != nil {
				c.logger.Warn("queue: corrective retarget failed", "task", taskID, "err", rerr)
			}
			return fmt.Errorf("send: %w", err)
		}
	} else {
		// No link at claim time: park directly without a doomed send attempt.
		// The audit trail already names the peer as delegation target, so the
		// result the flush eventually earns authenticates normally.
		kind := "dtn"
		if pinned && !dtn {
			kind = "pin"
		}
		// A parked task holds no executor, so no lease may expire on it:
		// whatever a previous attempt stamped would otherwise fire mid-wait
		// and fail a task that is legitimately holding for its peer.
		if err := c.store.ClearLease(ctx, taskID); err != nil {
			c.logger.Warn("queue: clear lease for parked task", "task", taskID, "err", err)
		}
		c.taskOutboxPersist(ctx, target, p, kind, pushDeadline)
		c.logger.Info("queue: task parked in task_outbox awaiting peer link",
			"task", taskID, "target", target, "kind", kind)
	}
	// The hop landed on the wire (or is durably parked for it) — now the
	// budget is spent. Persisting only here keeps a failed retarget or dead
	// socket from burning a delegation the mesh never took.
	if err := c.store.SetDelegationBudget(ctx, taskID, remaining); err != nil {
		c.logger.Warn("persist delegation budget", "task", taskID, "err", err)
	}
	if dtn || park {
		// Drain deferred push custody into the contact window that just
		// carried the delegate — or, for a parked bundle, prime the flush so
		// the next peer hello has nothing left to prepare.
		go c.outboxFlush(context.WithoutCancel(ctx), target)
	}
	// — Trace: queue re-route hop (from=here, to=target), same shape as
	// dispatchDelegated's so the orbit treats both alike.
	c.EvTrace(ctx, taskID, EvDelegationHop, map[string]any{
		"from_node":  c.nodeID,
		"to_node":    target,
		"via":        "retarget",
		"chain":      p.Chain,
		"attempt_id": p.AttemptID,
	})
	return nil
}

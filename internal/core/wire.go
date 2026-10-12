// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
	"github.com/Xustalis/OpenPanda/internal/storage"
	"github.com/Xustalis/OpenPanda/internal/util"
)

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
	// Trust zones: a signed accept is the executor's bilateral commitment —
	// verify it before letting it move our copy or ride further up the
	// chain. An unsigned frame is a legacy peer and passes as before; a
	// malformed or non-verifying signed frame is dropped.
	if err := c.verifyTaskAccept(ctx, env.From, t, p); err != nil {
		c.logger.Warn("task_accept signature rejected", "task", p.TaskID,
			"from", env.From, "err", err)
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
		if attemptIsNewer(p.AttemptID, t.AttemptID) {
			// The executor rotated its attempt forward (a resume re-run or a
			// supervisor retry re-attempts under a fresh id upstream never
			// sees). UUIDv7 ids are time-ordered, so a newer id can only come
			// from a rotation after our record — adopt it and let the beat
			// renew the lease: the executor is alive on its current attempt.
			if aerr := c.store.AdoptAttempt(ctx, p.TaskID, p.AttemptID); aerr == nil {
				t.AttemptID = p.AttemptID
			}
		} else {
			// A beat from a superseded attempt (after a retry/transfer) must not
			// extend the current one's lease.
			return
		}
	}
	if err := c.store.SetLease(ctx, p.TaskID, c.lease().Milliseconds()); err != nil {
		c.logger.Warn("refresh lease from progress", "task", p.TaskID, "err", err)
	}
	// A beat carrying a note is the executor's progress sink reaching the
	// delegator: record it so `task logs`/`task show` here shows the same
	// life the executor's own timeline does. The executor already throttles
	// and dedupes notes before sending, so one row lands per reported beat.
	if p.Note != "" {
		if err := c.store.RecordEvent(ctx, p.TaskID, EvProgress, map[string]any{
			"note": p.Note, "from": env.From,
		}); err != nil {
			c.logger.Warn("record remote progress", "task", p.TaskID, "err", err)
		}
	}
	c.relayToParent(ctx, bus.MsgTaskProgress, t.Chain, p)
}

// attemptIsNewer reports whether candidate is a strictly later attempt than
// stored: both must be UUIDv7 (the only id shape whose lexical order is time
// order), and candidate must sort after stored. Anything else — an arbitrary
// foreign id, a missing version nibble — cannot be ordered and stays on the
// drop path it always took.
func attemptIsNewer(candidate, stored string) bool {
	return util.IsUUIDv7(candidate) && util.IsUUIDv7(stored) && candidate > stored
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
	if pin := targetNodeOf(t); pin != "" {
		// The task names its destination: a decline from that destination is
		// a terminal answer, not an invitation to shop for a substitute.
		// Record who refused, fail honestly, and let the verdict reach the
		// waiting submitter below.
		if err := c.store.RecordEvent(ctx, p.TaskID, EvDecline,
			map[string]any{"reason": p.Reason, "by": env.From}); err != nil {
			c.logger.Debug("decline event record failed", "task", p.TaskID, "err", err)
		}
		c.failLocal(ctx, p.TaskID,
			fmt.Errorf("pinned node %s declined: %s", env.From, p.Reason))
		c.relayToParent(ctx, bus.MsgTaskDecline, t.Chain, p)
		c.signalResult(p.TaskID, bus.TaskResultPayload{
			TaskID: p.TaskID, State: StateFailed, OK: false, ExitCode: 1,
			Stderr: fmt.Sprintf("pinned node %s declined: %s", env.From, p.Reason),
		})
		return
	}
	// Parent returns the task to queued for re-routing elsewhere. The wire's
	// machine reason code (DeclineCodeTrust* etc.) rides into the audit trail
	// alongside the text so policy refusals stay distinguishable from
	// capacity ones.
	if err := c.store.Decline(ctx, p.TaskID, c.nodeID, p.Reason, p.ReasonCode, env.From); err != nil {
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
	if targetNodeOf(t) != "" {
		// A pinned task has exactly one acceptable destination; when it
		// declines, the task must surface that refusal — rerouting would be
		// the silent-substitution failure the pin exists to prevent.
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

	decision := scheduler.RouteP(c.nodeID, seenChain,
		c.eligibleForSend(ctx, needsForTask(t), c.onlineEmployees(ctx)),
		c.localMatch(), t.Requires,
		resourceRequirement(t.ResourceJSON), "", t.Project)
	if decision.Action != scheduler.ActionForward {
		c.logger.Info("reroute: no alternate node", "task", taskID, "action", decision.Action,
			"reason", c.enrichDeclineReason(ctx, taskID, decision.Reason))
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
		// A stage re-routed after a decline must keep its plan identity and its
		// inputs: without them the executor cannot fetch the trees its
		// predecessors produced, and its output artifact is never adopted back —
		// the plan stalls on a stage that ran to completion as an orphan.
		PlanID:  t.PlanID,
		StageID: t.StageID,
		Inputs:  t.Inputs,
	}
	// Session resume hint (§5.2): only the node that minted the handle can
	// use it, so it rides the wire only when the route leads back there.
	if t.AgentSessionID != "" && t.AgentSessionNode == decision.Target {
		payload.ResumeSessionID = t.AgentSessionID
	}
	// A re-routed project task needs its context as much as the first attempt did.
	c.attachProject(ctx, &payload, t.Project)
	// Same for an ad-hoc file task: the tree travels with the re-route or the
	// task stays — a blind copy on a fresh peer is the failure mode the
	// attach exists to prevent.
	c.attachWorktreeFrom(ctx, &payload, t)
	if t.ContextType == "file" && t.WorkDir != "" && len(payload.Inputs) == 0 {
		return false
	}
	if t.ContextHash != "" {
		payload.ContextLevel = "pointer"
	}
	// Hop-limited consent (S2-8): a re-route is one direct dispatch, so the
	// consent covers exactly the receiving hop and must not walk further
	// through a forwarding sub-scheduler. Signed after the attaches — the
	// grant's digest binds the final payload — and re-emitted verbatim when
	// this node is only relaying another origin's task (P2-8).
	if t.Authorized {
		payload.AuthHops = 1
		c.grantConsentFor(t, &payload)
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
		// creating one (Phase 0 entry flow), or lost it across a restart.
		// Reconstruct a minimal row, adopting the executor's attempt so the
		// result is not flagged stale.
		//
		// The chain comes from the result payload itself (S1-3): the executor
		// echoes the delegation chain it ran under, so the relayed result
		// walks the real upstream path instead of stopping at a guessed
		// [self, sender]. Old executors that do not echo it fall back to the
		// guess (this node is then treated as the root).
		if p.AttemptID == "" {
			c.logger.Warn("result with empty attempt rejected", "task", p.TaskID, "from", env.From)
			return
		}
		chain := p.Chain
		if len(chain) == 0 {
			chain = []string{c.nodeID, env.From}
		} else if chain[len(chain)-1] != env.From {
			// The chain the executor echoes must end at the authenticated
			// sender — the same rule handleDelegate enforces on dispatch.
			// Without it, the row being reconstructed here would adopt a
			// fabricated history (any upstream the sender names) instead of
			// the path the result actually travelled.
			c.logger.Warn("result chain does not end at sender", "task", p.TaskID,
				"from", env.From, "chain_tail", chain[len(chain)-1])
			return
		}
		if _, err := c.store.CreateFromRemote(ctx, p.TaskID, p.TaskID, c.nodeID, p.AttemptID, chain); err != nil {
			c.logger.Warn("create task from result", "task", p.TaskID, "err", err)
			return
		}
		// A row built from a bare result is not a delegation record: mark it
		// in the audit trail so the origin of this task's history stays
		// distinguishable from one the wire actually delivered.
		c.audit(ctx, p.TaskID, "task:reconstruct", env.From, "warn",
			"task row reconstructed from an inbound result; no local delegation record existed")
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
		if t.AttemptID != "" && attemptIsNewer(p.AttemptID, t.AttemptID) {
			// The executor rotated its attempt forward — a task_resume
			// re-run or supervisor retry mints a fresh attempt upstream never
			// hears about, and before this the result was dropped as stale,
			// leaving delegator and executor permanently split (running vs
			// review). The sender already authenticated as THIS task's
			// current executor above, and UUIDv7 ids are time-ordered: a
			// newer attempt can only come from a rotation after our record,
			// never from a replayed older one (those stay dropped below).
			if aerr := c.store.AdoptAttempt(ctx, p.TaskID, p.AttemptID); aerr != nil {
				c.logger.Warn("adopt rotated attempt", "task", p.TaskID, "err", aerr)
			} else {
				c.logger.Info("adopted executor's rotated attempt", "task", p.TaskID,
					"stored", t.AttemptID, "got", p.AttemptID)
				t.AttemptID = p.AttemptID
			}
		} else {
			c.logger.Info("stale attempt result ignored", "task", p.TaskID,
				"stored", t.AttemptID, "got", p.AttemptID)
			// Keep the drop honest: a real executor DID produce this outcome for
			// an attempt the row moved past. Without the audit event the timeline
			// reads as if the remote work never reported back.
			if rerr := c.store.RecordEvent(ctx, p.TaskID, EvResult, map[string]any{
				"late": true, "dropped": "stale_attempt",
				"from": env.From, "remote_state": p.State, "remote_attempt": p.AttemptID,
			}); rerr != nil {
				c.logger.Debug("late result audit failed", "task", p.TaskID, "err", rerr)
			}
			return
		}
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
	if Terminal(t.State) {
		// An expired row is a timeout's pessimistic close — the deadline
		// bounded delivery, not correctness — so a real done still
		// reconciles below. Cancelled and done stay sealed: one is a human
		// decision, the other is the answer already.
		if !(t.State == StateExpired && state == StateDone && p.OK) {
			// A closed row stays closed — cancel/expire/done are deliberate
			// outcomes a late wire message must not reopen. But the executor's
			// real verdict is recorded so `task show` shows what actually
			// happened remotely instead of a silent divergence.
			c.logger.Info("late result on terminal task", "task", p.TaskID,
				"state", t.State, "from", env.From, "remote_state", p.State)
			if rerr := c.store.RecordEvent(ctx, p.TaskID, EvResult, map[string]any{
				"late": true, "dropped": "terminal_state", "local_state": t.State,
				"from": env.From, "remote_state": p.State, "remote_attempt": p.AttemptID,
			}); rerr != nil {
				c.logger.Debug("late result audit failed", "task", p.TaskID, "err", rerr)
			}
			c.signalResult(p.TaskID, p)
			return
		}
	}
	transitionOK := true
	switch state {
	case StateDone:
		if !p.OK {
			c.logger.Warn("inconsistent task_result state", "task", p.TaskID, "state", state, "ok", p.OK)
			return
		}
		if t.State == StateFailed || t.State == StateExpired {
			// Reconcile: a timeout path already closed this row, but the
			// executor's done is evidence the work actually finished. The
			// one failure a result may never overturn is a human reject —
			// its last event carries {rejected}, everything else (lease
			// expiry, remote fail, deadline) wrote EvResult.
			if c.store.failureWasRejected(ctx, p.TaskID) {
				c.logger.Info("late done on human-rejected task ignored", "task", p.TaskID, "from", env.From)
				if rerr := c.store.RecordEvent(ctx, p.TaskID, EvResult, map[string]any{
					"late": true, "dropped": "human_rejected",
					"from": env.From, "remote_state": p.State, "remote_attempt": p.AttemptID,
				}); rerr != nil {
					c.logger.Debug("late result audit failed", "task", p.TaskID, "err", rerr)
				}
				transitionOK = false
			} else if rerr := c.store.ReconcileDone(ctx, p.TaskID, c.nodeID, p, t.State); rerr != nil {
				if !errors.Is(rerr, ErrConflict) {
					c.logger.Warn("reconcile late done", "task", p.TaskID, "err", rerr)
					transitionOK = false
				}
			} else {
				c.logger.Info("late done reconciled over timeout close", "task", p.TaskID,
					"was", t.State, "from", env.From)
			}
		} else if err := c.store.CompleteFromRemote(ctx, p.TaskID, c.nodeID, p); err != nil {
			c.logger.Warn("complete from result", "task", p.TaskID, "err", err)
			transitionOK = false
		}
	case StateReview:
		if err := c.store.ReviewFromRemote(ctx, p.TaskID, c.nodeID, p, parseApprovalDisposition(p.ApprovalDisposition)); err != nil {
			c.logger.Warn("review from result", "task", p.TaskID, "err", err)
			transitionOK = false
		}
	case StateFailed:
		if err := c.store.FailFromRemote(ctx, p.TaskID, c.nodeID, p.Stderr); err != nil {
			c.logger.Warn("fail from result", "task", p.TaskID, "err", err)
			transitionOK = false
		}
	case StateCancelled:
		if err := c.store.Cancel(ctx, p.TaskID); err != nil && !errors.Is(err, ErrConflict) {
			c.logger.Warn("cancel from result", "task", p.TaskID, "err", err)
			transitionOK = false
		}
	case StateExpired:
		// An executor copy that expired reports it so both sides land on the
		// same verdict instead of one side waiting on a dead run.
		reason := p.Stderr
		if reason == "" {
			reason = "executor task expired"
		}
		if err := c.store.FailFromRemote(ctx, p.TaskID, c.nodeID, reason); err != nil {
			c.logger.Warn("expire from result", "task", p.TaskID, "err", err)
			transitionOK = false
		}
	case StateRunning, StateQueued, StateDispatched, StateWaitingCtx, StateSubmitted:
		// Informational: the executor's copy is in flight — most commonly a
		// task_resume whose consent raced the task's own progress (the human
		// approved on both copies). No transition: this node keeps waiting
		// (its lease is renewed by the executor's beats), and the eventual
		// terminal result converges both copies. Recording it keeps the
		// timeline honest about why the wait continues.
		c.logger.Info("in-flight executor report", "task", p.TaskID, "state", state, "from", env.From)
		if rerr := c.store.RecordEvent(ctx, p.TaskID, EvProgress, map[string]any{
			"note": "executor reports task " + state, "from": env.From,
		}); rerr != nil {
			c.logger.Debug("record in-flight report", "task", p.TaskID, "err", rerr)
		}
		return
	default:
		c.logger.Warn("unknown task_result state ignored", "task", p.TaskID, "state", state)
		return
	}

	// Record the executor's adapter session against our copy of the task
	// (§5.2): the handle belongs to the reporting node's adapter store, which
	// is why it is keyed by env.From — a later re-dispatch offers it back as
	// ResumeSessionID only when the target is that same node.
	if p.AgentSessionID != "" {
		if err := c.store.SetAgentSession(ctx, p.TaskID, p.AgentSessionID, env.From); err != nil {
			c.logger.Warn("record agent session", "task", p.TaskID, "err", err)
		}
	}

	// Plan plane: only a stage that produced output (done, or parked in review)
	// donates its artifact — the successors' input — and only after it was
	// adopted into this node's pool does the graph decide what became ready.
	// It is recorded for a review result too: the tree exists either way, and
	// the hash would otherwise be lost by the time a human approves the stage.
	// A failed or cancelled stage instead goes straight to AdvancePlan, which
	// fails every dependent waiting on it. A failed transition means the row is
	// terminal or contested (e.g. the stage was cancelled while the result was
	// in flight): advancing then would emit a spurious stage event on a plan
	// that already moved on.
	if transitionOK && t.PlanID != "" {
		if state == StateDone || state == StateReview {
			c.adoptStageOutput(ctx, t, env.From, p.OutputArtifact)
		} else {
			c.advanceStagePlan(ctx, t)
		}
	} else if transitionOK && t.Project != "" && p.OutputArtifact != "" &&
		(state == StateDone || state == StateReview) {
		c.adoptProjectOutput(ctx, t, env.From, p.OutputArtifact)
	} else if transitionOK && p.OutputArtifact != "" &&
		(state == StateDone || state == StateReview) {
		// The ad-hoc sibling: a file task that shipped its work tree gets the
		// produced tree back over the directory it packed from, so a remote
		// edit actually reaches the user's checkout.
		c.adoptWorktreeOutput(ctx, t, env.From, p.OutputArtifact)
	}

	// The executor reported WHERE it ran; record it on this row too, so the
	// delegator's queue board answers "where did it run?" for remote work
	// instead of showing a dash (the row's own exec_work_dir only exists
	// when THIS node ran the task).
	if p.WorkDir != "" && p.WorkDir != t.ExecWorkDir {
		if werr := c.store.SetExecWorkDir(context.WithoutCancel(ctx), p.TaskID, p.WorkDir); werr != nil {
			c.logger.Warn("record executor work dir", "task", p.TaskID, "err", werr)
		}
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
			t.Requires, state == StateDone, latencyMs, p.Tokens, p.Cost); err != nil {
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
		// A progress beat to an offline delegator is expected, not news: the
		// executor keeps working while the delegator is away, and a WARN per
		// beat (one every few seconds for a live task) is pure log noise.
		// Terminal frames keep the warning — those have custody semantics.
		if typ == bus.MsgTaskProgress {
			c.logger.Debug("relay progress to offline delegator", "to", parent, "err", err)
		} else {
			c.logger.Warn("relay", "type", typ, "to", parent, "err", err)
		}
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
	// The origin's CLI and web panel cancel through their own ephemeral core
	// (an ask-engine sibling of the daemon), so a cancel legitimately arrives
	// from an ephemeral id of the owner node. IsSelfRow recognizes that form;
	// anything else from another node is an unauthorized cross-node cancel and
	// is dropped.
	if env.From != t.OwnerNode && env.From != parent && !scheduler.IsSelfRow(t.OwnerNode, env.From) {
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
// cancelled mid-fetch does not leak in pendingCtx (P2-7) — or strand the
// capacity reservation the entry carries — and propagate the cancel to any
// remote executors holding dispatch leases (P2-3).
func (c *Core) finishCancel(ctx context.Context, cancelled []string) {
	for _, id := range cancelled {
		c.cancelRunning(id)
		c.dropPendingContext(id)
		c.forwardCancelDownstream(ctx, id, "cancelled by delegator")
	}
}

// CancelTree is the local-entry cancel: cascade locally, then notify
// downstream executors. The CLI and any in-process caller share the exact
// post-cancel behaviour of the wire handler. The cancelled id set is the
// store cascade's, so a caller can still report how many tasks it touched.
func (c *Core) CancelTree(ctx context.Context, taskID string) ([]string, error) {
	cancelled, err := c.store.CancelCascade(ctx, taskID)
	if err != nil {
		return nil, err
	}
	c.finishCancel(ctx, cancelled)
	return cancelled, nil
}

// RejectTree is the local-entry reject: fail the reviewed row, then notify
// the downstream executor holding the delegated review copy. Reject without
// the forward left the executor's copy parked in review — a local approve on
// that copy would run work the origin explicitly denied. The kill rides the
// same task_cancel/cancel_outbox path as CancelTree: the executor's
// handleCancel cascades it out of review, and an unreachable executor gets it
// parked for its next hello.
func (c *Core) RejectTree(ctx context.Context, taskID, reason string) error {
	if err := c.store.Reject(ctx, taskID, reason); err != nil {
		return err
	}
	c.forwardCancelDownstream(ctx, taskID, "rejected by delegator: "+reason)
	return nil
}

// forwardCancelDownstream propagates a cancel to the remote executor holding
// the dispatch lease, if any (P2-3). Without it, cancelling a delegated task
// cancelled only the delegator's local copy: the executor kept running to
// completion and its eventual result landed on an already-cancelled task.
// The receiver's handleCancel re-runs its own cascade-and-forward, so the
// cancel walks the whole downstream chain hop by hop.
func (c *Core) forwardCancelDownstream(ctx context.Context, taskID, reason string) {
	target, err := c.store.DispatchTarget(ctx, taskID)
	if err != nil {
		c.logger.Warn("cancel: dispatch target lookup", "task", taskID, "err", err)
		return
	}
	if target == "" || target == c.nodeID {
		return // never dispatched, or dispatched to ourselves
	}
	if reason == "" {
		reason = "cancelled by delegator"
	}
	if c.deliverCancel(ctx, target, taskID, reason) {
		// Delivered now: clear any copy parked from an earlier failed attempt.
		c.outboxCancelDrop(ctx, target, taskID)
		c.logger.Info("cancel forwarded downstream", "task", taskID, "to", target)
		return
	}
	// The executor is unreachable at the moment of the cancel. Park it for
	// redelivery on the next hello (S2-7) instead of relying on the lease
	// alone: the lease bounds liveness, but a live executor keeps renewing
	// it and would burn tokens on abandoned work until it finishes. On a
	// borrowed engine (a `panda cancel` CLI sharing the node id) the failed
	// send is structural — the owning daemon holds the edge — so the park
	// is the designed path, not a warning.
	if c.ownsNodeRow {
		c.logger.Warn("cancel: executor unreachable, parked for delivery on reconnect",
			"task", taskID, "to", target)
	} else {
		c.logger.Info("cancel parked for delivery via the owning engine",
			"task", taskID, "to", target)
	}
	c.outboxCancelPersist(ctx, target, taskID, reason)
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
	if p.Accept {
		c.handleAcceptWork(ctx, env, t)
		return
	}
	parent := scheduler.Predecessor(t.Chain, c.nodeID)
	if !scheduler.SameRuntimeIdentity(env.From, parent) {
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
		// The consent raced the task's own progress — most commonly the
		// human approved on BOTH copies and this one is already running.
		// Echo the row's REAL state instead of a synthesized failure: the
		// old "resume: task state is running" failure reply landed on the
		// delegator as a task_result and killed its copy (and cascaded its
		// plan) over a benign double approval. In-flight states are
		// informational (the delegator keeps waiting; the executor's
		// eventual result converges both copies); terminal states carry the
		// row's stored result so the delegator lands on the same verdict.
		c.logger.Info("resume raced task progress", "task", p.TaskID, "state", t.State)
		c.replyResult(ctx, env, c.resumeOutcomeFor(ctx, t))
		return
	}
	if t.ApprovalDisposition == ApprovalAdmissionReview {
		// An admission park gates on THIS node's operator — the delegator is
		// exactly the party the gate exists to check, so a wire task_resume
		// from it can never release the hold (a `panda approve` here can).
		// Echo the row's real state so the delegator's copy stays truthfully
		// parked instead of timing a refusal out.
		c.logger.Warn("resume on admission-parked task refused", "task", p.TaskID, "from", env.From)
		c.replyResult(ctx, env, c.resumeOutcomeFor(ctx, t))
		return
	}
	// Re-run asynchronously so the message loop stays responsive to
	// task_cancel while the (potentially long) agent run proceeds. The
	// outcome travels back over the normal task_result path. The re-run ctx is
	// detached from the handler's ctx for the same reason as the context-ack
	// resume path: that ctx dies with the requester's websocket read loop, so
	// inheriting it would kill the resumed agent the moment the requester's
	// link drops. Explicit cancels still land via the running map's CancelFunc.
	runCtx := context.WithoutCancel(ctx)
	c.goGuarded("task resume "+p.TaskID, func() {
		// relayUpstream=false: this goroutine answers the resumer on env —
		// relaying over the stored chain too would double-deliver (and to a
		// possibly-stale instance id if the delegator restarted mid-park).
		final, result, rerr := c.resumeApproved(runCtx, p.TaskID, false, p.Answer)
		if rerr != nil {
			result = bus.TaskResultPayload{
				TaskID: p.TaskID, AttemptID: t.AttemptID, State: StateFailed, OK: false, ExitCode: 1,
				Stderr: "resume: " + rerr.Error(),
				Chain:  t.Chain,
			}
		}
		// ResumeApproved can return without a wire-shaped outcome when a
		// local queue scheduler raced the just-approved task (its dispatch
		// lost the CAS): echo the row's real state so the delegator's wait
		// continues (in-flight) or converges (terminal) instead of dying on
		// a synthesized failure.
		switch result.State {
		case StateDone, StateReview, StateFailed, StateCancelled, StateExpired:
		default:
			result = c.resumeOutcomeFor(ctx, final)
		}
		c.replyResult(context.WithoutCancel(ctx), env, result)
	})
}

// resumeOutcomeFor builds the reply a resume gets when the consent raced the
// task's own progress on this node. The payload echoes the row's REAL state
// and stored result: an in-flight state (running/queued/dispatched/waiting)
// is informational — OK stays true so no delegator path renders it as a
// failure, and handleResult keeps the delegator's copy waiting for the
// eventual terminal result; a terminal state carries the row's verdict so
// both copies converge on the same outcome.
func (c *Core) resumeOutcomeFor(ctx context.Context, t Task) bus.TaskResultPayload {
	result := bus.TaskResultPayload{
		TaskID: t.TaskID, AttemptID: t.AttemptID, State: t.State, Chain: t.Chain,
	}
	if t.ResultJSON != "" {
		_ = json.Unmarshal([]byte(t.ResultJSON), &result)
		if result.Stderr == "" {
			// ForceFail-style rows store {"failed": reason} — carry it.
			var alt struct {
				Failed string `json:"failed"`
			}
			if json.Unmarshal([]byte(t.ResultJSON), &alt) == nil && alt.Failed != "" {
				result.Stderr = alt.Failed
			}
		}
		result.TaskID, result.AttemptID, result.State, result.Chain =
			t.TaskID, t.AttemptID, t.State, t.Chain
	}
	if t.State == StateReview && t.ApprovalDisposition != "" {
		// Without the typed disposition the receiver classifies the park as
		// accept_work (the conservative legacy default) and then protects it
		// from the eventual result — a resume_execution park (trust admission
		// hold, consent re-ask) would strand the upstream copy in review
		// forever after a mid-review reconnect re-push.
		result.ApprovalDisposition = string(t.ApprovalDisposition)
	}
	if result.Stderr == "" && (t.State == StateFailed || t.State == StateExpired) {
		// A locally-failed row keeps its reason on the audit trail (Fail
		// writes the EvResult event, not the result column); carry it so
		// the delegator converges on the real cause. A restart-recovery
		// failure records its cause on the EvRecover event instead — the
		// row's result_json is deliberately preserved there — and only a
		// recover that FAILED the task may answer; its requeue siblings
		// (to=queued) say nothing about a later failure.
		if evs, err := c.store.Events(ctx, t.TaskID); err == nil {
			for i := len(evs) - 1; i >= 0; i-- {
				if evs[i].Type != EvResult && evs[i].Type != EvRecover {
					continue
				}
				var d struct {
					Failed string `json:"failed"`
					Stderr string `json:"stderr"`
					To     string `json:"to"`
				}
				if json.Unmarshal([]byte(evs[i].DataJSON), &d) != nil {
					continue
				}
				if evs[i].Type == EvRecover && d.To != StateFailed {
					continue
				}
				if d.Failed != "" {
					result.Stderr = d.Failed
					break
				}
				if d.Stderr != "" {
					result.Stderr = d.Stderr
					break
				}
			}
		}
	}
	switch t.State {
	case StateDone:
		result.OK = true
	case StateFailed, StateCancelled, StateExpired:
		result.OK = false
		if result.Stderr == "" {
			result.Stderr = "task is " + t.State
		}
	default:
		// In flight: not a verdict, just the truth about where the task is.
		result.OK = true
		result.ExitCode = 0
		if result.Stderr == "" {
			result.Stderr = "resume: task already " + t.State + " (consent raced an earlier approval)"
		}
	}
	return result
}

// handleAcceptWork processes a task_resume carrying Accept: the human on the
// other copy of this task accepted work that already ran, and this parked copy
// mirrors the decision (review -> done). Accept is not consent to run
// anything — it only ever closes a finished copy — so it is refused for any
// disposition other than AcceptWork, and for rows that already moved on.
//
// Authorization: the sender must be this copy's chain predecessor (the
// delegator) or the dispatch target (the executor) — the two nodes that hold
// the sibling copies. A task that changed attempts since the decision is still
// closed: an accept mirrors a finished result, not consent scoped to a run,
// so the attempt guard a re-run needs does not apply.
func (c *Core) handleAcceptWork(ctx context.Context, env bus.Envelope, t Task) {
	parent := scheduler.Predecessor(t.Chain, c.nodeID)
	target, _ := c.store.DispatchTarget(ctx, t.TaskID)
	fromParent := scheduler.SameRuntimeIdentity(env.From, parent)
	fromTarget := target != "" && scheduler.SameRuntimeIdentity(env.From, target)
	if !fromParent && !fromTarget {
		c.logger.Warn("accept from unauthorized peer ignored", "task", t.TaskID,
			"from", env.From, "parent", parent, "target", target)
		return
	}
	if t.State != StateReview {
		// Honest reply instead of silence: the sender's mirror frame is
		// informational, but a node that raced another close (already done,
		// cancelled, expired) says so rather than looking unresponsive.
		c.logger.Info("accept for task not in review", "task", t.TaskID, "state", t.State)
		c.replyResult(ctx, env, bus.TaskResultPayload{
			TaskID: t.TaskID, AttemptID: t.AttemptID, State: t.State, OK: true, Chain: t.Chain,
		})
		return
	}
	disposition, err := c.store.ApprovalDisposition(ctx, t.TaskID)
	if err != nil || disposition != ApprovalAcceptWork {
		c.logger.Warn("accept for non-accept disposition ignored", "task", t.TaskID,
			"disposition", disposition, "err", err)
		return
	}
	if err := c.store.Approve(ctx, t.TaskID); err != nil {
		c.logger.Warn("accept approve failed", "task", t.TaskID, "err", err)
		return
	}
	final, err := c.store.Get(ctx, t.TaskID)
	if err != nil {
		c.logger.Warn("accept: read final row", "task", t.TaskID, "err", err)
		return
	}
	c.logger.Info("accepted parked copy from peer", "task", t.TaskID, "from", env.From)
	// Forward onward for multi-hop chains (the middle node closes its own
	// copy, then mirrors the accept to the next hop), never back to the
	// sender whose copy is the origin of this decision.
	c.forwardAcceptDownstream(ctx, t.TaskID, env.From)
	result := bus.TaskResultPayload{TaskID: final.TaskID, AttemptID: final.AttemptID, State: final.State, OK: true, Chain: final.Chain}
	if final.ResultJSON != "" {
		_ = json.Unmarshal([]byte(final.ResultJSON), &result)
		result.TaskID, result.AttemptID, result.State, result.Chain = final.TaskID, final.AttemptID, final.State, final.Chain
	}
	c.replyResult(ctx, env, result)
}

// forwardAcceptDownstream mirrors an accepted decision to the sibling copies
// of this task: from the delegator's copy the accept goes to the dispatch
// target (the executor), from the executor's copy to the chain predecessor
// (the delegator). Without it, whichever side approved an AcceptWork park
// first left the other side's copy in review forever — the mirror of the
// reject-forward bug RejectTree fixed in the other direction. Delivery rides
// deliverResume; an unreachable peer gets the accept parked in resume_outbox
// (flushed on its next hello or the periodic sweep). exclude names a peer
// that must not receive the frame (the sender of an accept we are relaying).
func (c *Core) forwardAcceptDownstream(ctx context.Context, taskID, exclude string) {
	if c.db == nil {
		return
	}
	var targets []string
	if target, err := c.store.DispatchTarget(ctx, taskID); err == nil && target != "" {
		targets = append(targets, target)
	}
	if t, err := c.store.Get(ctx, taskID); err == nil {
		if pred := scheduler.Predecessor(t.Chain, c.nodeID); pred != "" {
			targets = append(targets, pred)
		}
	}
	seen := map[string]bool{}
	for _, target := range targets {
		if scheduler.SameRuntimeIdentity(target, c.nodeID) || seen[target] {
			continue
		}
		if exclude != "" && scheduler.SameRuntimeIdentity(target, exclude) {
			continue
		}
		seen[target] = true
		p := bus.TaskResumePayload{TaskID: taskID, Accept: true}
		if c.deliverResume(ctx, target, p) {
			c.logger.Info("accept forwarded to peer copy", "task", taskID, "to", target)
			continue
		}
		c.resumeOutboxPersist(ctx, target, p, time.Now().Add(acceptOutboxTTL).Unix())
	}
}

// acceptOutboxTTL bounds how long an undelivered accept waits for its peer.
// Generous on purpose: an accept is custody of a decision already made (no
// run is gated on it), and the peer may be off overnight; past the bound the
// row is dropped and the peer's own human can still approve locally.
const acceptOutboxTTL = 7 * 24 * time.Hour

// replyResult returns a task_resume outcome to the authenticated requester that
// sent this specific request, not to the historical predecessor in the task's
// chain. That predecessor may be a dead ephemeral participant after restart.
// Failed delivery uses the normal result outbox keyed by the live requester.
func (c *Core) replyResult(ctx context.Context, env bus.Envelope, result bus.TaskResultPayload) {
	if err := c.reply(ctx, env, bus.MsgTaskResult, result); err != nil {
		c.logger.Warn("reply task result", "task", result.TaskID, "to", env.From, "err", err)
		c.outboxPersist(ctx, env.From, result)
		return
	}
	c.outboxDrop(ctx, env.From, result.TaskID)
}

// reply sends a message back to the sender of env. It goes through sendTo —
// not connFor+Send — so a peer whose only live path is a punched datagram
// route still gets its answer when the frame fits, and every caller shares
// the same fallback instead of replies alone blackholing on NAT-bound links.
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
	if err := c.sendTo(env.From, envOut); err != nil {
		c.logger.Warn("reply: no path to peer", "peer", env.From, "type", typ, "err", err)
		return err
	}
	return nil
}

// isOutputStagnant reports whether an agent's output across consecutive supervision rounds
// has made no substantive progress (identical stdout or identical error/stdout).
func isOutputStagnant(currOut, prevOut, currErr, prevErr string) bool {
	cOut := strings.TrimSpace(currOut)
	pOut := strings.TrimSpace(prevOut)
	cErr := strings.TrimSpace(currErr)
	pErr := strings.TrimSpace(prevErr)
	if cOut == pOut && cErr == pErr {
		return true
	}
	// Identical non-empty stdout is stagnation even when the error tail
	// differs: the report the agent produces is what progress looks like.
	if cOut != "" && cOut == pOut {
		return true
	}
	return false
}

func (c *Core) handleAgentNegotiate(ctx context.Context, env bus.Envelope) {
	var p bus.AgentNegotiatePayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("agent negotiate: decode payload", "from", env.From, "err", err)
		return
	}
	// The claim is attributed to the authenticated sender, never to whatever
	// node name the payload asserts (same fail-closed posture as task auth).
	p.FromNode = env.From
	// Weight is wire-supplied like the rest of the claim: bound it to what a
	// priority-derived weight can legitimately be (negoWeightOf, §5.2), or a
	// peer could bid an arbitrary value and preempt every lock in the mesh.
	if p.Weight < 0 {
		p.Weight = 0
	}
	if p.Weight > maxNegoWeight {
		p.Weight = maxNegoWeight
	}
	c.logger.Info("received agent negotiation signal",
		"from_node", p.FromNode, "agent", p.FromAgent, "scope", p.TargetScope.File, "weight", p.Weight)
	dec := c.negoDecide(time.Now(), p, "")
	c.EvTrace(ctx, "", "agent_negotiate", map[string]any{
		"from_node":  p.FromNode,
		"from_agent": p.FromAgent,
		"weight":     p.Weight,
		"scope":      p.TargetScope,
		"intent":     p.Intent,
		"granted":    dec.granted,
		"reason":     dec.reason,
	})
	// Answer first: the requester waits on a 4s timeout while a yield to a
	// remote preempted holder can block up to the write deadline — delivering
	// the verdict first keeps a slow yield from turning a grant into a
	// phantom lock the requester never knew it held.
	grant := bus.AgentGrantPayload{
		LockID:    env.MsgID,
		GrantedTo: p.FromAgent,
		LeaseMS:   dec.leaseMS,
		Denied:    !dec.granted,
		Reason:    dec.reason,
	}
	_ = c.reply(ctx, env, bus.MsgAgentGrant, grant)
	for _, h := range dec.preempted {
		c.negoYieldTo(ctx, h, p.TargetScope, negoHolder(p.FromNode, p.FromAgent))
	}
}

func (c *Core) handleAgentGrant(ctx context.Context, env bus.Envelope) {
	var p bus.AgentGrantPayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("agent grant: decode payload", "from", env.From, "err", err)
		return
	}
	c.logger.Info("received agent lock grant", "lock_id", p.LockID, "granted_to", p.GrantedTo,
		"lease_ms", p.LeaseMS, "denied", p.Denied, "reason", p.Reason)
	c.negoMu.Lock()
	ch := c.negoWaiters[p.LockID]
	c.negoMu.Unlock()
	if ch != nil {
		select {
		case ch <- p:
		default:
		}
		return
	}
	c.EvTrace(ctx, "", "agent_grant", map[string]any{
		"lock_id":    p.LockID,
		"granted_to": p.GrantedTo,
		"lease_ms":   p.LeaseMS,
		"denied":     p.Denied,
	})
}

func (c *Core) handleAgentYield(ctx context.Context, env bus.Envelope) {
	var p bus.AgentYieldPayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("agent yield: decode payload", "from", env.From, "err", err)
		return
	}
	c.logger.Info("agent yield received", "from_agent", p.FromAgent, "scope", p.Scope.File, "reason", p.Reason)
	c.EvTrace(ctx, "", "agent_yield", map[string]any{
		"from_agent": p.FromAgent,
		"scope":      p.Scope,
		"reason":     p.Reason,
	})
	// A yield revokes the grant on the named scope — but only when the sender
	// actually arbitrated it. negotiateTaskScope records every peer whose
	// table granted this lock; a yield from anywhere else is a peer that was
	// never consulted claiming a preemption it could not have decided, and
	// honoring it would let any authenticated node cancel local tasks at will.
	// Payload FromAgent stays informational (the winner's identity).
	key := negoScopeKey(p.Scope)
	c.negoMu.Lock()
	l, held := c.nego[key]
	if !held || !l.grantors[env.From] {
		c.negoMu.Unlock()
		if held {
			c.logger.Warn("agent yield from non-arbitrator ignored",
				"from", env.From, "scope", p.Scope.File, "holder", l.holder)
		}
		return
	}
	victim := l.holder
	c.negoMu.Unlock()
	if node, _, _ := strings.Cut(victim, "|"); node == c.nodeID {
		// The interrupt must find the lock row itself: deleting it first
		// loses holderTask with the row and the running task survives as a
		// zombie holding no grant at all.
		c.negoInterruptLocal(ctx, victim, key)
		return
	}
	c.negoMu.Lock()
	delete(c.nego, key)
	c.negoMu.Unlock()
	c.negoRelease(victim)
}

// SendNegotiation sends a horizontal conflict negotiation signal to target peer (whitepaper §5.1).
func (c *Core) SendNegotiation(ctx context.Context, target string, p bus.AgentNegotiatePayload) error {
	msgID, err := newUUID()
	if err != nil {
		return err
	}
	env, err := bus.NewEnvelope(bus.MsgAgentNegotiate, c.nodeID, msgID, p)
	if err != nil {
		return err
	}
	env.To = target
	return c.sendTo(target, env)
}

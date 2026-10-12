// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ctxstore"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/plan"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
	"github.com/Xustalis/OpenPanda/internal/trust"
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
	// routing loop, which we reject instead of echoing around forever; a chain
	// that has reached the depth cap is rejected too, so sub-schedulers cannot
	// hand work onward indefinitely (S2-5).
	//
	// A chain that is present but does not end at the sender is forged — the
	// last hop is always the node that dispatched this envelope, and anything
	// else means a peer is laundering a task through a chain it never walked
	// (M11). An explicitly empty chain (nil or []) predates the field and is
	// rebuilt from the authenticated sender.
	chain := p.Chain
	if len(chain) == 0 {
		chain = []string{env.From}
	} else if chain[len(chain)-1] != env.From {
		c.logger.Warn("delegation chain last hop mismatch", "task", p.TaskID,
			"from", env.From, "last", chain[len(chain)-1])
		c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{
			TaskID: p.TaskID, Reason: "delegation chain does not end at sender",
		})
		return
	}
	chain, err := scheduler.AppendChain(chain, c.nodeID)
	if err != nil {
		reason := "delegation loop"
		if errors.Is(err, scheduler.ErrChainTooDeep) {
			reason = fmt.Sprintf("delegation chain exceeds %d hops", scheduler.MaxChainDepth)
		}
		c.logger.Warn("delegation refused", "task", p.TaskID, "from", env.From, "reason", reason)
		c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{
			TaskID: p.TaskID, Reason: reason,
		})
		return
	}

	// A bundle that outlived its TTL in transit must not start executing
	// (§8.2): the deadline is the mesh-wide bound every hop shares, and
	// declining lets the delegator's own deadline sweep close its copy too.
	if p.DeadlineUnix > 0 && time.Now().Unix() > p.DeadlineUnix {
		c.logger.Warn("task_delegate arrived past deadline", "task", p.TaskID,
			"from", env.From, "deadline", p.DeadlineUnix)
		c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{
			TaskID: p.TaskID, Reason: "bundle expired in transit",
		})
		return
	}

	// §6.1 token budget: a task that arrives with its quota already spent is
	// declined before any local resource goes to it. The budget is mesh-wide
	// — accepting an exhausted one here would launder the bound into a fresh
	// local quota.
	if p.TokenBudget < 0 {
		c.logger.Warn("task_delegate arrived with exhausted token budget",
			"task", p.TaskID, "from", env.From)
		c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{
			TaskID: p.TaskID, Reason: "token budget exhausted",
		})
		return
	}

	// remote=true: the intent text came off the wire. Persisted because the
	// chain is peer-supplied — a delegate may write chain=[self, sender] and
	// claim we authored it, so executor policy cannot re-derive provenance.
	// The row is created BEFORE admission runs (handleLocalDelegate): a
	// refused or parked delegate still needs an audit record, and
	// terminalizeDeclined/the review park both write onto this row. The cost
	// is that a persistently refused peer adds one cancelled row per push —
	// deliberate audit bookkeeping, not an ordering mistake.
	t, err := c.store.CreateWithID(ctx, p.TaskID, p.ParentID, p.Project, p.TitleOrDefault(), c.nodeID, chain, true)
	if err != nil {
		c.logger.Error("create task from delegate", "err", err)
		return
	}
	// Adopt the upstream attempt so every copy of this task reports the same
	// attempt_id, and a result relayed along the chain is not flagged stale.
	if err := c.store.AdoptAttempt(ctx, t.TaskID, p.AttemptID); err != nil {
		c.logger.Warn("adopt attempt", "task", t.TaskID, "err", err)
	}
	// Resume hint (§5.2): the delegator only sends ResumeSessionID when it
	// believes this node minted the session — adopt it so the run loop picks
	// the conversation back up. A wrong hint costs one cold start, nothing
	// more; it names no capability and grants no privilege.
	if p.ResumeSessionID != "" {
		if err := c.store.SetAgentSession(ctx, t.TaskID, p.ResumeSessionID, c.nodeID); err != nil {
			c.logger.Warn("adopt resume session", "task", t.TaskID, "err", err)
		}
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
	// is the trust boundary; see TaskDelegatePayload.Authorized). When the
	// payload carries a signed consent grant it must verify against the
	// origin node's recorded key — a relay minting another node's consent is
	// the P2-8 hole the grant exists to close, so an unverifiable signature
	// drops the consent rather than laundering it.
	if p.Authorized {
		switch {
		case !c.consentGrantValid(p, env.From):
			c.logger.Warn("delegate consent grant failed verification; dropping authorized flag",
				"task", t.TaskID, "from", env.From)
			c.audit(ctx, t.TaskID, "consent:forged", "", "rejected",
				"invalid Ed25519 consent grant on delegate")
		case c.acceptPolicyFor(c.admissionTier(ctx, chain, env.From)).ForeignConsent == trust.ConsentStrip:
			// A sub-mesh path's consent is genuine but foreign: their user
			// approving the intent cannot silently stand in for THIS node's
			// operator approving an irreversible step on this machine. The
			// tier here is the least-trusting hop of the whole delegation
			// chain, not just the origin — a guarded relay in the path is
			// enough to strip. Drop the grant so the task lands under the
			// restricted tool face and any tier-2 step parks for local
			// approval — "their yes ≠ our yes". Set
			// accept.<tier>.foreign_consent: honor for the same-owner case
			// (an own VPS on an untrusted segment).
			c.logger.Info("foreign consent grant stripped by trust policy",
				"task", t.TaskID, "from", env.From)
			c.audit(ctx, t.TaskID, "consent:stripped", env.From, "applied",
				"delegation path's least-trusting tier strips foreign tier-2 consent")
		default:
			if err := c.store.SetAuthorizedGrant(ctx, t.TaskID, p.AuthSig, p.AuthPub, p.AuthTs); err != nil {
				// Recorded, not fatal. The task row already exists, so returning
				// here would strand it: nothing would ever answer the delegator.
				// An unstamped row is the safe failure — it fails closed at the
				// executor's defense gate rather than running unauthorized.
				c.logger.Error("adopt authorization failed", "task", t.TaskID, "err", err)
			}
		}
	}
	// Persist the entry-model detail carried on the wire so the local queue
	// shows intent/context/complexity/risk even before execution starts.
	if err := c.store.SetDetail(ctx, t.TaskID, delegateDetail(p)); err != nil {
		// As above: losing the detail costs queue readability, not correctness.
		// Dropping the task costs both.
		c.logger.Error("set task detail failed", "task", t.TaskID, "err", err)
	}
	// Adopt the mesh budget the payload carries (§6.1): the row's remaining
	// delegation quota must reflect what the wire declared, or this node —
	// and every restart after it — would spend budget the mesh already used.
	// A nil budget marks a pre-budget peer: seed the default rather than
	// persisting an exhaustion marker the sender never meant.
	budget := scheduler.MaxDelegationBudget
	if p.DelegationBudget != nil {
		budget = *p.DelegationBudget
	}
	if err := c.store.SetDelegationBudget(ctx, t.TaskID, budget); err != nil {
		c.logger.Warn("adopt delegation budget", "task", t.TaskID, "err", err)
	}
	// Same adoption for the token quota (§6.1): zero is the legacy/unbounded
	// default the column already carries, so only a declared remainder needs
	// persisting — and a negative wire value never reaches here (declined
	// above).
	if p.TokenBudget > 0 {
		if err := c.store.SetTokenBudget(ctx, t.TaskID, p.TokenBudget); err != nil {
			c.logger.Warn("adopt token budget", "task", t.TaskID, "err", err)
		}
	}
	// A delegated stage of a plan keeps its place in that plan and the artifacts
	// it must start from. Both are needed locally before execution: run() derives
	// the stage work dir from plan_id/stage_id, and fetchStageInputs pulls the
	// inputs from the nodes named here. The dependency graph is deliberately not
	// carried: only the node orchestrating the plan decides what runs next.
	if p.PlanID != "" || p.StageID != "" {
		if err := c.store.SetStage(ctx, t.TaskID, p.PlanID, p.StageID, nil); err != nil {
			c.logger.Error("set stage metadata failed", "task", t.TaskID, "err", err)
		}
		if len(p.Inputs) > 0 {
			if err := c.store.SetStageInputs(ctx, t.TaskID, p.Inputs); err != nil {
				c.logger.Error("set stage inputs failed", "task", t.TaskID, "err", err)
			}
		}
	} else if len(p.Inputs) > 0 {
		// A standalone task's inputs are its project tree. The column is the same
		// one a stage uses; run() pulls from it either way.
		if err := c.store.SetStageInputs(ctx, t.TaskID, p.Inputs); err != nil {
			c.logger.Error("set project inputs failed", "task", t.TaskID, "err", err)
		}
	}
	// Import any bundled artifacts carried in Fat Bundle (whitepaper §8.3)
	if len(p.BundledArtifacts) > 0 && c.artifacts != nil {
		if _, err := c.ImportFatBundleArtifacts(ctx, p.BundledArtifacts); err != nil {
			c.logger.Warn("import bundled artifacts", "task", t.TaskID, "err", err)
		}
	}
	// The project's memory lands before anything runs, so the agent reads it from
	// the same path a local task would.
	if err := c.landProjectPack(p.Project, p.ProjectPack); err != nil {
		// Degraded, not fatal: the task runs without the project's memory, and
		// the gap is recorded so the operator can see why context was thin.
		if c.store != nil {
			if recordErr := c.store.RecordEvent(ctx, t.TaskID, EvContextDegraded, map[string]any{
				"stage": "land_memory", "project": p.Project, "err": err.Error(),
			}); recordErr != nil {
				c.logger.Error("record context degraded event failed", "err", recordErr)
			}
		}
	}
	// The wire-supplied timeout becomes this node's own lease on the task —
	// including the parked waiting_context state — so it must not exceed the
	// silence bound this node would grant itself. An unbounded value would let
	// a dead delegator's orphan squat an execution slot for as long as the
	// sender named; a live task never notices the cap because renewLease
	// extends the lease for as long as execution actually runs.
	if max := c.lease().Milliseconds(); p.TimeoutMS > max {
		p.TimeoutMS = max
	}
	if p.TimeoutMS > 0 {
		if err := c.store.SetLease(ctx, t.TaskID, p.TimeoutMS); err != nil {
			// The default lease still applies, so this is a warning, not a fail.
			c.logger.Warn("set lease failed, using default", "task", t.TaskID, "err", err)
		}
	}

	required := delegateRequired(p)
	var decision scheduler.Decision
	if strings.TrimSpace(p.TargetNode) != "" {
		// The pin is re-evaluated per hop: resolving to self accepts and runs
		// the task here; resolving onward forwards toward the named node;
		// resolving nowhere declines honestly. What it never does is fall
		// through to scored ranking — the pin names the destination, not a
		// preference.
		decision = c.routePinned(ctx, p.TargetNode, chain, required, resourceRequirement(p.ResourceJSON))
	} else {
		// Trust zones (experimental): a next hop whose send ceiling cannot
		// hold what this delegate carries is not a candidate — the task
		// routes around it instead of arriving stripped of a need.
		decision = scheduler.RouteP(c.nodeID, chain,
			c.eligibleForSend(ctx, trust.NeedsOf(p), c.onlineEmployees(ctx)),
			c.localMatch(), required, resourceRequirement(p.ResourceJSON),
			p.PreferredNode, p.Project)
	}

	switch decision.Action {
	case scheduler.ActionLocal:
		c.handleLocalDelegate(ctx, env, t.TaskID, p, required, chain)
	case scheduler.ActionForward:
		// Sub-scheduler: hand the task to a capable peer. The peer's result
		// arrives later via handleResult, which relays it up the chain — so no
		// immediate reply to the parent here.
		//
		// Hop-limited consent (S2-8): the tier-2 consent minted at the origin
		// decays one hop per relay. This node already adopted it for its own
		// copy above; the copy forwarded onward carries one hop less, and once
		// the hops are spent the consent is cleared so a distant executor asks
		// for fresh approval instead of running irreversible work under a
		// consent it was never granted. A payload with AuthHops == 0 is a
		// legacy/unlimited sender and keeps propagating as before.
		forward := p
		if forward.Authorized && forward.AuthHops > 0 {
			forward.AuthHops--
			if forward.AuthHops == 0 {
				forward.Authorized = false
			}
		}
		if err := c.forwardDelegated(ctx, t.TaskID, decision.Target, forward, chain); err != nil {
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
	if c.node != nil && c.node.draining(ctx) {
		// Maintenance drain (Track 2): decline new work so the delegator
		// re-routes, while tasks already in flight finish undisturbed.
		c.logger.Info("declining delegated task: node draining", "task", taskID)
		c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{TaskID: taskID, Reason: "node draining"})
		c.terminalizeDeclined(ctx, taskID, "node draining")
		return
	}
	// Trust-zone admission (experimental): the origin's tier decides whether
	// push-mode work is even admissible, and the tier's accept policy may
	// refuse it or park it for this node's operator before a slot is ever
	// reserved. mesh resolves to admit — an all-mesh fleet takes this branch
	// unconditionally, exactly as before.
	if adm := c.admitDelegate(ctx, taskID, p, chain, env.From); !adm.admit {
		if adm.review {
			c.parkDelegateReview(ctx, env, taskID, adm.reason)
			return
		}
		c.logger.Info("declining delegated task: trust admission", "task", taskID,
			"reason", adm.reason, "code", adm.code, "from", env.From)
		c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{
			TaskID: taskID, Reason: adm.reason, ReasonCode: adm.code,
		})
		c.terminalizeDeclined(ctx, taskID, adm.reason)
		return
	}
	release, ok := c.reserveCapacity(ctx)
	if !ok {
		c.logger.Info("declining delegated task: capacity full", "task", taskID)
		c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{TaskID: taskID, Reason: "capacity full"})
		c.terminalizeDeclined(ctx, taskID, "capacity full")
		return
	}
	c.proceedLocalDelegate(ctx, env, taskID, p, required, chain, release)
}

// parkDelegateReview holds a just-arrived delegate for this node's operator
// under the tier's accept mode "review". The row was saved by handleDelegate
// before the gate ran, so the admission park reuses the ordinary review →
// approve lifecycle: `panda approve` queues it, the queue scheduler runs it
// here, and the finished result rides back along chain. The delegator sees a
// held task — holdReviewUpstream renews its lease with progress beats and
// marks the first beat "held for operator admission review", so its copy
// waits rather than expiring into a misdiagnosed re-route.
func (c *Core) parkDelegateReview(ctx context.Context, env bus.Envelope, taskID string, reason string) {
	err := c.store.transitionToReview(ctx, taskID, StateSubmitted, StateReview,
		c.nodeID, EvReview, map[string]any{"reason": reason}, ApprovalAdmissionReview)
	if err == nil {
		c.logger.Info("delegated task parked for admission review", "task", taskID)
		c.holdReviewUpstream(taskID)
		return
	}
	// A park that failed to persist must not silently drop the delegate:
	// fall back to declining so the delegator can re-route honestly.
	c.logger.Warn("delegate admission park failed; declining instead",
		"task", taskID, "err", err)
	c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{
		TaskID: taskID, Reason: "admission review park failed", ReasonCode: bus.DeclineCodeTrustRefused,
	})
	c.terminalizeDeclined(ctx, taskID, "admission review park failed")
}

// holdReviewUpstream keeps the delegator's copy of an admission-parked task
// alive while it waits on this node's operator. Nothing executes for a parked
// row, so nothing renews its lease — left alone the delegator's lease monitor
// would expire the task mid-review and re-route the same work to a second
// node, racing a later approval. A progress beat each lease tick marks the
// custody "held, deciding" up the chain — the first beat carries a note so
// `task logs` on the delegator shows why the task is quiet; later beats are
// noteless so a long hold does not spam its audit trail. The loop exits when
// the row leaves review — approval hands it to the queue (whose run renews
// its own lease), a reject or cancel ends it.
func (c *Core) holdReviewUpstream(taskID string) {
	t, err := c.store.Get(context.Background(), taskID)
	if err != nil || t.State != StateReview || len(t.Chain) <= 1 {
		return
	}
	if _, dup := c.reviewHolds.LoadOrStore(taskID, struct{}{}); dup {
		return // a hold is already beating for this row — don't stack loops
	}
	interval := c.lease() / leaseRenewDivisor
	if interval < minLeaseRenewInterval {
		interval = minLeaseRenewInterval
	}
	beat := func(attemptID, note string) {
		c.relayToParent(context.Background(), bus.MsgTaskProgress, t.Chain,
			bus.TaskProgressPayload{TaskID: taskID, AttemptID: attemptID, Note: note})
	}
	beat(t.AttemptID, "held for operator admission review (trust zone)")
	c.goGuarded("review hold "+taskID, func() {
		defer c.reviewHolds.Delete(taskID)
		tk := time.NewTicker(interval)
		defer tk.Stop()
		// The hold is bounded: a review parked past a full day has become a
		// stall, and the delegator's lease expiring then is the honest answer
		// — not a custody claim that silently outlives the work's relevance.
		deadline := time.Now().Add(defaultDTNTTL)
		for range tk.C {
			if time.Now().After(deadline) {
				return
			}
			cur, gerr := c.store.Get(context.Background(), taskID)
			if gerr != nil || cur.State != StateReview {
				return
			}
			beat(cur.AttemptID, "")
		}
	})
}

// resumeReviewHolds restarts the upstream hold for every remote-origin task
// still parked in review after a restart — the keepalive lives in memory, so
// a daemon bounce would otherwise silently drop the beats and let the
// delegator's lease expire mid-review exactly as if the hold never existed.
// Covers any remote review row (trust admission park and the older defense
// park share the expiry hazard); holdReviewUpstream itself skips local rows.
func (c *Core) resumeReviewHolds(ctx context.Context) {
	rows, err := c.store.ListByState(ctx, StateReview)
	if err != nil {
		c.logger.Debug("resume review holds: list failed", "err", err)
		return
	}
	for _, t := range rows {
		if len(t.Chain) > 1 {
			c.holdReviewUpstream(t.TaskID)
		}
	}
}

// proceedLocalDelegate is the post-admission body of handleLocalDelegate,
// split out so a task resumed from an artifact-push park re-enters the same
// context-resolution and execution path without taking a second capacity
// reservation (its parked row was countable the whole wait). The release
// contract is unchanged: held until the row claims a countable slot, fired
// on every early return instead.
func (c *Core) proceedLocalDelegate(ctx context.Context, env bus.Envelope, taskID string, p bus.TaskDelegatePayload, required []string, chain []string, release func()) {
	level := p.ContextLevel
	hash := p.ContextHash

	if level == "full" && len(p.ContextData) > 0 {
		// Inline snapshot: cache it and proceed without a round-trip — but only
		// after the same integrity check handleContextAck applies. Put keys the
		// entry on the caller-supplied hash without verifying it, so an
		// unchecked blob would be cached under a name its bytes do not hash to
		// (M1): a later pointer fetch for that hash would then hand the
		// executor attacker-chosen context that verifies "correctly".
		if hash == "" || ctxstore.Hash(p.ContextData) != hash {
			c.logger.Warn("inline context hash mismatch", "task", taskID, "hash", hash)
			c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{
				TaskID: taskID, Reason: "inline context hash mismatch",
			})
			c.terminalizeDeclined(ctx, taskID, "inline context hash mismatch")
			release()
			return
		}
		if err := c.ctx.Put(ctx, hash, p.ContextType, p.ContextData, nil); err != nil {
			c.logger.Warn("store inline context", "task", taskID, "err", err)
		}
	}

	// §8.3 fat-push: a dtn task's payload may still be on the wire behind the
	// delegate. Failing now — the old outcome — would defeat the push; the
	// task parks in waiting_context until every missing input verifies or the
	// bundle's own deadline expires. Live-transport tasks keep the pull path
	// inside run(), which fails fast as before.
	if p.Transport == "dtn" {
		if missing := c.missingPushInputs(p); len(missing) > 0 {
			c.parkForArtifactPush(ctx, env, taskID, p, required, missing, release)
			return
		}
	}

	if level == "pointer" && hash != "" {
		if ok, _ := c.ctx.Contains(ctx, hash); !ok {
			if err := c.prepare(ctx, taskID); err != nil {
				c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{TaskID: taskID, Reason: err.Error()})
				release()
				return
			}
			if err := c.store.SetWaitingContext(ctx, taskID, c.nodeID); err != nil {
				c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{TaskID: taskID, Reason: err.Error()})
				release()
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
			// The fetch goes to the peer that handed us the task, not the
			// origin: chain[0] is unreachable by construction on a partial
			// mesh (the very delegation topology that made the task
			// multi-hop), while env.From just sent us a frame. A
			// predecessor that does not hold the snapshot relays the fetch
			// up its own chain (relayContextFetch), so the origin's answer
			// still arrives — one hop at a time.
			src := env.From
			if src == "" {
				src = chain[0]
			}
			c.pendingCtx.Store(taskID, &pendingContext{intent: p.Intent, required: required, ctxType: p.ContextType, source: src, release: release})
			c.sendContextFetch(ctx, src, taskID, hash, p.ContextType)
			return
		}
	}

	// Execute asynchronously so the message loop stays responsive to
	// task_cancel while a long native/agent command runs. The result (or a
	// decline) is reported back via the env captured here.
	c.goGuarded("delegated exec "+taskID, func() {
		result, err := c.execute(ctx, taskID, p.Intent, required, release)
		if err != nil {
			if errors.Is(err, ErrCancelled) {
				c.logger.Info("task cancelled during execution", "task", taskID)
				return
			}
			if errors.Is(err, ErrAlreadyRunning) {
				// Another runner already owns the row (a duplicate delivery's
				// first invocation, a resume that raced ahead): it reports the
				// outcome through its own env, and declining here would cancel
				// a live task the delegator is waiting on.
				c.logger.Info("delegated task already running; standing down", "task", taskID)
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
	})
}

// reserveCapacity claims one execution slot for an incoming delegated task
// (DCPS capacity-driven accept/decline, design §2.4): true unless the
// capability card declares a MaxConcurrent limit and the active-task count —
// plus the reservations already handed out — has reached it. A node with no
// declared limit always accepts (unknown capacity is not a limit); a
// load-count failure fails closed (declining is recoverable — the delegator
// re-routes — while over-committing a saturated node is not).
//
// The returned release must be called exactly once. The reservation is a
// promise: the row does not occupy a countable slot until it reaches
// running/waiting_context, and without the in-memory counter every delegate
// that arrived in the gap would read the same "active < max" and all accept
// (M14).
func (c *Core) reserveCapacity(ctx context.Context) (release func(), ok bool) {
	maxConcurrent := c.Card().Capacity.MaxConcurrent
	if maxConcurrent <= 0 {
		return func() {}, true
	}
	active, err := c.store.CountActive(ctx, c.nodeID)
	if err != nil {
		c.logger.Warn("count active tasks", "err", err)
		return nil, false
	}
	reserved := c.execSlots.Add(1)
	if active+int(reserved) > maxConcurrent {
		c.execSlots.Add(-1)
		return nil, false
	}
	var once sync.Once
	return func() { once.Do(func() { c.execSlots.Add(-1) }) }, true
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
		plan, err := router.Route(required)
		if err != nil {
			return false
		}
		// A static ability match is not dispatch readiness: the plan must
		// also have a usable executor. Without this gate a node whose
		// harnesses are all installed-but-unconfigured — or whose model
		// endpoint is down — still claims the task, accepts it, and blocks
		// inside the adapter instead of letting the scheduler fail over to
		// a healthy peer.
		return router.PlanUsable(plan)
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
	nodes = c.applyPeerBlockers(nodes)
	c.annotateReputation(ctx, nodes)
	return nodes
}

// applyPeerBlockers strips agents a peer's heartbeats report as circuit-open
// from that peer's ability set, so Route weighs the peer's failure history
// into candidate selection instead of learning it one bounce-decline at a
// time: a node whose only match for the task is a failing agent loses the
// match and the task routes to a healthier candidate directly. The self row
// is left alone — the local breaker is enforced at execution time (run),
// where a half-open trial can still clear the circuit.
func (c *Core) applyPeerBlockers(nodes []ledger.Node) []ledger.Node {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for i := range nodes {
		if scheduler.IsSelfRow(nodes[i].ID, c.nodeID) || len(nodes[i].Agents) == 0 {
			continue
		}
		blocked := c.peerBlocked[nodes[i].ID]
		if len(blocked) == 0 {
			continue
		}
		hit := false
		for _, name := range blocked {
			if _, ok := nodes[i].Agents[name]; ok {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		agents := make(map[string]ledger.Agent, len(nodes[i].Agents))
		for name, ag := range nodes[i].Agents {
			agents[name] = ag
		}
		for _, name := range blocked {
			delete(agents, name)
		}
		nodes[i].Agents = agents
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

// defaultDTNTTL bounds how long a parked DTN task stays deliverable when the
// task itself declares no deadline (§8.2). Store-and-forward is measured in
// hours, not lease seconds.
const defaultDTNTTL = 24 * time.Hour

// delegationBudget resolves the remaining §6.1 forward budget for taskID and
// spends one hop, returning what the wire should carry. The persisted row is
// authoritative — a received task's row was seeded from the wire on arrival —
// with the caller's payload as fallback for a row that has no record yet. An
// origin row (chain of self only, budget unset) seeds the default; a
// mid-chain task whose budget is spent is refused: decrementing a zero would
// launder the bound, since the receiver seeds its row from the wire.
func (c *Core) delegationBudget(ctx context.Context, taskID string, wireBudget int) (int, error) {
	if t, err := c.store.Get(ctx, taskID); err == nil {
		switch {
		case t.DelegationBudget > 0:
			wireBudget = t.DelegationBudget
		case len(t.Chain) <= 1 && wireBudget <= 0:
			wireBudget = scheduler.MaxDelegationBudget
		}
	}
	if wireBudget <= 0 {
		return 0, scheduler.ErrBudgetExceeded
	}
	return wireBudget - 1, nil
}

// derefBudget reads the wire's optional budget field (nil = pre-budget peer).
func derefBudget(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// tokenBudgetWire resolves the §6.1 token quota the wire should carry for
// taskID. The persisted row is authoritative — local execution spend has
// already been deducted from it — so a stored non-zero value replaces the
// payload's. An exhausted row (-1) refuses the hop outright: forwarding it
// would let the next node spend quota the mesh already declared gone.
func (c *Core) tokenBudgetWire(ctx context.Context, taskID string, wireBudget int64) (int64, error) {
	if t, err := c.store.Get(ctx, taskID); err == nil && t.TokenBudget != 0 {
		wireBudget = t.TokenBudget
	}
	if wireBudget < 0 {
		return 0, scheduler.ErrBudgetExceeded
	}
	return wireBudget, nil
}

// dispatchDelegated is forwardDelegated for a task already in queued state —
// e.g. a declined task being re-routed (P1-5), where Decline already moved it
// dispatched -> queued and a second queue transition would conflict.
func (c *Core) dispatchDelegated(ctx context.Context, taskID, target string, p bus.TaskDelegatePayload, chain []string) error {
	// Trust-zone send ceiling (experimental): clamp the payload to what this
	// next hop's tier may receive, BEFORE any budget is spent or the dispatch
	// is recorded. A target whose ceiling strips a need the task cannot run
	// without is refused outright — the caller re-routes or fails honestly
	// rather than shipping a lobotomized delegate. mesh resolves to a no-op
	// policy, so a fleet that never opted in runs this as a pure pass-through.
	if !c.clampForPeer(target, &p) {
		return fmt.Errorf("peer %s trust ceiling refuses the task's needs (%s)", target, bus.DeclineCodeTrustCeiling)
	}
	// §6.1 mesh budget: every forward hop spends one delegation, on the wire
	// and in the row, so the mesh-wide bound survives restarts and relays.
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
	// Dispatch records the new delegation target on the audit trail — the
	// supersede check must read the PREVIOUS target first, so it runs before
	// the transition, not after.
	c.supersedeTarget(ctx, taskID, target)
	if err := c.store.Dispatch(ctx, taskID, c.nodeID, target); err != nil {
		return fmt.Errorf("dispatch: %w", err)
	}
	dtn := p.Transport == "dtn"
	// A hard-pinned task shares DTN's delivery contract: the named peer is the
	// only acceptable destination, so a dead link means park-and-wait — and a
	// parked copy must hold no lease. An expiry ticking while custody still
	// intends delivery would fail the local row, and the real result would
	// land on a terminal task after the peer's reconnect.
	pinned := p.TargetNode != ""
	parkable := dtn || pinned
	// The deadline a parked bundle or a deferred push lives under: the wire's
	// absolute TTL, else the default DTN lifetime. Computed once here because
	// both the fat-bundle attachment below and the send-failure park use it.
	pushDeadline := p.DeadlineUnix
	if pushDeadline <= 0 {
		pushDeadline = time.Now().Add(defaultDTNTTL).Unix()
	}
	park := parkable && !c.sendableTo(target)
	// Stamp a lease on the local copy so a dead executor is detected and the
	// failure propagated, instead of leaving this copy dispatched forever (D3).
	// The timeout is carried on the wire so every hop inherits the same
	// deadline. A DTN task is exempt (§8.2): a store-and-forward path has no
	// heartbeat to renew against, so the absolute DeadlineUnix is its bound.
	// A pinned task about to be parked is exempt for the same reason.
	timeoutMS := p.TimeoutMS
	if !park && !dtn {
		if timeoutMS <= 0 {
			timeoutMS = c.lease().Milliseconds()
			p.TimeoutMS = timeoutMS
		}
		if err := c.store.SetLease(ctx, taskID, timeoutMS); err != nil {
			return fmt.Errorf("set lease: %w", err)
		}
	} else if dtn {
		// Inline what fits the envelope; everything else becomes a chunked
		// push obligation the flush drains once the link carries the delegate.
		for _, h := range c.attachFatBundle(ctx, &p) {
			c.artifactPushEnqueue(ctx, target, taskID, h, pushDeadline)
		}
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
	delivered := false
	var sendErr error
	if !park {
		sendErr = c.sendTo(target, env)
		delivered = sendErr == nil
	}
	if !delivered {
		kind := "dtn"
		if pinned && !dtn {
			kind = "pin"
			if !park {
				// The link died between the sendable check and the send: take
				// the lease stamped above back off before custody takes over.
				if cerr := c.store.ClearLease(ctx, taskID); cerr != nil {
					c.logger.Warn("clear lease for parked pinned task", "task", taskID, "err", cerr)
				}
			}
		}
		c.logger.Info("delegation undeliverable; parked in task_outbox for the peer's next contact",
			"target", target, "task", taskID, "kind", kind, "err", sendErr)
		c.taskOutboxPersist(ctx, target, p, kind, pushDeadline)
	} else {
		c.logger.Info("forwarded delegated task", "task", taskID, "to", target)
		if dtn {
			// The delegate is on the wire — drain the push custody now rather
			// than waiting for the next sweep, so chunks follow the task into
			// the same contact window.
			c.goGuarded("outbox flush "+target, func() { c.outboxFlush(context.WithoutCancel(ctx), target) })
		}
	}
	// Persist the spent budget only once the hop is committed — sent or
	// parked. A build/lookup failure above leaves the row untouched so a
	// retry does not burn a second hop.
	if err := c.store.SetDelegationBudget(ctx, taskID, remaining); err != nil {
		c.logger.Warn("persist delegation budget", "task", taskID, "err", err)
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
	return nil
}

// delegateDetail maps the wire payload's entry-model fields onto the persisted
// TaskDetail. resource_json is not present on the Phase 1 wire format, so it is
// left empty until a later phase adds it.
func delegateDetail(p bus.TaskDelegatePayload) TaskDetail {
	specJSON := p.SpecJSON
	if p.TargetNode != "" || p.PreferredNode != "" {
		// The wire carries the canonical pin identity (k:<pubkey> when the
		// destination proved a key); persist THAT into spec.node so a later
		// local re-dispatch — restart recovery, a requeue — resolves by
		// identity instead of the raw name the origin user typed, which could
		// collide with a same-named ghost row on this node's directory.
		var m map[string]any
		if err := json.Unmarshal([]byte(specJSON), &m); err != nil || m == nil {
			m = map[string]any{}
		}
		if p.TargetNode != "" {
			m["node"] = p.TargetNode
		}
		if p.PreferredNode != "" {
			m["preferred"] = p.PreferredNode
		}
		if b, err := json.Marshal(m); err == nil {
			specJSON = string(b)
		}
	}
	if p.UserLocale != "" {
		var m map[string]any
		if err := json.Unmarshal([]byte(specJSON), &m); err == nil && m != nil {
			if _, ok := m["user_locale"]; !ok {
				m["user_locale"] = p.UserLocale
				if b, err := json.Marshal(m); err == nil {
					specJSON = string(b)
				}
			}
		} else if specJSON == "" {
			m = map[string]any{"user_locale": p.UserLocale}
			if b, err := json.Marshal(m); err == nil {
				specJSON = string(b)
			}
		}
	}
	return TaskDetail{
		ContextType:  p.ContextType,
		ContextHash:  p.ContextHash,
		Intent:       p.Intent,
		SpecJSON:     specJSON,
		Complexity:   p.Complexity,
		Risk:         p.Risk,
		ResourceJSON: p.ResourceJSON,
		Requires:     delegateRequired(p),
		UserLocale:   p.UserLocale,
		Transport:    p.Transport,
		DeadlineUnix: p.DeadlineUnix,
	}
}

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
	"sync"
	"sync/atomic"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/ctxstore"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/plan"
	"github.com/Xustalis/OpenPanda/internal/providers"
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
			// Recorded, not fatal. The task row already exists, so returning
			// here would strand it: nothing would ever answer the delegator.
			// An unstamped row is the safe failure — it fails closed at the
			// executor's defense gate rather than running unauthorized.
			c.logger.Error("adopt authorization failed", "task", t.TaskID, "err", err)
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
	if p.DelegationBudget > 0 {
		if err := c.store.SetDelegationBudget(ctx, t.TaskID, p.DelegationBudget); err != nil {
			c.logger.Warn("adopt delegation budget", "task", t.TaskID, "err", err)
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
	if p.TimeoutMS > 0 {
		if err := c.store.SetLease(ctx, t.TaskID, p.TimeoutMS); err != nil {
			// The default lease still applies, so this is a warning, not a fail.
			c.logger.Warn("set lease failed, using default", "task", t.TaskID, "err", err)
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
	release, ok := c.reserveCapacity(ctx)
	if !ok {
		c.logger.Info("declining delegated task: capacity full", "task", taskID)
		c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{TaskID: taskID, Reason: "capacity full"})
		c.terminalizeDeclined(ctx, taskID, "capacity full")
		return
	}
	// The reservation is held until the task's row claims the slot for real —
	// SetWaitingContext/Rename-to-running happens inside the goroutine, so the
	// release fires there. Any early return before the goroutine spawns must
	// release immediately or the slot leaks.
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
	} else if level == "pointer" && hash != "" {
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
			c.pendingCtx.Store(taskID, &pendingContext{intent: p.Intent, required: required, ctxType: p.ContextType, source: chain[0], release: release})
			c.sendContextFetch(ctx, chain[0], taskID, hash, p.ContextType)
			return
		}
	}

	// Execute asynchronously so the message loop stays responsive to
	// task_cancel while a long native/agent command runs. The result (or a
	// decline) is reported back via the env captured here.
	go func() {
		result, err := c.execute(ctx, taskID, p.Intent, required, release)
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
	return c.applyPeerBlockers(nodes)
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

// dispatchDelegated is forwardDelegated for a task already in queued state —
// e.g. a declined task being re-routed (P1-5), where Decline already moved it
// dispatched -> queued and a second queue transition would conflict.
func (c *Core) dispatchDelegated(ctx context.Context, taskID, target string, p bus.TaskDelegatePayload, chain []string) error {
	// §6.1 mesh budget: every forward hop spends one delegation, on the wire
	// and in the row, so the mesh-wide bound survives restarts and relays.
	remaining, err := c.delegationBudget(ctx, taskID, p.DelegationBudget)
	if err != nil {
		return err
	}
	p.DelegationBudget = remaining
	if err := c.store.Dispatch(ctx, taskID, c.nodeID, target); err != nil {
		return fmt.Errorf("dispatch: %w", err)
	}
	if err := c.store.SetDelegationBudget(ctx, taskID, p.DelegationBudget); err != nil {
		c.logger.Warn("persist delegation budget", "task", taskID, "err", err)
	}
	dtn := p.Transport == "dtn"
	// Stamp a lease on the local copy so a dead executor is detected and the
	// failure propagated, instead of leaving this copy dispatched forever (D3).
	// The timeout is carried on the wire so every hop inherits the same
	// deadline. A DTN task is exempt (§8.2): a store-and-forward path has no
	// heartbeat to renew against, so the absolute DeadlineUnix is its bound.
	timeoutMS := p.TimeoutMS
	if !dtn {
		if timeoutMS <= 0 {
			timeoutMS = c.lease().Milliseconds()
			p.TimeoutMS = timeoutMS
		}
		if err := c.store.SetLease(ctx, taskID, timeoutMS); err != nil {
			return fmt.Errorf("set lease: %w", err)
		}
	} else {
		c.attachFatBundle(ctx, &p)
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
		deadline := p.DeadlineUnix
		if deadline <= 0 {
			deadline = time.Now().Add(defaultDTNTTL).Unix()
		}
		c.logger.Info("delegation send failed or target offline; parking in task_outbox for DTN relay",
			"target", target, "task", taskID, "err", err)
		c.taskOutboxPersist(ctx, target, p, "dtn", deadline)
	} else {
		c.logger.Info("forwarded delegated task", "task", taskID, "to", target)
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
func (c *Core) prepare(ctx context.Context, taskID string) error {
	if err := c.store.Queue(ctx, taskID, c.nodeID); err != nil {
		return fmt.Errorf("queue: %w", err)
	}
	if err := c.store.Dispatch(ctx, taskID, c.nodeID, c.nodeID); err != nil {
		return fmt.Errorf("dispatch: %w", err)
	}
	return nil
}

// storeWriteCtx returns a context bounded to 5 seconds that survives cancellation
// of ctx, ensuring task terminal status and event writes are committed to SQLite.
func (c *Core) storeWriteCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
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
	defer func() {
		if out.TaskID != "" {
			out.DurationMS = time.Since(start).Milliseconds()
			out.Executor = c.nodeID
			if len(out.Chain) == 0 {
				out.Chain = taskChain
			}
		} else if rerr != nil {
			// If run failed or cancelled before reaching a terminal state,
			// fail the task in the store so it never lingers stranded in 'running'.
			wCtx, cancel := c.storeWriteCtx(ctx)
			defer cancel()
			if t, err := c.store.Get(wCtx, taskID); err == nil && t.State == StateRunning {
				_ = c.store.Fail(wCtx, taskID, c.nodeID, rerr.Error())
			}
		}
	}()
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
				return bus.TaskResultPayload{}, fmt.Errorf("agent %s circuit open", plan.Agent)
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

	task, err := c.store.Get(ctx, taskID)
	if err != nil {
		return bus.TaskResultPayload{}, fmt.Errorf("load task: %w", err)
	}
	// §7.2: an actuator plan's argv is a template — fill {intent}/{action}/
	// {param:<name>} from the task's action_spec before anything runs. A
	// substitution failure means the spec and the card disagree, which no
	// retry fixes: fail at the gate, not inside the driver.
	if plan.ActuatorID != "" {
		spec, serr := commander.ParseActionSpec(task.SpecJSON)
		if serr != nil {
			return bus.TaskResultPayload{}, fmt.Errorf("route actuator: %w", serr)
		}
		if err := commander.SubstituteActionSpec(&plan, spec, intent); err != nil {
			return bus.TaskResultPayload{}, fmt.Errorf("route actuator: %w", err)
		}
	}
	taskChain = task.Chain
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
					// The row is verifiably in a countable state: the
					// reservation is redundant from here on — free it now
					// rather than at run end (double-count). Reachable by
					// fall-through alone, so release before the break.
					releaseSlot()
					releaseSlot = func() {}
					break
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
		// Resume succeeded: the row is running and countable, so the
		// reservation is freed now — holding it through execution would
		// double-count the slot.
		releaseSlot()
		releaseSlot = func() {}
	case StateRunning:
		// Already running (a duplicate context_ack raced ahead): the slot is
		// countable, so the reservation is redundant — free it now.
		releaseSlot()
		releaseSlot = func() {}
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
	} else if projectInputs(task) {
		// A delegated project task: the tree it must start from arrived as an
		// artifact reference, and it unpacks into this node's own directory for the
		// project. Failing rather than running on an absent tree is the same
		// judgement the stage path makes — an agent let loose in an empty directory
		// reports success over work it never had the inputs to do.
		wd, err := c.projectWorkDir(task.Project)
		if err != nil {
			return bus.TaskResultPayload{}, err
		}
		workDir = wd
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			return bus.TaskResultPayload{}, fmt.Errorf("create project work dir: %w", err)
		}
		if err := c.fetchStageInputs(execCtx, task, workDir); err != nil {
			return bus.TaskResultPayload{}, fmt.Errorf("project inputs: %w", err)
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
		shadowConflicts = conflicts
		c.EvTrace(execCtx, taskID, "shadow_merge", map[string]any{
			"restored":  len(restored),
			"conflicts": conflicts,
		})
		c.audit(ctx, taskID, "shadow:merge", "", "merged",
			fmt.Sprintf("restored %d preempted files; %d contested", len(restored), len(conflicts)))
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
			return bus.TaskResultPayload{}, fmt.Errorf("scope negotiation: %w", err)
		}
		defer c.negoReleaseTask(task.TaskID)
	}
	var before defense.Snapshot
	if plan.Kind == "agent" && !scope.Empty() {
		var err error
		if before, err = defense.SnapshotDir(workDir); err != nil {
			c.logger.Warn("snapshot workdir before agent", "task", taskID, "err", err)
		}
	}

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
		})
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
	var sessionID string
	var lastAgent, lastOutput, lastStderr string
	verdict := entry.SuperviseVerdict{Status: entry.VerdictDone}
	// delegations bounds how many PANDA_DELEGATE promotions one task may make
	// (§4.2). Each re-runs the round with the child's result folded into the
	// intent, so the counter — not the judge budget — is the guard against a
	// marker-happy agent; the mesh budget bounds the spawned tree itself.
	const maxDelegateRequests = 4
	delegations := 0
	for round := 0; round < maxRounds; round++ {
		// §6.2 monotonic progress: hashing the work tree at each round's head
		// means a later round that regresses to a state an earlier round (or
		// an earlier task on this project) already produced is caught before
		// more tokens are spent — agent B silently undoing agent A's fix can
		// never converge, so it fails fast with a trace event instead.
		if oscKey != "" {
			if h, n, herr := defense.HashDir(workDir, stateHashMaxFiles); herr == nil && n <= stateHashMaxFiles && n > 0 {
				if c.stateOscillates(oscKey, h) {
					c.EvTrace(execCtx, taskID, "state_oscillation", map[string]any{
						"key":   oscKey,
						"hash":  h,
						"round": round + 1,
					})
					return bus.TaskResultPayload{}, fmt.Errorf(
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
		})

		runCtx := execCtx
		if sessionID != "" {
			runCtx = commander.WithResume(execCtx, sessionID)
		}
		res = router.Execute(runCtx, plan, prompt, workDir, task.Authorized)
		if res.SessionID != "" {
			sessionID = res.SessionID
		}

		// §4.2 Sub-MainAgent promotion: an agent that hits a resource it lacks
		// (GPU, tool, hardware actuator) may emit a PANDA_DELEGATE line. This
		// node — now acting as the child's Sub-Main — spawns the causal child,
		// waits for its result, and re-runs the round with the product folded
		// into the intent. The delegation never reaches a judge: the round is
		// re-driven, not verified.
		if plan.Kind == "agent" {
			if dr, cleaned, ok := parseDelegateRequest(res.Stdout); ok {
				res.Stdout = cleaned
				if delegations < maxDelegateRequests {
					delegations++
					note, derr := c.delegateChild(execCtx, task, dr)
					if derr != nil {
						note = "delegation failed: " + derr.Error()
					}
					currentIntent += "\n\n[delegated child result]\n" + note
				} else {
					currentIntent += "\n\n[system] delegation budget for this task is exhausted; finish with local resources and report."
				}
				round--
				continue
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
		// verdict pill always reflect a decided round.
		// Gated: an authorization refusal parks before agent execution; emitting
		// a failed supervision round would falsely mark that a judge round took place.
		judgeWillRun := plan.Kind == "agent" && c.supervisor != nil && round < maxRounds-1 && res.OK
		if !judgeWillRun && !commander.IsAuthorizationRefusal(res.Stderr) {
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
			// Structured usage (when the adapter speaks it): the flat Tokens
			// total rides the existing pipeline; the breakdown lands as its
			// own event so input/output/cache traffic can be told apart.
			if res.Usage != nil {
				c.EvTrace(execCtx, taskID, EvAgentUsage, map[string]any{
					"agent":              res.Agent,
					"round":              round,
					"input_tokens":       res.Usage.InputTokens,
					"output_tokens":      res.Usage.OutputTokens,
					"cache_read_tokens":  res.Usage.CacheReadTokens,
					"cache_write_tokens": res.Usage.CacheWriteTokens,
				})
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
					TaskID: taskID, AttemptID: attemptID, State: StateReview,
					ApprovalDisposition: string(ApprovalNeedsChangedInput),
					OK:                  false, ExitCode: 1, Stderr: msg,
					Tokens: res.Tokens, Cost: res.Cost, Agent: res.Agent, Model: res.Model, Injected: res.Injected,
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
				TaskID: taskID, AttemptID: attemptID, State: StateReview,
				ApprovalDisposition: string(ApprovalAcceptWork),
				OK:                  true, ExitCode: 0, Stdout: res.Stdout,
				Tokens: res.Tokens, Cost: res.Cost, Agent: res.Agent, Model: res.Model, Injected: res.Injected,
			}, nil
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
						return bus.TaskResultPayload{}, ErrCancelled
					}
					return bus.TaskResultPayload{}, fmt.Errorf("fail: %w", err)
				}
				if err := c.store.Review(wCtx, taskID, c.nodeID, msg); err != nil {
					if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
						return bus.TaskResultPayload{}, ErrCancelled
					}
					return bus.TaskResultPayload{}, fmt.Errorf("park overflowed task: %w", err)
				}
				c.logTask(task.Title, false)
				trackTask(c, task.Project, required, task.Title, false)
				return bus.TaskResultPayload{
					TaskID: taskID, AttemptID: attemptID, State: StateReview,
					ApprovalDisposition: string(ApprovalNeedsChangedInput),
					OK:                  false, ExitCode: res.ExitCode, Stderr: msg,
					Tokens: res.Tokens, Cost: res.Cost, Agent: res.Agent, Model: res.Model, Injected: res.Injected,
				}, nil
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
						return bus.TaskResultPayload{}, ErrCancelled
					}
					return bus.TaskResultPayload{}, fmt.Errorf("fail: %w", err)
				}
				if err := c.store.ReviewWithDisposition(wCtx, taskID, c.nodeID, res.Stderr, ApprovalResumeExecution); err != nil {
					if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
						return bus.TaskResultPayload{}, ErrCancelled
					}
					return bus.TaskResultPayload{}, fmt.Errorf("park refused task: %w", err)
				}
				c.logTask(task.Title, false)
				trackTask(c, task.Project, required, task.Title, false)
				return bus.TaskResultPayload{
					TaskID: taskID, AttemptID: attemptID, State: StateReview,
					ApprovalDisposition: string(ApprovalResumeExecution),
					OK:                  false, ExitCode: res.ExitCode, Stderr: res.Stderr,
					Tokens: res.Tokens, Cost: res.Cost, Agent: res.Agent, Model: res.Model, Injected: res.Injected,
				}, nil
			}
			wCtx, cancel := c.storeWriteCtx(ctx)
			defer cancel()
			if err := c.store.Fail(wCtx, taskID, c.nodeID, res.Stderr); err != nil {
				if errors.Is(err, ErrConflict) || errors.Is(err, ErrIllegal) {
					return bus.TaskResultPayload{}, ErrCancelled
				}
				return bus.TaskResultPayload{}, fmt.Errorf("fail: %w", err)
			}
			c.logTask(task.Title, false)
			trackTask(c, task.Project, required, task.Title, false)
			return bus.TaskResultPayload{
				TaskID: taskID, AttemptID: attemptID, State: StateFailed, OK: false, ExitCode: res.ExitCode, Stderr: res.Stderr,
				Tokens: res.Tokens, Cost: res.Cost, Agent: res.Agent, Model: res.Model, Injected: res.Injected,
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
		judgeResult := res.Stdout
		if strings.TrimSpace(res.Stderr) != "" {
			judgeResult = judgeResult + "\n\n错误输出（stderr）：\n" + res.Stderr
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
	} else if projectInputs(task) {
		// A project task executed on someone else's behalf hands its tree back the
		// same way a stage does, so the changes it made reach the machine that
		// asked. A pack failure is a warning rather than a failure here: the work
		// itself succeeded, and reporting it failed would be a worse lie than
		// reporting it without the tree.
		if hash, perr := c.packStageOutput(ctx, task, workDir); perr != nil {
			c.logger.Warn("pack project output", "task", taskID, "err", perr)
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
			Tokens: res.Tokens, Cost: res.Cost, OutputArtifact: outputArtifact, Agent: res.Agent, Model: res.Model, Injected: res.Injected,
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
				Tokens: res.Tokens, Cost: res.Cost, OutputArtifact: outputArtifact, Agent: res.Agent, Model: res.Model, Injected: res.Injected,
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
		Tokens: res.Tokens, Cost: res.Cost, OutputArtifact: outputArtifact, Agent: res.Agent, Model: res.Model, Injected: res.Injected,
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

// taskToolsPolicy extracts the per-task tools policy from a task's persisted
// spec JSON (entry.TaskSpec.ToolsPolicy). Empty means "use the global
// routing.tools_policy"; "minimal" or "extended" override the router's
// default for this task alone. A parse failure yields empty (global default).
func taskToolsPolicy(specJSON string) string {
	if specJSON == "" {
		return ""
	}
	var spec struct {
		ToolsPolicy string `json:"tools_policy"`
	}
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		return ""
	}
	if spec.ToolsPolicy != "minimal" && spec.ToolsPolicy != "extended" {
		return ""
	}
	return spec.ToolsPolicy
}

// agentPromptBudget is the soft cap (bytes) on the assembled agent prompt.
// The budget exists because the prompt is composed of unbounded parts
// (matched skill bodies, the memory manifest) and no adapter can see an
// agent's context window from here; overflowing the window fails the run
// deterministically (ContextOverflow parks it). When the budget is tight the
// skills section degrades first — full bodies shrink to index lines — while
// the intent and the memory manifest are kept whole: the degradation order
// is intent > memory manifest > skill index > skill body. Set to 128KB so
// multi-byte UTF-8 (e.g. Chinese text) has ample headroom without prematurely
// degrading skills into index lines.
const agentPromptBudget = 128000

func getAgentOutputRider(locs ...i18n.Locale) string {
	promptLang := i18n.English
	outputLang := i18n.English
	if len(locs) == 1 {
		if locs[0] != "" {
			promptLang = locs[0]
			outputLang = locs[0]
		}
	} else if len(locs) >= 2 {
		if locs[0] != "" {
			promptLang = locs[0]
		}
		if locs[1] != "" {
			outputLang = locs[1]
		}
	}

	if promptLang == i18n.ChineseSimp {
		switch outputLang {
		case i18n.English:
			return "\n\n输出与执行要求：请高效聚焦核心目标，确保彻底完成任务。排查与分析任务请优先检索与阅读项目源代码（.go, .py, .yaml, .json, .md）与文档，避免无意义的二进制反汇编窥探；无论推理过程如何，请在获取到充分证据后直接输出清晰结构化的英文报告与最终结论；不要使用不必要的表情符号。"
		case i18n.Japanese:
			return "\n\n输出与执行要求：请高效聚焦核心目标，确保彻底完成任务。排查与分析任务请优先检索与阅读项目源代码（.go, .py, .yaml, .json, .md）与文档，避免无意义的二进制反汇编窥探；无论推理过程如何，请在获取到充分证据后直接输出清晰结构化的日文报告与最终结论；不要使用不必要的表情符号。"
		case i18n.Spanish:
			return "\n\n输出与执行要求：请高效聚焦核心目标，确保彻底完成任务。排查与分析任务请优先检索与阅读项目源代码（.go, .py, .yaml, .json, .md）与文档，避免无意义的二进制反汇编窥探；无论推理过程如何，请在获取到充分证据后直接输出清晰结构化的西班牙文报告与最终结论；不要使用不必要的表情符号。"
		case i18n.German:
			return "\n\n输出与执行要求：请高效聚焦核心目标，确保彻底完成任务。排查与分析任务请优先检索与阅读项目源代码（.go, .py, .yaml, .json, .md）与文档，避免无意义的二进制反汇编窥探；无论推理过程如何，请在获取到充分证据后直接输出清晰结构化的德文报告与最终结论；不要使用不必要的表情符号。"
		default:
			return "\n\n输出与执行要求：请高效聚焦核心目标，确保彻底完成任务。排查与分析任务请优先检索与阅读项目源代码（.go, .py, .yaml, .json, .md）与文档，避免无意义的二进制反汇编窥探；在获取到充分证据后直接输出清晰结构化的中文报告与最终结论；不要使用不必要的表情符号。"
		}
	}

	switch outputLang {
	case i18n.ChineseSimp:
		return "\n\nOutput & Execution Requirements: Focus efficiently on the core goal, ensuring thorough task completion. For investigation and analysis tasks, prioritize searching and reading project source code (.go, .py, .yaml, .json, .md) and documentation; avoid meaningless binary disassembly inspection. Regardless of reasoning language, output a clear, structured Simplified Chinese report and final conclusions directly. Do not use unnecessary emojis."
	case i18n.Japanese:
		return "\n\nOutput & Execution Requirements: Focus efficiently on the core goal, ensuring thorough task completion. For investigation and analysis tasks, prioritize searching and reading project source code (.go, .py, .yaml, .json, .md) and documentation; avoid meaningless binary disassembly inspection. Regardless of reasoning language, output a clear, structured Japanese report and final conclusions directly. Do not use unnecessary emojis."
	case i18n.Spanish:
		return "\n\nOutput & Execution Requirements: Focus efficiently on the core goal, ensuring thorough task completion. For investigation and analysis tasks, prioritize searching and reading project source code (.go, .py, .yaml, .json, .md) and documentation; avoid meaningless binary disassembly inspection. Regardless of reasoning language, output a clear, structured Spanish report and final conclusions directly. Do not use unnecessary emojis."
	case i18n.German:
		return "\n\nOutput & Execution Requirements: Focus efficiently on the core goal, ensuring thorough task completion. For investigation and analysis tasks, prioritize searching and reading project source code (.go, .py, .yaml, .json, .md) and documentation; avoid meaningless binary disassembly inspection. Regardless of reasoning language, output a clear, structured German report and final conclusions directly. Do not use unnecessary emojis."
	default:
		return "\n\nOutput & Execution Requirements: Focus efficiently on the core goal, ensuring thorough task completion. For investigation and analysis tasks, prioritize searching and reading project source code (.go, .py, .yaml, .json, .md) and documentation; avoid meaningless binary disassembly inspection. Upon gathering sufficient evidence, output a clear, structured English report and final conclusions directly. Do not use unnecessary emojis."
	}
}

// buildAgentPrompt assembles the full agent execution prompt — the memory
// file manifest (A3 selective loading) plus the task intent plus any matched
// skills (design §8.5) — and returns the skills that were actually loaded, so
// the caller can record their use. Skill matching keys off the short title,
// not the full intent, so a long instruction does not over-match on common
// words. The assembly stays under agentPromptBudget by degrading skills to
// index lines when the budget is tight.
func buildAgentPrompt(c *Core, intent, project, title, workDir string, loc ...i18n.Locale) (string, []*skills.Skill) {
	promptLang := i18n.ChineseSimp
	outputLang := i18n.ChineseSimp
	if len(loc) == 1 && loc[0] != "" {
		promptLang = loc[0]
		outputLang = loc[0]
	} else if len(loc) >= 2 {
		if loc[0] != "" {
			promptLang = loc[0]
		}
		if loc[1] != "" {
			outputLang = loc[1]
		}
	}
	rider := getAgentOutputRider(promptLang, outputLang)

	manifest := ""
	if c.memory != nil {
		if project == "" {
			if files, err := c.memory.Manifest(); err == nil {
				manifest = memory.RenderManifest(files)
			}
		} else if pm, err := c.memory.ProjectManifest(project, workDir); err == nil {
			manifest = pm
		}
	}
	// The intent, the manifest and the output rider are non-negotiable; only
	// the skills section degrades. A negative remainder (a huge intent) still
	// runs — withSkills treats it as "no room for skill bodies".
	budget := agentPromptBudget - len(intent) - len(manifest) - len(rider)
	prompt, used := withSkills(c, intent, project, title, budget, promptLang)
	if manifest != "" {
		prompt = manifest + "\n\n" + prompt
	}
	// Output style rider: the agent's final message is shown to the user
	// (often verbatim in a terminal) and may be spoken aloud by the voice
	// pipeline, so it must read as a direct answer — not a transcript of
	// the agent's exploration. Execution details stay in the task's event
	// stream (panda task <id>) for anyone who wants the full trail.
	prompt += rider
	// §4.2 Sub-MainAgent protocol hint: the marker format the run loop
	// parses. Kept terse — it rides every agent round.
	prompt += i18n.T(promptLang, "prompt.delegate.hint")
	return prompt, used
}

// withSkills prepends matched active skills to the intent via the lightweight
// index (design §8.5 progressive loading): only a matched skill's full body is
// loaded, never the whole bank. Global skills apply everywhere; project skills
// apply within their own project. budget bounds the skills section (bytes):
// a body that does not fit degrades to an index line (name + description) so
// the agent still knows the skill exists without the prompt overflowing the
// agent's window. The returned slice is the set actually loaded.
func withSkills(c *Core, intent, project, query string, budget int, loc ...i18n.Locale) (string, []*skills.Skill) {
	targetLoc := i18n.ChineseSimp
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	}
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
	availableKey := i18n.T(targetLoc, "prompt.skills.available")
	b.WriteString(availableKey + "\n")
	remaining := budget - b.Len()
	var used []*skills.Skill
	for _, e := range matched {
		sk, err := c.skills.Load(e.Scope, e.Key, e.Name)
		if err != nil || sk == nil {
			continue
		}
		used = append(used, sk)
		full := fmt.Sprintf("## %s\n%s\n", sk.Name, sk.Body)
		if len(full) <= remaining {
			b.WriteString(full)
			remaining -= len(full)
			continue
		}
		// Budget exhausted: degrade to an index line — the agent sees the
		// skill exists (and what it is for) without the full body.
		omittedMsg := i18n.Tf(targetLoc, "prompt.skills.omitted", "desc", sk.Description)
		line := fmt.Sprintf("## %s\n%s\n", sk.Name, omittedMsg)
		b.WriteString(line)
		c.logger.Warn("agent prompt budget: skill degraded to index line",
			"skill", sk.Name, "budget", budget)
	}
	if len(used) == 0 {
		return intent, nil
	}
	intentHeader := i18n.T(targetLoc, "prompt.task.intent_header")
	return b.String() + "\n" + intentHeader + "\n" + intent, used
}

// logTask appends one daily-log line recording a task outcome. The daily log is
// the warm layer the Dreaming engine (design §17.3) consolidates from, so this
// is the point where task history becomes candidate long-term memory.
func (c *Core) logTask(title string, ok bool, loc ...i18n.Locale) {
	if c.daily == nil {
		return
	}
	targetLoc := i18n.ChineseSimp
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	}
	status := i18n.T(targetLoc, "prompt.task.status.success")
	if !ok {
		status = i18n.T(targetLoc, "prompt.task.status.fail")
	}
	line := i18n.Tf(targetLoc, "prompt.task.log_format", "title", title, "status", status)
	if err := c.daily.AppendExternal(time.Now(), line); err != nil {
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
		// A stage re-routed after a decline must keep its plan identity and its
		// inputs: without them the executor cannot fetch the trees its
		// predecessors produced, and its output artifact is never adopted back —
		// the plan stalls on a stage that ran to completion as an orphan.
		PlanID:  t.PlanID,
		StageID: t.StageID,
		Inputs:  t.Inputs,
	}
	// Hop-limited consent (S2-8): a re-route is one direct dispatch, so the
	// consent on record covers exactly the receiving hop and must not walk
	// further through a forwarding sub-scheduler.
	if t.Authorized {
		payload.AuthHops = 1
	}
	// A re-routed project task needs its context as much as the first attempt did.
	c.attachProject(ctx, &payload, t.Project)
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
		}
		if _, err := c.store.CreateFromRemote(ctx, p.TaskID, p.TaskID, c.nodeID, p.AttemptID, chain); err != nil {
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
	transitionOK := true
	switch state {
	case StateDone:
		if !p.OK {
			c.logger.Warn("inconsistent task_result state", "task", p.TaskID, "state", state, "ok", p.OK)
			return
		}
		if err := c.store.CompleteFromRemote(ctx, p.TaskID, c.nodeID, p); err != nil {
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
	default:
		c.logger.Warn("unknown task_result state ignored", "task", p.TaskID, "state", state)
		return
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
		c.forwardCancelDownstream(ctx, id)
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
	const reason = "cancelled by delegator"
	if c.deliverCancel(ctx, target, taskID, reason) {
		// Delivered now: clear any copy parked from an earlier failed attempt.
		c.outboxCancelDrop(ctx, target, taskID)
		c.logger.Info("cancel forwarded downstream", "task", taskID, "to", target)
		return
	}
	// The executor is unreachable at the moment of the cancel. Park it for
	// redelivery on the next hello (S2-7) instead of relying on the lease
	// alone: the lease bounds liveness, but a live executor keeps renewing
	// it and would burn tokens on abandoned work until it finishes.
	c.logger.Warn("cancel: forward downstream", "task", taskID, "to", target)
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
		// Answer honestly instead of leaving the delegator waiting on its
		// lease timeout: a task that moved on (timed out, cancelled, already
		// re-run) cannot take the consent, and a failed result is the one
		// state every delegator path already renders.
		c.logger.Warn("resume for task not in review", "task", p.TaskID, "state", t.State)
		result := bus.TaskResultPayload{
			TaskID: t.TaskID, AttemptID: t.AttemptID, State: StateFailed, OK: false, ExitCode: 1,
			Stderr: "resume: task state is " + t.State,
			Chain:  t.Chain,
		}
		c.replyResult(ctx, env, result)
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
	go func() {
		final, result, rerr := c.ResumeApproved(runCtx, p.TaskID)
		if rerr != nil {
			result = bus.TaskResultPayload{
				TaskID: p.TaskID, AttemptID: t.AttemptID, State: StateFailed, OK: false, ExitCode: 1,
				Stderr: "resume: " + rerr.Error(),
				Chain:  t.Chain,
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
				Chain:  t.Chain,
			}
		}
		c.replyResult(context.WithoutCancel(ctx), env, result)
	}()
}

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
	if cOut != "" && cOut == pOut {
		return true
	}
	if cErr != "" && cErr == pErr && cOut == pOut {
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
	// A yield revokes grants on the named scope: drop matching locks whose
	// holder lives on this node and interrupt the local task that held them,
	// so the preempting agent's edit does not race a zombie grant. Payload
	// FromAgent is informational (the winner's identity); matching by scope
	// key, not claimed name, keeps the revoke unforgeable.
	key := negoScopeKey(p.Scope)
	c.negoMu.Lock()
	var victims []string
	for k, l := range c.nego {
		if k == key {
			victims = append(victims, l.holder)
			delete(c.nego, k)
		}
	}
	c.negoMu.Unlock()
	for _, h := range victims {
		if node, _, _ := strings.Cut(h, "|"); node == c.nodeID {
			c.negoInterruptLocal(ctx, h, key)
		} else {
			c.negoRelease(h)
		}
	}
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

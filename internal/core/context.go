// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/ctxstore"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
)

// pendingContext is the execution context saved when a task pauses in
// waiting_context to fetch its full snapshot. handleContextAck uses it to
// resume execution. source is the node the fetch was sent to — the chain
// predecessor that handed us the task (chain[0] on a direct delegation); a
// context_ack from any other node is rejected (P1-11).
//
// release frees the capacity reservation handleLocalDelegate took for this
// task. The parked row already occupies a CountActive slot (waiting_context),
// so the reservation is redundant while parked — but it must still be freed
// exactly once, either when the ack arrives (the row stays countable from
// there on) or when the fetch fails and the task dies.
type pendingContext struct {
	intent   string
	required []string
	ctxType  string
	source   string
	release  func()
	// waitHashes is non-empty when the entry parks a dtn task for pushed
	// artifacts rather than a context fetch: the input hashes still missing
	// from the pool. resume re-drives execution once every one of them
	// verifies — from a push commit, a pull, or a bundled import alike.
	waitHashes []string
	resume     func()
}

// packContext builds and stores the full context snapshot for a local-origin
// task, returning the wire hash and level. It is the MVP of pointer/summary/
// full packing (design doc §12.3):
//
//   - A caller that already packed the snapshot (ContextHash set) is trusted
//     and passed through.
//   - A file task with a known repo path is packed into a FileContext snapshot
//     and cached locally, advertised as "pointer".
//   - Everything else is "summary": the intent/spec already on the wire is the
//     whole context, and no snapshot transfer occurs.
func (c *Core) packContext(ctx context.Context, in TaskInput) (hash, level string, err error) {
	if in.ContextHash != "" {
		lvl := in.ContextLevel
		if lvl == "" {
			lvl = "pointer"
		}
		return in.ContextHash, lvl, nil
	}
	if in.ContextType == "file" && in.RepoPath != "" {
		fc := commander.FileContext{Type: "file", Repo: in.RepoPath}
		data, err := json.Marshal(fc)
		if err != nil {
			return "", "", fmt.Errorf("marshal file context: %w", err)
		}
		snap := ctxstore.Snapshot{Type: "file", Data: data}
		hash, blob, err := ctxstore.Pack(snap)
		if err != nil {
			return "", "", err
		}
		if err := c.ctx.Put(ctx, hash, "file", blob, nil); err != nil {
			return "", "", err
		}
		return hash, "pointer", nil
	}

	// Normal task workspace packaging: if repo, workdir or project is present,
	// pack workspace context so the remote node does not run blind without project/repo baseline.
	repo := in.RepoPath
	if repo == "" && in.WorkDir != "" {
		repo = in.WorkDir
	}
	if repo == "" && c.workDir != "" && c.workDir != "." {
		repo = c.workDir
	}
	if repo != "" || in.Project != "" {
		wc := map[string]string{
			"repo":    repo,
			"project": in.Project,
			"intent":  in.Intent,
		}
		data, err := json.Marshal(wc)
		if err == nil {
			snap := ctxstore.Snapshot{Type: "workspace", Data: data}
			if h, blob, err := ctxstore.Pack(snap); err == nil {
				if err := c.ctx.Put(ctx, h, "workspace", blob, nil); err == nil {
					return h, "pointer", nil
				}
			}
		}
	}

	return "", "summary", nil
}

// chainPredecessor names the node one hop toward the origin in this task's
// delegation chain — the peer this node received the task from, and therefore
// one it held a live link to when the task arrived. It is the natural relay
// target for data-plane pulls whose nominal source (the origin) is
// unreachable on a partial mesh: Mac→Pi through Windows means the Pi cannot
// dial the Mac, but it can dial the node that just handed it the task, and
// every hop up the chain can dial the next. "" when this node is the origin,
// the row is gone, or the node is not on the chain.
func (c *Core) chainPredecessor(ctx context.Context, taskID string) string {
	t, err := c.store.Get(ctx, taskID)
	if err != nil {
		return ""
	}
	for i, n := range t.Chain {
		if n == c.nodeID || scheduler.IsSelfRow(n, c.nodeID) {
			if i == 0 {
				return ""
			}
			return t.Chain[i-1]
		}
	}
	return ""
}

// sendContextFetch asks the source node for the full snapshot. On a send
// failure the task is failed rather than left hanging in waiting_context.
func (c *Core) sendContextFetch(ctx context.Context, source, taskID, hash, ctxType string) {
	if err := c.emitContextFetch(source, taskID, hash, ctxType); err != nil {
		c.logger.Warn("context fetch", "task", taskID, "source", source, "err", err)
		c.failPendingContext(ctx, taskID, "context source unreachable: "+err.Error())
	}
}

// emitContextFetch is the bare wire half of sendContextFetch: one
// context_fetch envelope to source, error returned rather than folded into a
// task failure. The relay path needs the error without a parked task to fail.
func (c *Core) emitContextFetch(source, taskID, hash, ctxType string) error {
	msgID, err := newUUID()
	if err != nil {
		return fmt.Errorf("mint message id: %w", err)
	}
	env, err := bus.NewEnvelope(bus.MsgContextFetch, c.nodeID, msgID, bus.ContextFetchPayload{
		TaskID: taskID, Hash: hash, ContextType: ctxType,
	})
	if err != nil {
		return fmt.Errorf("build context_fetch: %w", err)
	}
	env.To = source
	return c.sendTo(source, env)
}

// ctxServeEntry is one relayed context_fetch: this node did not hold the
// snapshot, so it asked its own chain predecessor (source) and owes every
// waiter in requesters the answer. source is the only peer whose ack may
// close the entry — the same P1-11 rule pendingContext.source enforces for
// an executor, applied to the relay role.
type ctxServeEntry struct {
	mu         sync.Mutex
	taskID     string
	hash       string
	source     string
	ctxType    string
	requesters map[string]bool
	// closed marks an entry that already answered its waiters (upstream
	// ack, emit failure, or expiry). A fetch that registers on a closed
	// entry must not wait — nobody will ever fan an answer out to it.
	closed bool
}

// ctxServeKey names a relayed snapshot. The hash is already unique; the task
// id scopes the entry to one delegation so two plans pulling the same
// snapshot never share a waiter set.
func ctxServeKey(taskID, hash string) string { return taskID + "|" + hash }

// relayContextFetch answers a context_fetch this node cannot serve by asking
// its own chain predecessor — the transitive hop that makes pointer-context
// tasks executable on a partial mesh. The executor cannot reach the origin,
// but it can reach the node that just handed it the task, and that node can
// reach its own predecessor the same way. Returns false when there is no
// relay to attempt (this node IS the origin, no row, or the emit failed),
// leaving the honest miss reply to the caller.
func (c *Core) relayContextFetch(ctx context.Context, env bus.Envelope, p bus.ContextFetchPayload) bool {
	pred := c.chainPredecessor(ctx, p.TaskID)
	if pred == "" || pred == c.nodeID || env.From == "" || env.From == pred {
		return false
	}
	key := ctxServeKey(p.TaskID, p.Hash)
	for attempt := 0; attempt < 2; attempt++ {
		v, loaded := c.pendingCtxServe.LoadOrStore(key, &ctxServeEntry{
			taskID: p.TaskID, hash: p.Hash, source: pred, ctxType: p.ContextType,
			requesters: map[string]bool{},
		})
		sv := v.(*ctxServeEntry)
		sv.mu.Lock()
		if sv.closed {
			// The entry answered before this fetch arrived. Replacing it
			// and retrying gives a transient upstream drop a second chance
			// — and when the close cached the snapshot, the retry is
			// served locally before it ever reaches this branch. A CAS
			// loser simply reloads: a concurrent fetch already swapped in
			// the fresh entry this attempt needs.
			sv.mu.Unlock()
			c.pendingCtxServe.CompareAndDelete(key, sv)
			continue
		}
		sv.requesters[env.From] = true
		sv.mu.Unlock()
		if loaded {
			// A fetch for this snapshot is already in flight upstream; the
			// waiter was registered and the answer fans out to it too.
			return true
		}
		if err := c.emitContextFetch(pred, p.TaskID, p.Hash, p.ContextType); err != nil {
			c.pendingCtxServe.Delete(key)
			c.logger.Warn("relay context fetch", "task", p.TaskID, "source", pred, "err", err)
			c.answerContextServe(sv, bus.ContextAckPayload{TaskID: p.TaskID, Hash: p.Hash, OK: false})
			return true
		}
		// Bound the serve: an upstream that never answers must not leave
		// the waiters parked forever — the requester's own
		// waiting-context lease is the backstop, and the expiry answers
		// the same honest miss.
		time.AfterFunc(2*artifactChunkTimeout, func() {
			if c.pendingCtxServe.CompareAndDelete(key, sv) {
				c.answerContextServe(sv, bus.ContextAckPayload{TaskID: p.TaskID, Hash: p.Hash, OK: false})
			}
		})
		return true
	}
	// Both attempts raced closed entries: nobody owns this requester's
	// wait now, so answer the honest miss directly instead of parking it
	// on an entry that will never fan out.
	_ = c.reply(ctx, env, bus.MsgContextAck, bus.ContextAckPayload{
		TaskID: p.TaskID, Hash: p.Hash, OK: false,
	})
	return true
}

// relayContextAck closes a relayed context fetch: the ack from our
// predecessor either carries the snapshot — verified here before it is cached
// or passed downstream, because a relay must not launder bytes that do not
// hash to their name (M1) — or a miss; either way every waiter gets the same
// answer. An ack for a task this node is executing never reaches here (the
// executor branch claims it first).
func (c *Core) relayContextAck(ctx context.Context, env bus.Envelope, p bus.ContextAckPayload) {
	key := ctxServeKey(p.TaskID, p.Hash)
	v, ok := c.pendingCtxServe.Load(key)
	if !ok {
		c.logger.Debug("context_ack for unknown task", "task", p.TaskID)
		return
	}
	sv := v.(*ctxServeEntry)
	if env.From != sv.source {
		c.logger.Warn("relayed context_ack from non-source ignored", "task", p.TaskID,
			"from", env.From, "source", sv.source)
		return
	}
	if !c.pendingCtxServe.CompareAndDelete(key, v) {
		c.logger.Debug("relayed context_ack superseded by a concurrent ack", "task", p.TaskID)
		return
	}
	ack := p
	if p.OK {
		if ctxstore.Hash(p.Data) != p.Hash {
			c.logger.Warn("relayed context hash mismatch", "task", p.TaskID)
			ack = bus.ContextAckPayload{TaskID: p.TaskID, Hash: p.Hash, OK: false}
		} else if err := c.ctx.Put(ctx, p.Hash, sv.ctxType, p.Data, p.Refs); err != nil {
			// Caching is an optimization for the next waiter, not a
			// correctness step — the verified bytes still travel.
			c.logger.Warn("cache relayed context", "task", p.TaskID, "err", err)
		}
	}
	c.answerContextServe(sv, ack)
}

// answerContextServe fans one ack out to every waiter registered on a
// relayed fetch and empties the set, so a late duplicate answer has nobody
// left to reach.
func (c *Core) answerContextServe(sv *ctxServeEntry, ack bus.ContextAckPayload) {
	sv.mu.Lock()
	sv.closed = true
	waiters := make([]string, 0, len(sv.requesters))
	for r := range sv.requesters {
		waiters = append(waiters, r)
	}
	sv.requesters = map[string]bool{}
	sv.mu.Unlock()
	for _, r := range waiters {
		msgID, err := newUUID()
		if err != nil {
			continue
		}
		env, err := bus.NewEnvelope(bus.MsgContextAck, c.nodeID, msgID, ack)
		if err != nil {
			continue
		}
		env.To = r
		if err := c.sendTo(r, env); err != nil {
			c.logger.Warn("relay context ack", "task", ack.TaskID, "to", r, "err", err)
		}
	}
}

// failPendingContext removes a pending entry and fails the task.
func (c *Core) failPendingContext(ctx context.Context, taskID, reason string) {
	if c.dropPendingContext(taskID) {
		if err := c.store.ForceFail(ctx, taskID, reason); err != nil {
			c.logger.Warn("fail pending context task", "task", taskID, "err", err)
		}
	}
}

// dropPendingContext removes a parked entry and frees the capacity reservation
// it was holding, reporting whether an entry existed. Every site that drops an
// entry without resuming its run — a failed fetch, a cancel, an expiry sweep —
// must go through here: a bare Delete strands the reservation and slowly eats
// the node's MaxConcurrent budget (the row never reaches a countable state, so
// nothing else ever releases it). The release is sync.Once-guarded, so firing
// it here and again from a racing resume is still exactly-once.
func (c *Core) dropPendingContext(taskID string) bool {
	v, ok := c.pendingCtx.LoadAndDelete(taskID)
	if !ok {
		return false
	}
	if pc, ok := v.(*pendingContext); ok && pc.release != nil {
		pc.release()
	}
	return true
}

// handleContextFetch serves a peer's request for a full snapshot. If this node
// has it, it replies with the data; otherwise a non-OK ack so the executor can
// fail fast rather than guess.
func (c *Core) handleContextFetch(ctx context.Context, env bus.Envelope) {
	var p bus.ContextFetchPayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad context_fetch", "err", err)
		return
	}
	// Authorization after authentication (review P1-3): a context snapshot is
	// source code, specs and conversation history, so it must not be enumerable
	// by any peer that learned a hash from a plaintext delegate. Only a node
	// actually part of this task's delegation — owner, chain member, or the
	// dispatch target — may pull it; the same criteria that guard artifact
	// fetches. Unknown task or unknown peer fails closed with a non-OK ack.
	if !c.artifactPeerAuthorized(ctx, p.TaskID, p.Hash, env.From) {
		c.logger.Warn("context_fetch from unauthorized peer", "task", p.TaskID,
			"hash", p.Hash, "from", env.From)
		_ = c.reply(ctx, env, bus.MsgContextAck, bus.ContextAckPayload{
			TaskID: p.TaskID, Hash: p.Hash, OK: false,
		})
		return
	}
	// Trust ceiling on the pull channel (experimental): a "summary" send
	// ceiling declared the peer may not hold snapshots — letting it fetch
	// the same bytes would make the ceiling an audit note, not a bound.
	// mesh resolves full → this branch never fires on a pre-feature fleet.
	if pol := c.sendPolicyFor(c.peerTrustOf(ctx, env.From)); !pol.MayHoldContext() {
		c.logger.Warn("context_fetch refused by trust ceiling", "task", p.TaskID,
			"from", env.From)
		_ = c.reply(ctx, env, bus.MsgContextAck, bus.ContextAckPayload{
			TaskID: p.TaskID, Hash: p.Hash, OK: false,
		})
		return
	}
	e, ok, err := c.ctx.Get(ctx, p.Hash)
	if err != nil || !ok {
		// Mid-chain relay: the requester asked us because we are the node
		// that handed it the task, and the snapshot lives at the origin —
		// one or more hops up OUR chain, all of them reachable one at a
		// time. Relay instead of answering a miss for a snapshot that
		// exists.
		if c.relayContextFetch(ctx, env, p) {
			return
		}
		c.logger.Debug("context miss on fetch", "hash", p.Hash, "err", err)
		_ = c.reply(ctx, env, bus.MsgContextAck, bus.ContextAckPayload{
			TaskID: p.TaskID, Hash: p.Hash, OK: false,
		})
		return
	}
	_ = c.reply(ctx, env, bus.MsgContextAck, bus.ContextAckPayload{
		TaskID: p.TaskID, Hash: p.Hash, OK: true, Data: e.Data, Refs: e.Refs,
	})
}

// handleContextAck processes a context_fetch response. It verifies the snapshot
// hash, caches it, and resumes the paused task.
func (c *Core) handleContextAck(ctx context.Context, env bus.Envelope) {
	var p bus.ContextAckPayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad context_ack", "err", err)
		return
	}
	v, ok := c.pendingCtx.Load(p.TaskID)
	if !ok {
		// Not our task — but this ack may close a relayed fetch we opened
		// on a downstream executor's behalf.
		c.relayContextAck(ctx, env, p)
		return
	}
	pc, ok := v.(*pendingContext)
	if !ok {
		c.logger.Warn("bad pending context entry", "task", p.TaskID)
		return
	}
	if pc.source != "" && env.From != pc.source {
		// Only the node we actually asked may answer the fetch; otherwise any
		// authenticated peer could reply OK:false and force-fail an arbitrary
		// parked task, or inject attacker-controlled context data (P1-11).
		c.logger.Warn("context_ack from non-source ignored", "task", p.TaskID,
			"from", env.From, "source", pc.source)
		return
	}
	// The entry must be claimed atomically: a duplicated ack (re-sent after a
	// connection flap) racing this handler would otherwise let two goroutines
	// both pass the source check and both resume the same task (M-double-run).
	if !c.pendingCtx.CompareAndDelete(p.TaskID, v) {
		c.logger.Debug("context_ack superseded by a concurrent ack", "task", p.TaskID)
		return
	}
	release := pc.release
	if release == nil {
		release = func() {}
	}
	if !p.OK {
		c.logger.Warn("context fetch declined", "task", p.TaskID)
		release()
		if ferr := c.store.ForceFail(ctx, p.TaskID, "context unavailable"); ferr != nil {
			c.logger.Warn("fail task on declined context", "task", p.TaskID, "err", ferr)
		}
		return
	}
	if ctxstore.Hash(p.Data) != p.Hash {
		c.logger.Warn("context hash mismatch", "task", p.TaskID)
		release()
		if ferr := c.store.ForceFail(ctx, p.TaskID, "context hash mismatch"); ferr != nil {
			c.logger.Warn("fail task on context hash mismatch", "task", p.TaskID, "err", ferr)
		}
		return
	}
	if err := c.ctx.Put(ctx, p.Hash, pc.ctxType, p.Data, p.Refs); err != nil {
		c.logger.Warn("store fetched context", "task", p.TaskID, "err", err)
		release()
		if ferr := c.store.ForceFail(ctx, p.TaskID, "store context: "+err.Error()); ferr != nil {
			c.logger.Warn("fail task on context store error", "task", p.TaskID, "err", ferr)
		}
		return
	}
	// The parked row has been occupying a CountActive slot since
	// SetWaitingContext, so the reservation is redundant now — free it before
	// the resume, not inside run(): in the gap between this ack and Resume the
	// slot would otherwise be double-counted (row + reservation) against
	// MaxConcurrent, shrinking real capacity by one for every parked fetch.
	release()
	// Resume the paused task. Its result (or decline) must reach the parent so
	// the root scheduler is unblocked, exactly as the synchronous execute path
	// reports back via reply(). The execution ctx is detached from the handler's
	// ctx: that ctx dies with the peer's websocket connection (the ack reader
	// loop), and cancelling it mid-run would kill the resumed agent subprocess
	// the moment the sender closes or drops the link — an executor-side failure
	// no caller asked for. WithoutCancel keeps the run alive; explicit cancels
	// still reach it via the running map's CancelFunc.
	runCtx := context.WithoutCancel(ctx)
	go func() {
		result, err := c.run(runCtx, p.TaskID, pc.intent, pc.required, nil)
		if err != nil {
			if errors.Is(err, ErrCancelled) {
				c.logger.Info("task cancelled during execution", "task", p.TaskID)
				return
			}
			c.logger.Warn("run fetched-context task", "task", p.TaskID, "err", err)
			if t, gerr := c.store.Get(runCtx, p.TaskID); gerr == nil {
				c.relayToParent(runCtx, bus.MsgTaskDecline, t.Chain, bus.TaskDeclinePayload{
					TaskID: p.TaskID, Reason: err.Error(),
				})
			}
			// Same as the synchronous path: the local row must not linger in a
			// pre-terminal state for Recover to resurrect as an orphan (P1-9).
			c.terminalizeDeclined(runCtx, p.TaskID, err.Error())
			return
		}
		if t, gerr := c.store.Get(runCtx, p.TaskID); gerr == nil {
			c.relayToParent(runCtx, bus.MsgTaskResult, t.Chain, result)
		}
	}()
}

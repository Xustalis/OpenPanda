package core

// Chunked proactive artifact delivery (whitepaper §8.3 fat-push, v2).
//
// The first-generation fat bundle inlined artifacts into the delegate
// envelope — capped at 2 MiB by the frame limit, which put the doc's "push a
// model pack alongside the task" scenario out of reach. This file is the
// data-plane answer: the delegating node enqueues one durable row per
// (peer, hash) in artifact_push_outbox and streams the archive in
// bus.ArtifactChunkBytes frames on the QoS data lane, while the receiver
// stages the bytes on disk under .staging/ and reports its contiguous
// waterline back.
//
// The pieces fit the store-and-forward model the rest of the DTN stack
// already uses:
//
//   - Custody, not fire-and-forget. A row retires only on the receiver's
//     done verdict, and progress is measured by the receiver's reported
//     acked_through — bytes that left the socket but were never staged are
//     re-sent, so a crash mid-transfer resumes instead of leaving a gap.
//   - Resume from the receiver's truth. On every flush the sender starts at
//     acked_through, and the receiver's status replies update the live
//     waterline mid-stream; a reconnect continues where the receiver's disk
//     says it stopped, not where the sender hoped it got to.
//   - Chained custody. A relay that forwards a dtn task before its own
//     staging completes re-pushes each artifact to the task's dispatch
//     target the moment the commit lands (cascadePushToDispatchTarget), so
//     a bundle's payload chases the delegate hop by hop.
//   - Bounded wait. A dtn task whose inputs have not all landed parks in
//     waiting_context until its absolute deadline — the same bound the
//     bundle itself lives under — rather than failing the moment a pull
//     finds nothing online.

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Xustalis/OpenPanda/internal/artifact"
	"github.com/Xustalis/OpenPanda/internal/bus"
)

// stagedPushMaxAge is how long a receiver keeps a partial push before
// reclaiming the disk. It matches defaultDTNTTL: a staged copy has the same
// lifetime as the bundle that commissioned it, and a transfer still landing
// chunks refreshes its sidecar mtime, so only genuinely dead pushes age out.
const stagedPushMaxAge = defaultDTNTTL

// artifactPushEnqueue records a push obligation for (peer, hash). A repeat
// enqueue keeps the existing waterlines: custody is cumulative, so
// re-delegating the same artifact to the same peer resumes the transfer
// rather than restarting it.
func (c *Core) artifactPushEnqueue(ctx context.Context, peer, taskID, hash string, ttl int64) {
	if c.db == nil || c.artifacts == nil || peer == "" || hash == "" {
		return
	}
	size, ok := c.artifacts.Has(hash)
	if !ok {
		// We can only push what we hold; a hash this node lacks stays a pull
		// fallback for whoever eventually has it.
		return
	}
	if _, err := c.db.ExecContext(ctx,
		`INSERT INTO artifact_push_outbox (peer, hash, task_id, total, sent_through, acked_through, ttl, created_at)
		 VALUES (?, ?, ?, ?, 0, 0, ?, ?)
		 ON CONFLICT(peer, hash) DO UPDATE SET task_id=excluded.task_id, total=excluded.total, ttl=excluded.ttl`,
		peer, hash, taskID, size, ttl, time.Now().Unix()); err != nil {
		c.logger.Warn("artifact push enqueue", "peer", peer, "hash", hash, "err", err)
	}
}

// pushWaterline is the session-local progress for one (peer, hash) push —
// the receiver's latest reported contiguous bytes, ahead of what the last
// sweep persisted. It exists because status replies can outpace the SQL
// writes mid-stream, and the stream loop should skip bytes the receiver
// already acknowledged instead of resending them on a hiccup.
func (c *Core) pushWaterline(peer, hash string) int64 {
	v, ok := c.pushAck.Load(peer + "|" + hash)
	if !ok {
		return 0
	}
	if n, ok := v.(*atomic.Int64); ok {
		return n.Load()
	}
	return 0
}

// bumpPushWaterline raises the live waterline (monotonic — a stale or
// reordered status must not drag the stream backwards).
func (c *Core) bumpPushWaterline(peer, hash string, through int64) {
	key := peer + "|" + hash
	v, _ := c.pushAck.LoadOrStore(key, &atomic.Int64{})
	if n, ok := v.(*atomic.Int64); ok {
		for {
			cur := n.Load()
			if through <= cur || n.CompareAndSwap(cur, through) {
				return
			}
		}
	}
}

// streamArtifactPushes drains this node's push custody toward peer. It runs
// inside outboxFlush (hello-triggered or sweep-triggered), which already
// holds the per-peer claim — so at most one stream per peer exists and the
// chunks stay ordered on the wire.
//
// Per row: TTL expiry mirrors the parked-bundle rule (dead is dead — the
// task's own row is swept by the same deadline), a hash we no longer hold is
// abandoned (the pull path remains the receiver's fallback), and the stream
// starts at max(acked_through, live waterline). Sent bytes persist as
// sent_through — the next flush re-reads them as candidates but the
// receiver's ack is what actually stops retransmission.
func (c *Core) streamArtifactPushes(ctx context.Context, peer string) {
	if c.db == nil || c.artifacts == nil {
		return
	}
	rows, err := c.db.QueryContext(ctx,
		`SELECT hash, task_id, total, sent_through, acked_through, ttl
		 FROM artifact_push_outbox WHERE peer = ?`, peer)
	if err != nil {
		c.logger.Warn("push: query outbox", "peer", peer, "err", err)
		return
	}
	type pushRow struct {
		hash, taskID            string
		total, sent, acked, ttl int64
	}
	var pushes []pushRow
	for rows.Next() {
		var r pushRow
		if err := rows.Scan(&r.hash, &r.taskID, &r.total, &r.sent, &r.acked, &r.ttl); err != nil {
			rows.Close()
			c.logger.Warn("push: scan", "peer", peer, "err", err)
			return
		}
		pushes = append(pushes, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		c.logger.Warn("push: rows", "peer", peer, "err", err)
		return
	}
	buf := make([]byte, bus.ArtifactChunkBytes)
	for _, r := range pushes {
		// §8.2 TTL: the bundle is dead, so its payload is dead weight — stop
		// spending contact-window bandwidth on it.
		if r.ttl > 0 && time.Now().Unix() > r.ttl {
			c.pushRowDrop(ctx, peer, r.hash)
			c.logger.Info("push: custody past TTL, dropping", "hash", r.hash, "peer", peer)
			continue
		}
		size, ok := c.artifacts.Has(r.hash)
		if !ok || size != r.total {
			// The pool no longer holds (or disagrees about) what we promised:
			// drop the row rather than stream corrupt bytes the receiver's
			// hash check will reject anyway.
			c.pushRowDrop(ctx, peer, r.hash)
			continue
		}
		off := r.acked
		if live := c.pushWaterline(peer, r.hash); live > off {
			off = live
		}
		if off >= r.total {
			// Every byte is acknowledged but the done verdict never arrived
			// (lost over a flap, or the receiver committed while this node
			// was offline). A zero-length probe at the waterline makes the
			// receiver re-answer done — it recognizes the artifact it already
			// holds — so custody retires instead of aging out at TTL.
			msgID, err := newUUID()
			if err != nil {
				return
			}
			env, err := bus.NewEnvelope(bus.MsgArtifactPush, c.nodeID, msgID, bus.ArtifactPushPayload{
				TaskID: r.taskID, Hash: r.hash, Offset: off, Total: r.total,
			})
			if err != nil {
				return
			}
			env.To = peer
			if err := c.sendTo(peer, env); err != nil {
				return
			}
			continue
		}
		// The loop's exit condition is the receiver's waterline, not our send
		// counter: bytes sent below or ahead of the receiver's frontier are
		// dropped as dup/gap, so "we transmitted it" and "they hold it" are
		// different facts. When our sends reach total while the waterline
		// lags, the status replies may simply still be in flight — wait a
		// bounded moment for the frontier to advance before rewinding to it
		// and replaying the tail (the receiver dedups whatever did land). A
		// frontier that stays frozen across the stall budget means the
		// receiver is silent or the bytes never arrived; the stream leaves
		// the rest to the next flush rather than pinning the window open.
		stalls := 0
		for {
			live := c.pushWaterline(peer, r.hash)
			if live >= r.total {
				break // receiver holds every byte; the done verdict retires the row
			}
			if live > off {
				off = live
			}
			if off >= r.total {
				if c.waitPushAdvance(peer, r.hash, live) {
					stalls = 0 // receiver alive and reporting: keep going
					continue
				}
				stalls++
				if stalls >= pushStallBudget {
					break
				}
				off = live // replay from the last confirmed frontier
				continue
			}
			n, _, err := c.artifacts.ReadAt(r.hash, off, buf)
			if err != nil || n <= 0 {
				c.logger.Warn("push: read chunk", "hash", r.hash, "off", off, "err", err)
				break
			}
			msgID, err := newUUID()
			if err != nil {
				return
			}
			env, err := bus.NewEnvelope(bus.MsgArtifactPush, c.nodeID, msgID, bus.ArtifactPushPayload{
				TaskID: r.taskID, Hash: r.hash, Offset: off, Data: buf[:n], Total: r.total,
			})
			if err != nil {
				return
			}
			env.To = peer
			if err := c.sendTo(peer, env); err != nil {
				// Link died mid-stream. sent_through keeps the last durable
				// position; the receiver's ack — not our send log — decides
				// what the next flush actually re-sends.
				c.logger.Info("push: send failed, custody retained", "hash", r.hash, "peer", peer, "off", off, "err", err)
				return
			}
			off += int64(n)
			if _, err := c.db.ExecContext(ctx,
				`UPDATE artifact_push_outbox SET sent_through = ? WHERE peer = ? AND hash = ?`,
				off, peer, r.hash); err != nil {
				c.logger.Warn("push: persist sent", "peer", peer, "hash", r.hash, "err", err)
			}
		}
	}
}

// waitPushAdvance polls the session waterline until it moves past have or
// the grace expires. Status replies ride the control lane and normally land
// within an RTT, so a brief wait separates "ack still in flight" from
// "receiver never got the bytes" — the distinction between continuing and
// replaying.
func (c *Core) waitPushAdvance(peer, hash string, have int64) bool {
	deadline := time.Now().Add(pushStallGrace)
	for time.Now().Before(deadline) {
		if c.pushWaterline(peer, hash) > have {
			return true
		}
		time.Sleep(pushStallPoll)
	}
	return c.pushWaterline(peer, hash) > have
}

const (
	pushStallGrace  = 300 * time.Millisecond
	pushStallPoll   = 25 * time.Millisecond
	pushStallBudget = 4
)

// pushRowDrop retires one custody record and its live waterline.
func (c *Core) pushRowDrop(ctx context.Context, peer, hash string) {
	if _, err := c.db.ExecContext(ctx,
		`DELETE FROM artifact_push_outbox WHERE peer = ? AND hash = ?`, peer, hash); err != nil {
		c.logger.Warn("push: drop row", "peer", peer, "hash", hash, "err", err)
	}
	c.pushAck.Delete(peer + "|" + hash)
}

// handleArtifactPush lands one pushed chunk on the receiver's staging area.
// The reply is always a status carrying the contiguous waterline — for a
// duplicate, a gap, and a refusal alike — so the sender always learns the
// authoritative resume point instead of guessing at silent drops.
func (c *Core) handleArtifactPush(ctx context.Context, env bus.Envelope) {
	var p bus.ArtifactPushPayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad artifact_push", "err", err)
		return
	}
	status := func(through int64) {
		if err := c.reply(ctx, env, bus.MsgArtifactPushStatus, bus.ArtifactPushStatusPayload{
			TaskID: p.TaskID, Hash: p.Hash, ReceivedThrough: through,
		}); err != nil {
			c.logger.Debug("push status reply", "task", p.TaskID, "hash", p.Hash, "err", err)
		}
	}
	done := func(ok bool, reason string) {
		if err := c.reply(ctx, env, bus.MsgArtifactPushDone, bus.ArtifactPushDonePayload{
			TaskID: p.TaskID, Hash: p.Hash, OK: ok, Reason: reason,
		}); err != nil {
			c.logger.Debug("push done reply", "task", p.TaskID, "hash", p.Hash, "err", err)
		}
	}
	if c.artifacts == nil {
		done(false, "no artifact pool")
		return
	}
	// Same authorization rule as the pull side: the hash must legitimately
	// belong to this task and the sender must be a participant. A push from a
	// stranger is refused with a status — replying keeps a later legitimate
	// retry alive while telling a confused sender the receiver holds nothing.
	if !c.artifactPeerAuthorized(ctx, p.TaskID, p.Hash, env.From) {
		c.logger.Warn("artifact_push from non-participant", "task", p.TaskID, "hash", p.Hash, "from", env.From)
		// Report 0, not the real waterline: a stranger must not learn that a
		// transfer for this hash is staged here or how far along it is.
		status(0)
		return
	}
	// Already in the pool: answer done so the sender retires its custody row
	// — this is also how a peer learns a re-pushed artifact never needed
	// bytes on the wire.
	if _, ok := c.artifacts.Has(p.Hash); ok {
		done(true, "")
		return
	}
	through, err := c.artifacts.StageChunk(p.Hash, p.Offset, p.Data, p.Total)
	if err != nil {
		c.logger.Warn("stage pushed chunk", "task", p.TaskID, "hash", p.Hash, "off", p.Offset, "err", err)
		// An oversized or unholdable push can never complete — the task
		// parked for it would sit out its deadline for an artifact that can
		// never land. Fail it now instead. ENOSPC is the same verdict the
		// disk itself returns when the advertisement was honest but the
		// volume filled mid-transfer.
		if errors.Is(err, artifact.ErrTooLarge) || errors.Is(err, artifact.ErrNoSpace) ||
			errors.Is(err, artifact.ErrInvalid) || errors.Is(err, syscall.ENOSPC) {
			c.failPendingContext(ctx, p.TaskID, "pushed artifact cannot be stored: "+err.Error())
		}
		done(false, err.Error())
		return
	}
	status(through)
	if through < p.Total {
		return
	}
	m, err := c.artifacts.CommitStaged(p.Hash)
	if err != nil {
		c.logger.Warn("commit staged artifact", "task", p.TaskID, "hash", p.Hash, "err", err)
		// A corrupt staged copy cannot be trusted; drop it so the sender's
		// next push restarts clean rather than appending to poison.
		_ = c.artifacts.DropStaged(p.Hash)
		done(false, err.Error())
		return
	}
	if manifestJSON, merr := json.Marshal(m); merr != nil {
		c.logger.Warn("marshal pushed manifest", "hash", m.Hash, "err", merr)
	} else if err := c.store.RecordArtifact(ctx, m.Hash, m.Size, p.TaskID, string(manifestJSON)); err != nil {
		c.logger.Warn("index pushed artifact", "hash", m.Hash, "err", err)
	}
	c.EvTrace(ctx, p.TaskID, EvArtifactTransfer, map[string]any{
		"from_node":  env.From,
		"to_node":    c.nodeID,
		"hash":       m.Hash,
		"ok":         true,
		"size_bytes": m.Size,
		"mode":       "push",
	})
	done(true, "")
	// The artifact landing may unblock two kinds of downstream work: a task
	// parked on this node waiting for its inputs, and a task this node
	// already forwarded whose own push to the next hop was waiting for these
	// same bytes (chained custody — §8.3).
	c.wakeArtifactWaiters(ctx, m.Hash)
	c.cascadePushToDispatchTarget(ctx, m.Hash)
}

// handleArtifactPushStatus records the receiver's contiguous waterline on
// the sender's custody row. The row stays until the receiver's done verdict:
// "all bytes arrived" and "the archive verified and entered the pool" are
// different facts, and only the second releases custody.
func (c *Core) handleArtifactPushStatus(ctx context.Context, env bus.Envelope) {
	var p bus.ArtifactPushStatusPayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad artifact_push_status", "err", err)
		return
	}
	if c.db == nil {
		return
	}
	c.bumpPushWaterline(env.From, p.Hash, p.ReceivedThrough)
	if _, err := c.db.ExecContext(ctx,
		`UPDATE artifact_push_outbox SET acked_through = MAX(acked_through, ?) WHERE peer = ? AND hash = ?`,
		p.ReceivedThrough, env.From, p.Hash); err != nil {
		c.logger.Warn("push: persist ack", "peer", env.From, "hash", p.Hash, "err", err)
	}
}

// handleArtifactPushDone ends a push: the receiver verified the archive into
// its pool (ok) or refuses it outright. Either verdict retires the custody
// row — resending a rejected artifact only burns bandwidth for the same
// answer.
func (c *Core) handleArtifactPushDone(ctx context.Context, env bus.Envelope) {
	var p bus.ArtifactPushDonePayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad artifact_push_done", "err", err)
		return
	}
	if !p.OK {
		c.logger.Warn("push rejected by receiver", "task", p.TaskID, "hash", p.Hash,
			"peer", env.From, "reason", p.Reason)
	}
	c.pushRowDrop(ctx, env.From, p.Hash)
}

// missingPushInputs lists the task's declared input hashes this node does
// not hold — the set a push from the delegator could still deliver.
func (c *Core) missingPushInputs(p bus.TaskDelegatePayload) []string {
	if c.artifacts == nil {
		return nil
	}
	var missing []string
	for _, in := range p.Inputs {
		if in.Hash == "" {
			continue
		}
		if _, ok := c.artifacts.Has(in.Hash); !ok {
			missing = append(missing, in.Hash)
		}
	}
	return missing
}

// parkForArtifactPush suspends a dtn task whose inputs have not all arrived.
// The bundle model promises the payload follows the task: failing the moment
// the delegate lands before its chunks would defeat the push entirely, while
// running without inputs is the failure the stage wiring exists to prevent.
// The task waits — countable in waiting_context, bounded by the bundle's own
// deadline — and wakes the moment every missing hash is verified in the pool.
func (c *Core) parkForArtifactPush(ctx context.Context, env bus.Envelope, taskID string, p bus.TaskDelegatePayload, required []string, missing []string, release func()) {
	if err := c.prepare(ctx, taskID); err != nil {
		c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{TaskID: taskID, Reason: err.Error()})
		c.terminalizeDeclined(ctx, taskID, err.Error())
		release()
		return
	}
	if err := c.store.SetWaitingContext(ctx, taskID, c.nodeID); err != nil {
		c.reply(ctx, env, bus.MsgTaskDecline, bus.TaskDeclinePayload{TaskID: taskID, Reason: err.Error()})
		c.terminalizeDeclined(ctx, taskID, err.Error())
		release()
		return
	}
	// A parked dtn task lives by the bundle's absolute deadline, not the
	// live-track lease that bounds a context fetch — contact windows are
	// measured in hours. Without an explicit deadline the default TTL bounds
	// the wait so a push that never comes cannot park a slot forever.
	timeoutMS := p.TimeoutMS
	if p.DeadlineUnix > 0 {
		timeoutMS = time.Until(time.Unix(p.DeadlineUnix, 0)).Milliseconds()
	}
	if timeoutMS <= 0 {
		timeoutMS = defaultDTNTTL.Milliseconds()
	}
	if err := c.store.SetLease(ctx, taskID, timeoutMS); err != nil {
		c.logger.Warn("lease artifact-push wait", "task", taskID, "err", err)
	}
	pc := &pendingContext{
		intent: p.Intent, required: required, ctxType: p.ContextType,
		source: env.From, release: release, waitHashes: missing,
	}
	runCtx := context.WithoutCancel(ctx)
	pc.resume = func() { c.resumePushedTask(runCtx, taskID, p, required) }
	c.pendingCtx.Store(taskID, pc)
	c.EvTrace(ctx, taskID, EvArtifactTransfer, map[string]any{
		"from_node": env.From,
		"to_node":   c.nodeID,
		"mode":      "push_wait",
		"hashes":    missing,
	})
	c.logger.Info("task parked awaiting pushed inputs", "task", taskID, "missing", missing)
	// A pull may still beat the push: inputs the sender declared but cannot
	// serve fast enough, third-party sources the sender never had, and the
	// mixed-version case (a peer that does not know the push protocol still
	// serves ordinary fetches) all complete through the same pool — the wake
	// path only checks Has(), it does not care how the bytes got there.
	go c.pullForeignPushInputs(runCtx, env.From, taskID, p.Inputs)
}

// resumePushedTask re-drives a parked task once every pushed input is in the
// pool. Mirrors the context-ack resume: the parked row occupied a countable
// slot the whole wait, so the capacity reservation freed when the entry was
// claimed is not re-taken — run() gets nil and the row's own transition does
// the accounting.
func (c *Core) resumePushedTask(ctx context.Context, taskID string, p bus.TaskDelegatePayload, required []string) {
	c.logger.Info("resuming task after pushed inputs", "task", taskID)
	go func() {
		result, err := c.run(ctx, taskID, p.Intent, required, nil)
		if err != nil {
			if errors.Is(err, ErrCancelled) {
				c.logger.Info("task cancelled during execution", "task", taskID)
				return
			}
			c.logger.Warn("run pushed-input task", "task", taskID, "err", err)
			if t, gerr := c.store.Get(ctx, taskID); gerr == nil {
				c.relayToParent(ctx, bus.MsgTaskDecline, t.Chain, bus.TaskDeclinePayload{
					TaskID: taskID, Reason: err.Error(),
				})
			}
			c.terminalizeDeclined(ctx, taskID, err.Error())
			return
		}
		if t, gerr := c.store.Get(ctx, taskID); gerr == nil {
			c.relayToParent(ctx, bus.MsgTaskResult, t.Chain, result)
		}
	}()
}

// wakeArtifactWaiters resumes parked tasks whose entire missing set is now
// in the pool. Triggered when a push commits a hash — and safe to call for
// any arrival path, because satisfaction is judged by Has(), not provenance.
// An empty hash checks every parked waiter (the periodic sweep): artifacts
// can also land via the pull path or a fat-bundle import, neither of which
// names the hash that should fire.
func (c *Core) wakeArtifactWaiters(ctx context.Context, hash string) {
	if c.artifacts == nil {
		return
	}
	c.pendingCtx.Range(func(k, v any) bool {
		taskID, ok := k.(string)
		if !ok {
			return true
		}
		pc, ok := v.(*pendingContext)
		if !ok || len(pc.waitHashes) == 0 {
			return true
		}
		if hash != "" && !slices.Contains(pc.waitHashes, hash) {
			return true
		}
		for _, h := range pc.waitHashes {
			if _, held := c.artifacts.Has(h); !held {
				return true // still waiting on another hash
			}
		}
		// Claim before resuming: a racing expiry/cancel or a second commit
		// must not double-run the task.
		if !c.pendingCtx.CompareAndDelete(taskID, v) {
			return true
		}
		if pc.release != nil {
			// The reservation stayed held across the park for exactly this
			// handoff: the row is already countable (waiting_context), so the
			// promise is redundant from here — same rule handleContextAck
			// applies when its fetch resolves.
			pc.release()
		}
		if pc.resume != nil {
			pc.resume()
		}
		return true
	})
}

// wakeSatisfiedArtifactWaiters is the periodic sweep: a parked task whose
// inputs all arrived by any means resumes on the next monitor tick instead
// of waiting out its deadline.
func (c *Core) wakeSatisfiedArtifactWaiters(ctx context.Context) {
	c.wakeArtifactWaiters(ctx, "")
}

// pullForeignPushInputs opportunistically pulls inputs the parked task still
// lacks: third-party sources a push was never coming from, and — as the
// mixed-version fallback — the sender itself when it stays reachable (a peer
// that does not implement the push still serves ordinary artifact_fetch, so
// waiting for its push alone would stall until the deadline).
func (c *Core) pullForeignPushInputs(ctx context.Context, sender, taskID string, inputs []bus.ArtifactRef) {
	if c.artifacts == nil {
		return
	}
	for _, in := range inputs {
		if in.Hash == "" || in.Source == c.nodeID {
			continue
		}
		source := in.Source
		if source == "" {
			source = sender
		}
		if source == "" || c.connFor(source) == nil {
			continue // source offline: the wait stays, the deadline bounds it
		}
		if _, ok := c.artifacts.Has(in.Hash); ok {
			continue
		}
		if _, err := c.FetchArtifactRef(ctx, source, taskID, in); err != nil {
			c.logger.Debug("foreign input pull failed", "task", taskID, "hash", in.Hash,
				"source", source, "err", err)
			continue
		}
		c.wakeArtifactWaiters(ctx, in.Hash)
	}
}

// cascadePushToDispatchTarget implements chained custody: when a pushed
// artifact lands on a node that already forwarded the owning task onward,
// this node becomes the holder for the next hop and streams it toward the
// task's current dispatch target. Without it a mid-route staging would sit
// complete but idle while the executor parks for inputs nobody will push.
func (c *Core) cascadePushToDispatchTarget(ctx context.Context, hash string) {
	if c.db == nil {
		return
	}
	rows, err := c.db.QueryContext(ctx,
		`SELECT task_id, input_artifacts_json, deadline_unix
		 FROM tasks WHERE state = 'dispatched' AND input_artifacts_json LIKE ?`,
		"%"+hash+"%")
	if err != nil {
		c.logger.Warn("push: cascade scan", "hash", hash, "err", err)
		return
	}
	type row struct {
		taskID   string
		inputs   string
		deadline int64
	}
	var candidates []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.taskID, &r.inputs, &r.deadline); err != nil {
			rows.Close()
			c.logger.Warn("push: cascade row", "err", err)
			return
		}
		candidates = append(candidates, r)
	}
	rows.Close()
	for _, r := range candidates {
		var refs []bus.ArtifactRef
		if err := json.Unmarshal([]byte(r.inputs), &refs); err != nil {
			continue
		}
		matched := false
		for _, in := range refs {
			if in.Hash == hash {
				matched = true
				break
			}
		}
		if !matched {
			continue // LIKE false-positive
		}
		target, err := c.store.DispatchTarget(ctx, r.taskID)
		if err != nil || target == "" || target == c.nodeID {
			continue
		}
		ttl := r.deadline
		if ttl <= 0 {
			ttl = time.Now().Add(defaultDTNTTL).Unix()
		}
		c.artifactPushEnqueue(ctx, target, r.taskID, hash, ttl)
		go c.outboxFlush(context.WithoutCancel(ctx), target)
	}
}

// pruneStagedArtifacts reclaims .staging space for pushes whose sidecar
// aged past the bundle's TTL — the sender's task died or its custody row was
// swept, so the partial copy will never complete.
func (c *Core) pruneStagedArtifacts(ctx context.Context) {
	if c.artifacts == nil {
		return
	}
	pruned, err := c.artifacts.PruneStaged(stagedPushMaxAge, time.Now())
	if err != nil {
		c.logger.Warn("prune staged artifacts", "err", err)
		return
	}
	for _, hash := range pruned {
		c.logger.Info("staged artifact pruned", "hash", hash)
	}
}

package core

// The result-delivery layer (review P0-2). Terminal task results cross the bus
// fire-and-forget, so a result emitted at the exact moment a peer disconnects
// was silently dropped: the executor recorded done while the delegator's lease
// expired into failed, and nothing ever reconciled the two. The outbox closes
// that gap with the smallest mechanism that is actually a guarantee: a terminal
// result that cannot be sent is persisted, keyed by (peer, task), and
// re-delivered the next time that peer's hello is accepted. Re-delivery is safe
// because the receiver (handleResult) is idempotent — it ignores results for a
// task it no longer owns or has already terminated.

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// outboxPersist stores a terminal result that could not be delivered to peer.
// It upserts on (peer, task_id) so repeated failures of the same result do not
// accumulate rows. A persistence failure is logged, not fatal: the result is
// still recorded locally on the task row, so the worst case degrades to the
// pre-outbox behaviour rather than corrupting anything.
func (c *Core) outboxPersist(ctx context.Context, peer string, p bus.TaskResultPayload) {
	if c.db == nil || peer == "" {
		return
	}
	raw, err := json.Marshal(p)
	if err != nil {
		c.logger.Warn("outbox: marshal result", "task", p.TaskID, "err", err)
		return
	}
	_, err = c.db.ExecContext(ctx,
		`INSERT INTO result_outbox (peer, task_id, payload_json, created_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(peer, task_id) DO UPDATE SET payload_json = excluded.payload_json`,
		peer, p.TaskID, string(raw), storage.Now())
	if err != nil {
		c.logger.Warn("outbox: persist result", "task", p.TaskID, "peer", peer, "err", err)
		return
	}
	c.logger.Info("outbox: result parked for redelivery", "task", p.TaskID, "peer", peer)
}

// outboxDrop removes a delivered result so it is not resent. Called when a
// result is successfully placed on the wire.
func (c *Core) outboxDrop(ctx context.Context, peer, taskID string) {
	if c.db == nil || peer == "" || taskID == "" {
		return
	}
	if _, err := c.db.ExecContext(ctx,
		`DELETE FROM result_outbox WHERE peer = ? AND task_id = ?`, peer, taskID); err != nil {
		c.logger.Warn("outbox: drop delivered result", "task", taskID, "peer", peer, "err", err)
	}
}

// outboxFlush re-delivers every parked result AND cancel destined for peer.
// Invoked when a peer's hello is accepted — the moment a return channel exists
// again. Each entry is sent independently so one bad payload cannot block the
// rest; a successful send removes the entry, a failed send leaves it for the
// next reconnect. Cancels ride the same guarantee (S2-7): a cancel that could
// not be delivered is re-delivered here, and the receiver's handleCancel is
// idempotent (a cancel on a terminal or unknown task is a no-op). The flush
// runs in its own goroutine so the handshake path is never blocked by
// delivery.
func (c *Core) outboxFlush(ctx context.Context, peer string) {
	if c.db == nil || peer == "" {
		return
	}
	if !c.outboxFlushClaim(peer) {
		return // a flush for this peer is already running (hello or sweep)
	}
	type entry struct {
		taskID string
		raw    string
	}
	rows, err := c.db.QueryContext(ctx,
		`SELECT task_id, payload_json FROM result_outbox WHERE peer = ?`, peer)
	if err != nil {
		c.logger.Warn("outbox: query", "peer", peer, "err", err)
		return
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.taskID, &e.raw); err != nil {
			rows.Close()
			c.logger.Warn("outbox: scan", "peer", peer, "err", err)
			return
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		c.logger.Warn("outbox: rows", "peer", peer, "err", err)
		return
	}
	type cancelEntry struct {
		taskID string
		reason string
	}
	var cancels []cancelEntry
	crows, err := c.db.QueryContext(ctx,
		`SELECT task_id, reason FROM cancel_outbox WHERE peer = ?`, peer)
	if err != nil {
		c.logger.Warn("outbox: query cancels", "peer", peer, "err", err)
	} else {
		for crows.Next() {
			var e cancelEntry
			if err := crows.Scan(&e.taskID, &e.reason); err != nil {
				crows.Close()
				c.logger.Warn("outbox: scan cancels", "peer", peer, "err", err)
				return
			}
			cancels = append(cancels, e)
		}
		crows.Close()
		if err := crows.Err(); err != nil {
			c.logger.Warn("outbox: cancel rows", "peer", peer, "err", err)
			return
		}
	}
	type taskEntry struct {
		taskID string
		raw    string
		blob   []byte
		ttl    int64
	}
	var taskEntries []taskEntry
	trows, err := c.db.QueryContext(ctx,
		`SELECT task_id, payload_json, payload_blob, ttl FROM task_outbox WHERE peer = ?`, peer)
	if err != nil {
		c.logger.Warn("outbox: query tasks", "peer", peer, "err", err)
	} else {
		for trows.Next() {
			var e taskEntry
			if err := trows.Scan(&e.taskID, &e.raw, &e.blob, &e.ttl); err != nil {
				trows.Close()
				c.logger.Warn("outbox: scan tasks", "peer", peer, "err", err)
				return
			}
			taskEntries = append(taskEntries, e)
		}
		trows.Close()
	}

	// Deferred artifact pushes are custody too: a peer whose only pending
	// work is chunked payload must still get its flush, or a large transfer
	// would stall until some unrelated row arrived.
	var pushPending int
	_ = c.db.QueryRowContext(ctx,
		`SELECT count(*) FROM artifact_push_outbox WHERE peer = ?`, peer).Scan(&pushPending)

	if len(entries) == 0 && len(cancels) == 0 && len(taskEntries) == 0 && pushPending == 0 {
		c.outboxFlushDone(peer)
		return
	}
	go func() {
		defer c.outboxFlushDone(peer)
		flushCtx := context.WithoutCancel(ctx)
		for _, e := range taskEntries {
			// §8.2 TTL: a bundle parked past its deadline is dead — delivering
			// it now would run work whose result the mesh already abandoned.
			// Drop the entry and expire the local copy so it stops occupying
			// the dispatched slot it was parked under.
			if e.ttl > 0 && time.Now().Unix() > e.ttl {
				c.logger.Info("outbox: parked task past TTL, expiring", "task", e.taskID, "peer", peer)
				c.taskOutboxDrop(flushCtx, peer, e.taskID)
				if err := c.store.MarkExpired(flushCtx, e.taskID, "dtn TTL expired"); err != nil {
					c.logger.Warn("outbox: mark expired", "task", e.taskID, "err", err)
				}
				continue
			}
			// The CBOR bundle is the authoritative record AND the wire form
			// (§8.3): verified on our side, then delivered verbatim as a
			// dtn_bundle so the receiver checks the same signature. A row
			// that was tampered while parked fails closed here instead of
			// executing forged work on delivery.
			if len(e.blob) > 0 {
				bnd, berr := bus.UnmarshalBundle(e.blob)
				if berr != nil {
					c.logger.Warn("outbox: corrupt bundle, dropping", "task", e.taskID, "peer", peer, "err", berr)
					c.taskOutboxDrop(flushCtx, peer, e.taskID)
					continue
				}
				if verr := bnd.Verify([]byte(c.sharedSecret), time.Now().Unix()); verr != nil {
					c.logger.Warn("outbox: bundle verify failed, dropping", "task", e.taskID, "peer", peer, "err", verr)
					c.taskOutboxDrop(flushCtx, peer, e.taskID)
					if err := c.store.MarkExpired(flushCtx, e.taskID, "bundle verify failed"); err != nil {
						c.logger.Warn("outbox: mark expired", "task", e.taskID, "err", err)
					}
					continue
				}
				if !c.deliverBundle(flushCtx, peer, e.blob) {
					continue
				}
				c.taskOutboxDrop(flushCtx, peer, e.taskID)
				c.logger.Info("outbox: redelivered forward task", "task", e.taskID, "peer", peer)
				continue
			}
			// Legacy row (pre-bundle or a bundle wrap that failed): deliver
			// the JSON payload as an ordinary task_delegate.
			var p bus.TaskDelegatePayload
			if err := json.Unmarshal([]byte(e.raw), &p); err != nil {
				c.logger.Warn("outbox: bad parked task payload, dropping", "task", e.taskID, "peer", peer, "err", err)
				c.taskOutboxDrop(flushCtx, peer, e.taskID)
				continue
			}
			if !c.deliverTask(flushCtx, peer, p) {
				continue
			}
			c.taskOutboxDrop(flushCtx, peer, e.taskID)
			c.logger.Info("outbox: redelivered forward task", "task", e.taskID, "peer", peer)
		}
		// §8.3 chunked push rides the same custody model as the parked
		// bundles above: rows retire on the receiver's acked waterline, and
		// the stream resumes — not restarts — after a reconnect. Ordering
		// matters: delegates go first so the task row exists when the chunks
		// arrive and the receiver's authorization check can bind them to it.
		c.streamArtifactPushes(flushCtx, peer)
		// Multi-hop custody: a peer that just connected may be the best next
		// hop for bundles keyed to some third, still-offline destination.
		// Rows keyed to this peer itself were handled above.
		c.relayParked(flushCtx, peer)
		for _, e := range entries {
			var p bus.TaskResultPayload
			if err := json.Unmarshal([]byte(e.raw), &p); err != nil {
				c.logger.Warn("outbox: bad parked payload, dropping", "task", e.taskID, "peer", peer, "err", err)
				c.outboxDrop(flushCtx, peer, e.taskID)
				continue
			}
			if !c.deliverResult(flushCtx, peer, p) {
				continue // still parked; retry on next reconnect
			}
			c.outboxDrop(flushCtx, peer, e.taskID)
			c.logger.Info("outbox: redelivered result", "task", e.taskID, "peer", peer)
		}
		for _, e := range cancels {
			if !c.deliverCancel(flushCtx, peer, e.taskID, e.reason) {
				continue // still parked; retry on next reconnect
			}
			c.outboxCancelDrop(flushCtx, peer, e.taskID)
			c.logger.Info("outbox: redelivered cancel", "task", e.taskID, "peer", peer)
		}
	}()
}

// deliverResult places a task_result envelope on the wire to peer, returning
// whether it was accepted by the connection. It does not consult or touch the
// outbox, so both the initial send path and the flush path can share it.
func (c *Core) deliverResult(ctx context.Context, peer string, p bus.TaskResultPayload) bool {
	msgID, err := newUUID()
	if err != nil {
		c.logger.Warn("outbox: mint message id", "task", p.TaskID, "err", err)
		return false
	}
	env, err := bus.NewEnvelope(bus.MsgTaskResult, c.nodeID, msgID, p)
	if err != nil {
		c.logger.Warn("outbox: build envelope", "task", p.TaskID, "err", err)
		return false
	}
	env.To = peer
	if err := c.sendTo(peer, env); err != nil {
		c.logger.Warn("outbox: send", "task", p.TaskID, "peer", peer, "err", err)
		return false
	}
	return true
}

// outboxCancelPersist stores a task_cancel that could not be delivered to peer,
// upserting on (peer, task_id). A persistence failure is logged, not fatal:
// the downstream lease monitor still bounds the worst case, it just costs the
// executor extra work on an abandoned task (S2-7).
func (c *Core) outboxCancelPersist(ctx context.Context, peer, taskID, reason string) {
	if c.db == nil || peer == "" || taskID == "" {
		return
	}
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO cancel_outbox (peer, task_id, reason, created_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(peer, task_id) DO UPDATE SET reason = excluded.reason, created_at = excluded.created_at`,
		peer, taskID, reason, storage.Now())
	if err != nil {
		c.logger.Warn("outbox: persist cancel", "task", taskID, "peer", peer, "err", err)
		return
	}
	c.logger.Info("outbox: cancel parked for redelivery", "task", taskID, "peer", peer)
}

// outboxCancelDrop removes a delivered cancel so it is not resent.
func (c *Core) outboxCancelDrop(ctx context.Context, peer, taskID string) {
	if c.db == nil || peer == "" || taskID == "" {
		return
	}
	if _, err := c.db.ExecContext(ctx,
		`DELETE FROM cancel_outbox WHERE peer = ? AND task_id = ?`, peer, taskID); err != nil {
		c.logger.Warn("outbox: drop delivered cancel", "task", taskID, "peer", peer, "err", err)
	}
}

// deliverCancel places a task_cancel envelope on the wire to peer, returning
// whether it was accepted by the connection. Shared by the initial send path
// (forwardCancelDownstream) and the flush path.
func (c *Core) deliverCancel(ctx context.Context, peer, taskID, reason string) bool {
	msgID, err := newUUID()
	if err != nil {
		c.logger.Warn("outbox: mint cancel id", "task", taskID, "err", err)
		return false
	}
	env, err := bus.NewEnvelope(bus.MsgTaskCancel, c.nodeID, msgID, bus.TaskCancelPayload{
		TaskID: taskID, Reason: reason,
	})
	if err != nil {
		c.logger.Warn("outbox: build cancel envelope", "task", taskID, "err", err)
		return false
	}
	env.To = peer
	if err := c.sendTo(peer, env); err != nil {
		c.logger.Warn("outbox: send cancel", "task", taskID, "peer", peer, "err", err)
		return false
	}
	return true
}

// taskOutboxPersist stores a forward task that could not be delivered immediately,
// implementing the universal DTN store-and-forward relay outbox (whitepaper §8.2, §9.1).
// The task is parked both as its JSON payload (legacy decode path) and as a
// CBOR-signed DTN bundle (§8.3): EID addressing, absolute TTL and an HMAC that
// keeps a parked row tamper-evident for as long as it waits.
func (c *Core) taskOutboxPersist(ctx context.Context, peer string, p bus.TaskDelegatePayload, transportType string, ttl int64) {
	if c.db == nil || peer == "" {
		return
	}
	raw, err := json.Marshal(p)
	if err != nil {
		c.logger.Warn("task_outbox: marshal task", "task", p.TaskID, "err", err)
		return
	}
	if transportType == "" {
		transportType = "dtn"
	}
	var blob []byte
	if b, berr := bus.NewBundle(p.TaskID, bus.EID(c.nodeID), bus.EID(peer),
		bus.MsgTaskDelegate, ttl, raw, []byte(c.sharedSecret)); berr == nil {
		blob = b.Marshal()
	} else {
		c.logger.Warn("task_outbox: bundle wrap", "task", p.TaskID, "err", berr)
	}
	_, err = c.db.ExecContext(ctx,
		`INSERT INTO task_outbox (peer, task_id, payload_json, payload_blob, transport_type, ttl, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(peer, task_id) DO UPDATE SET payload_json = excluded.payload_json,
			payload_blob = excluded.payload_blob, ttl = excluded.ttl`,
		peer, p.TaskID, string(raw), blob, transportType, ttl, storage.Now())
	if err != nil {
		c.logger.Warn("task_outbox: persist task", "task", p.TaskID, "peer", peer, "err", err)
		return
	}
	c.logger.Info("task_outbox: task parked for DTN redelivery", "task", p.TaskID, "peer", peer)
}

// taskOutboxDrop removes a delivered task so it is not resent.
func (c *Core) taskOutboxDrop(ctx context.Context, peer, taskID string) {
	if c.db == nil || peer == "" || taskID == "" {
		return
	}
	if _, err := c.db.ExecContext(ctx,
		`DELETE FROM task_outbox WHERE peer = ? AND task_id = ?`, peer, taskID); err != nil {
		c.logger.Warn("task_outbox: drop delivered task", "task", taskID, "peer", peer, "err", err)
	}
}

// deliverTask places a task_delegate envelope on the wire to peer.
func (c *Core) deliverTask(ctx context.Context, peer string, p bus.TaskDelegatePayload) bool {
	msgID, err := newUUID()
	if err != nil {
		c.logger.Warn("task_outbox: mint message id", "task", p.TaskID, "err", err)
		return false
	}
	env, err := bus.NewEnvelope(bus.MsgTaskDelegate, c.nodeID, msgID, p)
	if err != nil {
		c.logger.Warn("task_outbox: build envelope", "task", p.TaskID, "err", err)
		return false
	}
	env.To = peer
	if err := c.sendTo(peer, env); err != nil {
		c.logger.Warn("task_outbox: send", "task", p.TaskID, "peer", peer, "err", err)
		return false
	}
	return true
}

// deliverBundle places a dtn_bundle envelope carrying a verbatim signed CBOR
// bundle on the wire (§8.3). It is the flush path's send for a parked row:
// the stored blob IS the signed object, so redelivery is byte-identical to
// the dispatch that parked it.
func (c *Core) deliverBundle(ctx context.Context, peer string, blob []byte) bool {
	msgID, err := newUUID()
	if err != nil {
		c.logger.Warn("task_outbox: mint message id", "err", err)
		return false
	}
	env, err := bus.NewEnvelope(bus.MsgDTNBundle, c.nodeID, msgID, bus.DTNBundlePayload{Blob: blob})
	if err != nil {
		c.logger.Warn("task_outbox: build bundle envelope", "err", err)
		return false
	}
	env.To = peer
	if err := c.sendTo(peer, env); err != nil {
		c.logger.Warn("task_outbox: send bundle", "peer", peer, "err", err)
		return false
	}
	return true
}

// sweepOutboxes is the monitor tick's periodic redelivery pass (§9.1): parked
// entries only flushed on the peer's hello before, which meant a peer that
// stayed CONNECTED but briefly dropped a send (a transient write error, a
// lane that was full for a beat) kept its parked work until the next
// reconnect. The sweep walks every peer with a pending row and flushes the
// ones that are reachable right now — idempotent because the receiver dedups
// on the task/result id, not on the frame.
func (c *Core) sweepOutboxes(ctx context.Context) {
	if c.db == nil {
		return
	}
	rows, err := c.db.QueryContext(ctx, `
		SELECT DISTINCT peer FROM result_outbox
		UNION SELECT DISTINCT peer FROM cancel_outbox
		UNION SELECT DISTINCT peer FROM task_outbox`)
	if err != nil {
		c.logger.Warn("outbox: sweep query", "err", err)
		return
	}
	var peers []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err == nil {
			peers = append(peers, p)
		}
	}
	rows.Close()
	for _, peer := range peers {
		if c.connFor(peer) == nil {
			continue
		}
		c.outboxFlush(ctx, peer) // claims internally; skips if one is running
	}
	// §8.3 multi-hop: rows keyed to a destination that never connects
	// directly would wait out their TTL parked. Recompute a live next hop
	// for each signed bundle and move custody closer.
	c.relayParked(ctx, "")
}

// relayParked re-routes parked task_outbox bundles through the link-state
// graph: for every signed row whose final destination is unreachable, the
// first hop of the cheapest advertised path is tried instead (§8.3). Rows
// stay keyed by destination — custody moves, addressing does not. via on the
// row is excluded as a next hop so a flush can never echo a bundle back the
// way it arrived, and relayForwardOK bounds how often this node will carry
// the same bundle, which is what a signed hop-less bundle needs for loop
// safety. onlyHop narrows the pass to rows whose best hop is that peer (the
// hello-flush case); "" (the sweep case) considers every row.
func (c *Core) relayParked(ctx context.Context, onlyHop string) {
	if c.db == nil {
		return
	}
	rows, err := c.db.QueryContext(ctx,
		`SELECT peer, task_id, payload_blob, ttl, via FROM task_outbox WHERE payload_blob IS NOT NULL`)
	if err != nil {
		c.logger.Warn("outbox: relay scan", "err", err)
		return
	}
	type parked struct {
		peer, taskID, via string
		blob              []byte
		ttl               int64
	}
	var entries []parked
	for rows.Next() {
		var e parked
		var blob []byte
		if err := rows.Scan(&e.peer, &e.taskID, &blob, &e.ttl, &e.via); err != nil {
			rows.Close()
			c.logger.Warn("outbox: relay scan row", "err", err)
			return
		}
		e.blob = blob
		entries = append(entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		c.logger.Warn("outbox: relay rows", "err", err)
		return
	}
	now := time.Now().Unix()
	// The directory is loaded lazily on the first row that reaches routing —
	// a pass whose rows are all expired or self-addressed never queries it —
	// and shared across every row after: re-running ledger.Query per bundle
	// would make a sweep cost rows × directory size.
	var self ledger.Node
	var nodes []ledger.Node
	dirLoaded := false
	for _, e := range entries {
		// A row keyed to a connected peer is the ordinary flush's job; in
		// the hello-triggered pass the flush already handled its own rows.
		if e.peer == onlyHop || (onlyHop == "" && c.connFor(e.peer) != nil) {
			continue
		}
		if e.ttl > 0 && now > e.ttl {
			c.logger.Info("outbox: parked bundle past TTL, expiring", "task", e.taskID, "peer", e.peer)
			c.taskOutboxDrop(ctx, e.peer, e.taskID)
			if err := c.store.MarkExpired(ctx, e.taskID, "dtn TTL expired"); err != nil {
				c.logger.Warn("outbox: mark expired", "task", e.taskID, "err", err)
			}
			continue
		}
		bnd, err := bus.UnmarshalBundle(e.blob)
		if err != nil {
			c.logger.Warn("outbox: corrupt parked bundle, dropping", "task", e.taskID, "peer", e.peer, "err", err)
			c.taskOutboxDrop(ctx, e.peer, e.taskID)
			continue
		}
		if verr := bnd.Verify([]byte(c.sharedSecret), now); verr != nil {
			c.logger.Warn("outbox: parked bundle verify failed, dropping", "task", e.taskID, "peer", e.peer, "err", verr)
			c.taskOutboxDrop(ctx, e.peer, e.taskID)
			if err := c.store.MarkExpired(ctx, e.taskID, "bundle verify failed"); err != nil {
				c.logger.Warn("outbox: mark expired", "task", e.taskID, "err", err)
			}
			continue
		}
		dest := strings.TrimPrefix(bnd.DestEID, "panda://")
		if dest == "" || dest == c.nodeID {
			continue
		}
		if !dirLoaded {
			var derr error
			self, nodes, derr = c.dtnDirectory()
			if derr != nil {
				c.logger.Warn("outbox: relay directory", "err", derr)
				return
			}
			dirLoaded = true
		}
		exclude := map[string]bool{}
		if e.via != "" {
			exclude[e.via] = true
		}
		hop := scheduler.DTNNextHop(self, nodes, dest, exclude)
		if hop == "" || (onlyHop != "" && hop != onlyHop) || c.connFor(hop) == nil {
			continue
		}
		if !c.relayForwardOK(ctx, bnd.BundleID, bnd.DeadlineUnix) {
			continue // loop bound spent: hold custody for a direct contact
		}
		if !c.deliverBundle(ctx, hop, e.blob) {
			continue
		}
		c.taskOutboxDrop(ctx, e.peer, e.taskID)
		c.logger.Info("outbox: custody moved toward dest", "bundle", bnd.BundleID,
			"hop", hop, "dest", dest)
	}
}

// outboxFlushClaim marks a peer's flush as in-flight so the periodic sweep
// cannot stack concurrent flush goroutines on the same destination (artifact
// chunk redelivery inside one flush can legitimately outlive a tick).
func (c *Core) outboxFlushClaim(peer string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.outboxFlushing == nil {
		c.outboxFlushing = make(map[string]bool)
	}
	if c.outboxFlushing[peer] {
		return false
	}
	c.outboxFlushing[peer] = true
	return true
}

// outboxFlushDone releases a peer's in-flight flush marker.
func (c *Core) outboxFlushDone(peer string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.outboxFlushing, peer)
}

// SPDX-License-Identifier: AGPL-3.0-or-later

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
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// Batch-6 restart continuity (confirmed-issues §14): outbox rows used to key
// the DESTINATION by the peer's instance id, so a node that restarted — new
// process, new node id — could never claim work parked for its old self. The
// protocol now separates the three identities the doc requires:
//
//   - stable_node_id: the node's authenticated Ed25519 identity, recorded in
//     employee_cache.pub_key by a signed hello. On the wire and in the outbox
//     it is written "k:" + hex(pub) so stable keys can never collide with a
//     raw instance id.
//   - instance_id: the ordinary node id — one running process. It is what
//     hellos advertise, conns are dialed to, and envelopes are sent to.
//   - operation_id: the task id, which already survives every restart.
//
// The signed hello IS the binding between the two: EdSig proves the instance
// controls the key it advertises, and only then is pub_key recorded — so a
// restarted instance claiming the same key inherits its stable identity,
// while an impostor without the private key can never get the key attributed
// to its instance at all. Peers that never sign (pre-key hellos) keep their
// instance id as the stable key and behave exactly as before.
const stableIDPrefix = "k:"

// stablePeerID resolves the durable outbox key for a peer: its authenticated
// stable identity when the directory holds a proven key for that instance,
// the instance id itself otherwise. Already-stable inputs pass through so
// callers can feed either form.
func (c *Core) stablePeerID(ctx context.Context, peer string) string {
	if c.db == nil || peer == "" || strings.HasPrefix(peer, stableIDPrefix) {
		return peer
	}
	var pub string
	if err := c.db.QueryRowContext(ctx,
		`SELECT COALESCE(pub_key, '') FROM employee_cache WHERE id = ?`, peer).Scan(&pub); err == nil && pub != "" {
		return stableIDPrefix + pub
	}
	return peer
}

// instanceForStable maps a stable outbox key back to the peer's CURRENT
// instance id — the only form a conn can be dialed to. A restarted node may
// leave several directory rows under one identity, so the pick is the most
// recently seen instance, online first. Returns "" when the stable identity
// is not resolvable (the peer has not helloed this node since the identity
// was keyed). Non-stable inputs are themselves instances and pass through.
func (c *Core) instanceForStable(ctx context.Context, key string) string {
	if inst := c.sendableInstanceForStable(ctx, key); inst != "" {
		return inst
	}
	if c.db == nil || !strings.HasPrefix(key, stableIDPrefix) {
		return key
	}
	var id string
	if err := c.db.QueryRowContext(ctx,
		`SELECT id FROM employee_cache WHERE pub_key = ?
		 ORDER BY (status = 'online') DESC, last_seen DESC LIMIT 1`,
		key[len(stableIDPrefix):]).Scan(&id); err == nil {
		return id
	}
	return ""
}

// sendableInstanceForStable resolves a stable key to an instance that can
// actually be reached right now: it walks every directory id recorded under
// that identity, online first, and returns the first with a live track.
// Returns "" when no instance is currently sendable.
func (c *Core) sendableInstanceForStable(ctx context.Context, key string) string {
	for _, id := range c.instancesForStable(ctx, key) {
		if c.sendableTo(id) {
			return id
		}
	}
	return ""
}

// instancesForStable lists the instance ids a stable key may be delivered
// through, online instances first. Non-stable keys yield just themselves.
func (c *Core) instancesForStable(ctx context.Context, key string) []string {
	if c.db == nil || !strings.HasPrefix(key, stableIDPrefix) {
		return []string{key}
	}
	rows, err := c.db.QueryContext(ctx,
		`SELECT id FROM employee_cache WHERE pub_key = ?
		 ORDER BY (status = 'online') DESC, last_seen DESC`,
		key[len(stableIDPrefix):])
	if err != nil {
		return nil
	}
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil && id != "" {
			out = append(out, id)
		}
	}
	rows.Close()
	return out
}

// selfStableID is this node's own stable key — what bundles addressed to it
// carry once senders key destinations by identity rather than instance.
func (c *Core) selfStableID() string {
	if pub, _, ok := c.nodeKeyPair(); ok {
		return stableIDPrefix + hex.EncodeToString(pub)
	}
	return c.nodeID
}

// claimKeys returns every outbox destination key an instance may claim: its
// stable key plus every instance id the directory attributes to that same
// identity plus its own instance id. The union covers rows parked before the
// peer's key was known (keyed by raw instance id) and rows a previous
// instance left behind — while never reaching past the authenticated
// identity boundary: an instance whose key proves a DIFFERENT pub only ever
// matches its own key's set.
func (c *Core) claimKeys(ctx context.Context, key, inst string) []string {
	keys := []string{key}
	if inst != key {
		keys = append(keys, inst)
	}
	if strings.HasPrefix(key, stableIDPrefix) && c.db != nil {
		rows, err := c.db.QueryContext(ctx,
			`SELECT id FROM employee_cache WHERE pub_key = ?`, key[len(stableIDPrefix):])
		if err == nil {
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err == nil && id != key {
					keys = append(keys, id)
				}
			}
			rows.Close()
		}
	}
	// Dedup; the set is tiny (one entry per instance the mesh has seen).
	out := keys[:0]
	seen := map[string]bool{}
	for _, k := range keys {
		if k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// outboxPersist stores a terminal result that could not be delivered to peer.
// It upserts on (peer, task_id) so repeated failures of the same result do not
// accumulate rows. A persistence failure is logged, not fatal: the result is
// still recorded locally on the task row, so the worst case degrades to the
// pre-outbox behaviour rather than corrupting anything.
func (c *Core) outboxPersist(ctx context.Context, peer string, p bus.TaskResultPayload) {
	if c.db == nil || peer == "" {
		return
	}
	peer = c.stablePeerID(ctx, peer)
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
	c.resultOutboxDropKey(ctx, c.stablePeerID(ctx, peer), taskID)
}

// resultOutboxDropKey deletes by the exact stored destination key. The flush
// claims rows under several keys at once (stable + every known instance of
// that identity), so the delete must name the key the row was parked under,
// not whatever the peer resolves to today.
func (c *Core) resultOutboxDropKey(ctx context.Context, key, taskID string) {
	if _, err := c.db.ExecContext(ctx,
		`DELETE FROM result_outbox WHERE peer = ? AND task_id = ?`, key, taskID); err != nil {
		c.logger.Warn("outbox: drop delivered result", "task", taskID, "peer", key, "err", err)
	}
}

// outboxFlush re-delivers every parked result AND cancel destined for peer.
// Invoked when a peer's hello is accepted — the moment a return channel exists
// again. peer is the INSTANCE id from that hello; custody rows are keyed by
// the peer's stable identity, resolved once here — this is the claim: a
// restarted node whose new instance proves the same key collects everything
// its old instance was owed, while an instance that cannot authenticate the
// key selects nothing (stablePeerID falls back to the instance id, which
// matches only rows parked under that exact name). Each entry is sent
// independently so one bad payload cannot block the rest; a successful send
// removes the entry, a failed send leaves it for the next reconnect. Cancels
// ride the same guarantee (S2-7): a cancel that could not be delivered is
// re-delivered here, and the receiver's handleCancel is idempotent (a cancel
// on a terminal or unknown task is a no-op). The flush runs in its own
// goroutine so the handshake path is never blocked by delivery.
func (c *Core) outboxFlush(ctx context.Context, peer string) {
	if c.db == nil || peer == "" {
		return
	}
	key := c.stablePeerID(ctx, peer)
	if !c.outboxFlushClaim(key) {
		return // a flush for this peer is already running (hello or sweep)
	}
	fail := func() { c.outboxFlushDone(key) }
	// The claim set: the stable key plus every instance id the directory
	// attributes to that identity — rows parked under any of them are this
	// peer's custody, and nothing outside the set is.
	destKeys := c.claimKeys(ctx, key, peer)
	ph := "?" + strings.Repeat(",?", len(destKeys)-1)
	dargs := func() []any {
		a := make([]any, len(destKeys))
		for i, k := range destKeys {
			a[i] = k
		}
		return a
	}
	type entry struct {
		taskID, raw, peer string
	}
	rows, err := c.db.QueryContext(ctx,
		`SELECT task_id, payload_json, peer FROM result_outbox WHERE peer IN (`+ph+`)`, dargs()...)
	if err != nil {
		c.logger.Warn("outbox: query", "peer", peer, "err", err)
		fail()
		return
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.taskID, &e.raw, &e.peer); err != nil {
			rows.Close()
			c.logger.Warn("outbox: scan", "peer", peer, "err", err)
			fail()
			return
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		c.logger.Warn("outbox: rows", "peer", peer, "err", err)
		fail()
		return
	}
	type cancelEntry struct {
		taskID, reason, peer string
	}
	var cancels []cancelEntry
	crows, err := c.db.QueryContext(ctx,
		`SELECT task_id, reason, peer FROM cancel_outbox WHERE peer IN (`+ph+`)`, dargs()...)
	if err != nil {
		c.logger.Warn("outbox: query cancels", "peer", peer, "err", err)
	} else {
		for crows.Next() {
			var e cancelEntry
			if err := crows.Scan(&e.taskID, &e.reason, &e.peer); err != nil {
				crows.Close()
				c.logger.Warn("outbox: scan cancels", "peer", peer, "err", err)
				fail()
				return
			}
			cancels = append(cancels, e)
		}
		crows.Close()
		if err := crows.Err(); err != nil {
			c.logger.Warn("outbox: cancel rows", "peer", peer, "err", err)
			fail()
			return
		}
	}
	type taskEntry struct {
		taskID, raw, peer string
		blob              []byte
		ttl               int64
	}
	var taskEntries []taskEntry
	trows, err := c.db.QueryContext(ctx,
		`SELECT task_id, payload_json, peer, payload_blob, ttl FROM task_outbox WHERE peer IN (`+ph+`)`, dargs()...)
	if err != nil {
		c.logger.Warn("outbox: query tasks", "peer", peer, "err", err)
	} else {
		for trows.Next() {
			var e taskEntry
			if err := trows.Scan(&e.taskID, &e.raw, &e.peer, &e.blob, &e.ttl); err != nil {
				trows.Close()
				c.logger.Warn("outbox: scan tasks", "peer", peer, "err", err)
				fail()
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
		`SELECT count(*) FROM artifact_push_outbox WHERE peer IN (`+ph+`)`, dargs()...).Scan(&pushPending)

	if len(entries) == 0 && len(cancels) == 0 && len(taskEntries) == 0 && pushPending == 0 {
		c.outboxFlushDone(key)
		return
	}
	go func() {
		defer c.outboxFlushDone(key)
		flushCtx := context.WithoutCancel(ctx)
		for _, e := range taskEntries {
			// The local row died while the task was parked — a user cancel, a
			// decline, an orphan rescue. Custody must not resurrect it:
			// delivering now would run work the mesh already declared dead.
			if lt, lerr := c.store.Get(flushCtx, e.taskID); lerr == nil && Terminal(lt.State) {
				c.logger.Info("outbox: local task is terminal, dropping parked delivery",
					"task", e.taskID, "peer", e.peer, "state", lt.State)
				c.taskOutboxDropKey(flushCtx, e.peer, e.taskID)
				continue
			}
			// §8.2 TTL: a bundle parked past its deadline is dead — delivering
			// it now would run work whose result the mesh already abandoned.
			// Drop the entry and expire the local copy so it stops occupying
			// the dispatched slot it was parked under.
			if e.ttl > 0 && time.Now().Unix() > e.ttl {
				c.logger.Info("outbox: parked task past TTL, expiring", "task", e.taskID, "peer", e.peer)
				c.taskOutboxDropKey(flushCtx, e.peer, e.taskID)
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
					c.logger.Warn("outbox: corrupt bundle, dropping", "task", e.taskID, "peer", e.peer, "err", berr)
					c.taskOutboxDropKey(flushCtx, e.peer, e.taskID)
					continue
				}
				if verr := bnd.Verify([]byte(c.sharedSecret), time.Now().Unix()); verr != nil {
					c.logger.Warn("outbox: bundle verify failed, dropping", "task", e.taskID, "peer", e.peer, "err", verr)
					c.taskOutboxDropKey(flushCtx, e.peer, e.taskID)
					if err := c.store.MarkExpired(flushCtx, e.taskID, "bundle verify failed"); err != nil {
						c.logger.Warn("outbox: mark expired", "task", e.taskID, "err", err)
					}
					continue
				}
				if !c.deliverBundle(flushCtx, peer, e.blob) {
					continue
				}
				c.taskOutboxDropKey(flushCtx, e.peer, e.taskID)
				c.logger.Info("outbox: redelivered forward task", "task", e.taskID, "peer", peer)
				continue
			}
			// Legacy row (pre-bundle or a bundle wrap that failed): deliver
			// the JSON payload as an ordinary task_delegate.
			var p bus.TaskDelegatePayload
			if err := json.Unmarshal([]byte(e.raw), &p); err != nil {
				c.logger.Warn("outbox: bad parked task payload, dropping", "task", e.taskID, "peer", e.peer, "err", err)
				c.taskOutboxDropKey(flushCtx, e.peer, e.taskID)
				continue
			}
			if !c.deliverTask(flushCtx, peer, p) {
				continue
			}
			c.taskOutboxDropKey(flushCtx, e.peer, e.taskID)
			c.logger.Info("outbox: redelivered forward task", "task", e.taskID, "peer", peer)
		}
		// §8.3 chunked push rides the same custody model as the parked
		// bundles above: rows retire on the receiver's acked waterline, and
		// the stream resumes — not restarts — after a reconnect. Ordering
		// matters: delegates go first so the task row exists when the chunks
		// arrive and the receiver's authorization check can bind them to it.
		c.streamArtifactPushes(flushCtx, destKeys, peer)
		// Multi-hop custody: a peer that just connected may be the best next
		// hop for bundles keyed to some third, still-offline destination.
		// Rows keyed to this peer itself were handled above.
		c.relayParked(flushCtx, peer)
		for _, e := range entries {
			var p bus.TaskResultPayload
			if err := json.Unmarshal([]byte(e.raw), &p); err != nil {
				c.logger.Warn("outbox: bad parked payload, dropping", "task", e.taskID, "peer", e.peer, "err", err)
				c.resultOutboxDropKey(flushCtx, e.peer, e.taskID)
				continue
			}
			if !c.deliverResult(flushCtx, peer, p) {
				continue // still parked; retry on next reconnect
			}
			c.resultOutboxDropKey(flushCtx, e.peer, e.taskID)
			c.logger.Info("outbox: redelivered result", "task", e.taskID, "peer", peer)
		}
		for _, e := range cancels {
			if !c.deliverCancel(flushCtx, peer, e.taskID, e.reason) {
				continue // still parked; retry on next reconnect
			}
			c.cancelOutboxDropKey(flushCtx, e.peer, e.taskID)
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
	peer = c.stablePeerID(ctx, peer)
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
	c.cancelOutboxDropKey(ctx, c.stablePeerID(ctx, peer), taskID)
}

// cancelOutboxDropKey deletes by the exact stored destination key — the same
// raw-key rule as resultOutboxDropKey.
func (c *Core) cancelOutboxDropKey(ctx context.Context, key, taskID string) {
	if _, err := c.db.ExecContext(ctx,
		`DELETE FROM cancel_outbox WHERE peer = ? AND task_id = ?`, key, taskID); err != nil {
		c.logger.Warn("outbox: drop delivered cancel", "task", taskID, "peer", key, "err", err)
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
// CBOR-signed DTN bundle (§8.3): EID addressing, a relative lifetime, and an
// HMAC that keeps a parked row tamper-evident for as long as it waits. ttl is
// this node's local expiry for the row; the bundle converts it to a lifetime
// so relays downstream re-anchor custody to their own clocks.
func (c *Core) taskOutboxPersist(ctx context.Context, peer string, p bus.TaskDelegatePayload, transportType string, ttl int64) {
	if c.db == nil || peer == "" {
		return
	}
	peer = c.stablePeerID(ctx, peer)
	raw, err := json.Marshal(p)
	if err != nil {
		c.logger.Warn("task_outbox: marshal task", "task", p.TaskID, "err", err)
		return
	}
	if transportType == "" {
		transportType = "dtn"
	}
	var lifetimeSec int64
	if ttl > 0 {
		lifetimeSec = ttl - time.Now().Unix()
		if lifetimeSec < 1 {
			lifetimeSec = 1
		}
	}
	var blob []byte
	if b, berr := bus.NewBundle(p.TaskID, bus.EID(c.nodeID), bus.EID(peer),
		bus.MsgTaskDelegate, lifetimeSec, raw, []byte(c.sharedSecret)); berr == nil {
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

// taskOutboxPending reports whether a parked forward for taskID still sits in
// this node's custody. A parked task is awaiting delivery, not orphaned — the
// rescue sweep must leave it alone regardless of the state a restart left its
// row in.
func (c *Core) taskOutboxPending(ctx context.Context, taskID string) bool {
	if c.db == nil || taskID == "" {
		return false
	}
	var n int
	if err := c.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM task_outbox WHERE task_id = ?`, taskID).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// taskOutboxTTL returns the latest delivery deadline among parked custody
// rows for taskID — the bound a synchronous waiter should honor while a
// pinned task waits out an unreachable link. 0 means nothing parked.
func (c *Core) taskOutboxTTL(ctx context.Context, taskID string) int64 {
	if c.db == nil || taskID == "" {
		return 0
	}
	var ttl int64
	if err := c.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(ttl), 0) FROM task_outbox WHERE task_id = ?`, taskID).Scan(&ttl); err != nil {
		return 0
	}
	return ttl
}

// taskOutboxDrop removes a delivered task so it is not resent.
func (c *Core) taskOutboxDrop(ctx context.Context, peer, taskID string) {
	if c.db == nil || peer == "" || taskID == "" {
		return
	}
	c.taskOutboxDropKey(ctx, c.stablePeerID(ctx, peer), taskID)
}

// taskOutboxDropKey deletes by the exact stored destination key — the same
// raw-key rule as resultOutboxDropKey.
func (c *Core) taskOutboxDropKey(ctx context.Context, key, taskID string) {
	if _, err := c.db.ExecContext(ctx,
		`DELETE FROM task_outbox WHERE peer = ? AND task_id = ?`, key, taskID); err != nil {
		c.logger.Warn("task_outbox: drop delivered task", "task", taskID, "peer", key, "err", err)
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
		UNION SELECT DISTINCT peer FROM task_outbox
		UNION SELECT DISTINCT peer FROM artifact_push_outbox`)
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
		// Delivery needs an instance, custody is keyed by stable identity —
		// and a row can also be parked under an INSTANCE key of a peer that
		// has since proven a key (parked before the key was known, or parked
		// by a peer running the old protocol). Resolve through the row's
		// stable identity so a restarted peer's rows flush through whichever
		// instance is live, not only the one that parked them.
		stable := c.stablePeerID(ctx, peer)
		if inst := c.sendableInstanceForStable(ctx, stable); inst != "" {
			c.outboxFlush(ctx, inst) // claims internally; skips if one is running
		}
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
	// Two-pass scan: the light projection (no payload_blob) first, so every
	// row the key/TTL filters reject never pays to move its bundle bytes —
	// parked custody is mostly rows for OFFLINE destinations, and a bundle
	// can run to megabytes. The second pass fetches the blob only for a row
	// that survived everything checkable without it.
	rows, err := c.db.QueryContext(ctx,
		`SELECT peer, task_id, ttl, via FROM task_outbox WHERE payload_blob IS NOT NULL`)
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
		if err := rows.Scan(&e.peer, &e.taskID, &e.ttl, &e.via); err != nil {
			rows.Close()
			c.logger.Warn("outbox: relay scan row", "err", err)
			return
		}
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
	selfStable := c.selfStableID()
	for _, e := range entries {
		// A row keyed to a sendable peer is the ordinary flush's job; in
		// the hello-triggered pass the flush already handled its own rows.
		// e.peer is a stable key — resolve to the current instance before
		// comparing or dialing. A row parked under an instance key of a peer
		// that has since proven a key resolves through that identity.
		inst := c.sendableInstanceForStable(ctx, c.stablePeerID(ctx, e.peer))
		if e.peer == onlyHop || (inst != "" && inst == onlyHop) ||
			(onlyHop == "" && inst != "") {
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
		var blob []byte
		if err := c.db.QueryRowContext(ctx,
			`SELECT payload_blob FROM task_outbox WHERE peer = ? AND task_id = ?`,
			e.peer, e.taskID).Scan(&blob); err != nil {
			// Row vanished between passes (a flush claimed it mid-sweep): not
			// an error — custody moved on, nothing to re-route.
			continue
		}
		e.blob = blob
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
		if dest == "" || dest == c.nodeID || dest == selfStable {
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
		// Routing speaks instance ids; a stable-keyed destination resolves
		// through the directory's recorded pub_key (same mapping
		// instanceForStable uses, over the already-loaded node list).
		routeDest := dest
		if strings.HasPrefix(dest, stableIDPrefix) {
			routeDest = ""
			for _, n := range nodes {
				if stableIDPrefix+n.PubKey == dest {
					routeDest = n.ID
					break
				}
			}
			if routeDest == "" {
				continue // destination identity unresolvable: hold custody
			}
		}
		exclude := map[string]bool{}
		if e.via != "" {
			exclude[e.via] = true
		}
		if len(self.Contacts) == 0 {
			self.Contacts = c.contacts // local config is authoritative for self
		}
		var hop string
		if c.dtnPlanActive(nodes) {
			hop, _ = scheduler.ContactNextHop(self, nodes, routeDest, exclude, now, int64(len(e.blob)), e.ttl)
		} else {
			hop = scheduler.DTNNextHop(self, nodes, routeDest, exclude)
		}
		if hop == "" || (onlyHop != "" && hop != onlyHop) || !c.sendableTo(hop) {
			continue
		}
		if !c.relayForwardOK(ctx, bnd.BundleID, bnd.LocalExpiry(now)) {
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

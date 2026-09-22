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
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
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

	if len(entries) == 0 && len(cancels) == 0 && len(taskEntries) == 0 {
		return
	}
	go func() {
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
			// The CBOR bundle is the authoritative record when present
			// (§8.3): it carries its own TTL and signature, so a row that was
			// tampered while parked fails closed here instead of executing
			// forged work on delivery. payload_json is the legacy fallback.
			raw := e.raw
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
				raw = string(bnd.Payload)
			}
			var p bus.TaskDelegatePayload
			if err := json.Unmarshal([]byte(raw), &p); err != nil {
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

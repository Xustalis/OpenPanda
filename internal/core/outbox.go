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

// outboxFlush re-delivers every parked result destined for peer. Invoked when a
// peer's hello is accepted — the moment a return channel exists again. Each
// entry is sent independently so one bad payload cannot block the rest; a
// successful send removes the entry, a failed send leaves it for the next
// reconnect. The flush runs in its own goroutine so the handshake path is never
// blocked by result delivery.
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
	if len(entries) == 0 {
		return
	}
	go func() {
		flushCtx := context.WithoutCancel(ctx)
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

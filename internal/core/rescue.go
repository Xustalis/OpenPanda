package core

import (
	"context"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
)

// orphanedForwardGrace is how long an orphaned delegation may stay unroutable
// before the rescue sweep gives up and fails it (S1-1). A task stuck in queued
// after a restart is re-route-retried on every monitor tick; the moment a peer
// reconnects or another capable node appears, it is rescued immediately. Only
// a task unroutable for this whole window is force-failed, so a transient
// outage does not kill work that was already forwarded. The sighting
// timestamps live in memory, so a restart resets the grace window — an
// accepted trade-off for keeping the rescue state-free in the database.
const orphanedForwardGrace = 2 * time.Minute

// stalePeerAfter is how long a directory row may go unseen before the stale
// sweep marks it offline (S1-4). Without it a node that died silently — no
// clean disconnect, no heartbeat — keeps an online row forever and routing
// keeps handing work to a ghost. Peers with a live connection are excluded,
// and rows with last_seen=0 (never seen) are left alone.
const stalePeerAfter = 90 * time.Second

// rescueOrphanedForwards recovers tasks this node forwarded to a remote
// executor before a restart and that Recover left queued (S1-1): the local
// in-flight delegation state (waiter, lease, connection) died with the
// process, and nothing else touches a queued-remotely row — ExpireTasks
// ignores it (no lease), and the queue scheduler never claims it (it is not
// scheduled). Each scan first attempts a re-route; when none exists the task
// is given orphanedForwardGrace before being failed and reported upstream.
func (c *Core) rescueOrphanedForwards(ctx context.Context) {
	tasks, err := c.store.QueuedDelegatedRemotely(ctx, c.nodeID)
	if err != nil {
		c.logger.Warn("orphan sweep: list", "err", err)
		return
	}
	active := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		active[t.TaskID] = true
		if c.rerouteDeclined(ctx, t.TaskID) {
			c.dropOrphan(t.TaskID)
			c.logger.Info("orphaned forward rescued by re-route", "task", t.TaskID)
			continue
		}
		c.orphanExpired(ctx, t.TaskID)
	}
	// Drop sightings for tasks that are no longer orphaned (rescued by the
	// queue scheduler, completed, cancelled) so the map cannot leak.
	c.mu.Lock()
	for id := range c.orphanSeen {
		if !active[id] {
			delete(c.orphanSeen, id)
		}
	}
	c.mu.Unlock()
}

// orphanExpired records when a task was first seen orphaned and, once the
// grace window has passed, fails it and propagates the failure upstream.
func (c *Core) orphanExpired(ctx context.Context, taskID string) {
	c.mu.Lock()
	first, seen := c.orphanSeen[taskID]
	if !seen {
		c.orphanSeen[taskID] = time.Now()
	}
	c.mu.Unlock()
	if seen && time.Since(first) >= orphanedForwardGrace {
		c.failOrphanedForward(ctx, taskID)
	}
}

// dropOrphan forgets a task's orphan sighting once it is no longer stranded.
func (c *Core) dropOrphan(taskID string) {
	c.mu.Lock()
	delete(c.orphanSeen, taskID)
	c.mu.Unlock()
}

// failOrphanedForward force-fails an orphaned delegation and reports the
// failure up the chain, so a root scheduler blocked in Submit (or a parent
// relay) learns the forwarded work is dead instead of waiting on a lease
// that nothing renews.
func (c *Core) failOrphanedForward(ctx context.Context, taskID string) {
	const reason = "relay restarted: no route for re-forward"
	if err := c.store.ForceFail(ctx, taskID, reason); err != nil {
		c.logger.Warn("orphan: force fail", "task", taskID, "err", err)
		return
	}
	c.dropOrphan(taskID)
	tk, err := c.store.Get(ctx, taskID)
	if err != nil {
		return
	}
	res := bus.TaskResultPayload{
		TaskID: taskID, AttemptID: tk.AttemptID, State: StateFailed, OK: false, ExitCode: 1,
		Stderr: reason, Chain: tk.Chain, Executor: c.nodeID,
	}
	c.relayToParent(ctx, bus.MsgTaskResult, tk.Chain, res)
	c.signalResult(taskID, res)
	c.logger.Info("orphaned forward failed out", "task", taskID)
}

// sweepStalePeers marks directory rows that have gone silent offline (S1-4).
// Connected peers are excluded: their row may lag a heartbeat tick behind,
// and a live conn is the stronger liveness signal. The self row is restored
// immediately if the sweep ever catches it (its heartbeat refreshes
// last_seen, so this is belt-and-braces for a startup ordering race).
func (c *Core) sweepStalePeers(ctx context.Context) {
	c.mu.RLock()
	exclude := make([]string, 0, len(c.peers))
	for id := range c.peers {
		exclude = append(exclude, id)
	}
	c.mu.RUnlock()

	expired, err := ledger.ExpireStale(c.db, int64(stalePeerAfter.Seconds()), exclude)
	if err != nil {
		c.logger.Warn("sweep stale peers", "err", err)
		return
	}
	for _, id := range expired {
		if scheduler.IsSelfRow(id, c.nodeID) {
			if err := ledger.MarkOnline(c.db, id); err != nil {
				c.logger.Warn("restore self directory row", "err", err)
			}
			continue
		}
		c.logger.Info("stale peer marked offline", "peer", id)
	}
}

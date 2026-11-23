// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"fmt"
	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/guard"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// goGuarded starts fn in a goroutine protected by panic recovery — the same
// contract the daemon entrypoints get from guard.Go. Every long-lived
// goroutine inside Core (conn read loops, announce/flush/discovery loops,
// one-shot fan-out sends) must take this path: a bare `go` inside a daemon
// means one panic kills the whole node, silently for a panic that would
// have been a log line.
func (c *Core) goGuarded(name string, fn func()) {
	guard.Go(c.logger, "core: "+name, nil, fn)
}

// A peer disconnect must not cancel accepted work. An explicit deadline still bounds it.
func detachedRunContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.WithoutCancel(ctx)
	if deadline, ok := ctx.Deadline(); ok {
		return context.WithDeadline(base, deadline)
	}
	return context.WithCancel(base)
}

// goWork admits a detached database writer before shutdown starts its drain.
func (c *Core) goWork(name string, fn func()) bool {
	c.artMu.Lock()
	defer c.artMu.Unlock()
	if c.artClosing {
		return false
	}
	c.workWG.Add(1)
	c.goGuarded(name, func() { defer c.workWG.Done(); fn() })
	return true
}

// goArtifact is goGuarded for the detached artifact work — replica pulls and
// tree adoptions — that result handlers launch past their envelope's
// lifetime. It is additionally tracked in artWG so WaitArtifacts can drain
// the writes before the process exits or a test unlinks the pool directory.
func (c *Core) goArtifact(name string, fn func()) {
	c.artMu.Lock()
	if c.artClosing {
		// Shutdown is draining or past it: an Add landing at counter 0 after
		// Wait began is never awaited, so the write would run untracked — and
		// dropping the call would lose the adoption outright. Run it inline
		// instead: still panic-guarded, still bounded by the transfer budget
		// inside fn, and it completes inside the caller that asked for it.
		c.artMu.Unlock()
		guard.Call(c.logger, "core: "+name, nil, fn)
		return
	}
	c.artWG.Add(1)
	c.artMu.Unlock()
	c.goGuarded(name, func() {
		defer c.artWG.Done()
		fn()
	})
}

// WaitArtifacts blocks until every detached artifact adoption has finished
// writing. Transfers carry their own budget (artifactTransferBudget), so the
// wait is bounded: a wedged fetch fails its own deadline and drops out of the
// group rather than pinning the caller.
func (c *Core) WaitArtifacts() {
	c.artWG.Wait()
}

// Recover normalizes tasks left active by a previous process instance, then
// re-arms the upstream hold on any remote-origin row still parked in review
// — the keepalive is in-memory and does not survive the restart that left
// those rows.
func (c *Core) Recover(ctx context.Context) (int, error) {
	n, err := c.store.Recover(ctx, c.nodeID)
	if err != nil {
		return n, err
	}
	m, err := c.RecoverLocalRequests(ctx)
	c.resumeReviewHolds(ctx)
	return n + m, err
}

// blockedAgents enumerates this node's circuit-open agent names (breaker
// keys minus the "agent:" prefix) for heartbeat publication, so peers' route
// decisions can avoid agents that are failing repeatedly here instead of
// learning it one bounce-decline at a time.
func (c *Core) blockedAgents() []string {
	var out []string
	for _, key := range c.breaker.BlockedKeys() {
		if name, ok := strings.CutPrefix(key, "agent:"); ok {
			out = append(out, name)
		}
	}
	return out
}

// RunMonitor runs the full monitor tick — the task-lifecycle sweeps plus the
// peer-directory duties that only make sense on the node owning the peer
// connections. It returns when ctx is done.
func (c *Core) RunMonitor(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.taskSweeps(ctx)
			// S1-4: directory rows for silently-dead peers stay online forever
			// without a liveness sweep, and routing keeps aiming ghosts.
			c.sweepStalePeers(ctx)
			// M2: re-elect the dynamic rendezvous set on its own cadence —
			// the 5s tick would re-score far more often than a re-election
			// can usefully change anything, so the eval paces itself.
			c.rzv.mu.Lock()
			due := time.Since(c.rzv.lastEval) >= rendezvousEvalEvery || c.rzv.lastEval.IsZero()
			c.rzv.mu.Unlock()
			if due {
				c.rendezvousEval(ctx)
			}
			// M2/P5: keep the rendezvous claim honest — ask a few peers to
			// dial us back, and let the answer decide whether the capability
			// card keeps advertising the role.
			if c.dialCheckDue() {
				c.runDialCheck(ctx)
			}
			// §4.1: RTT samples land after the connect-time refresh, so the
			// self row's link metrics need the periodic re-publish the tick
			// provides; and any outbox entry whose peer reconnected between
			// hellos is flushed here rather than waiting on a greeting.
			c.refreshSelfNeighbors(ctx)
			c.sweepOutboxes(ctx)
			// Backstop for pairing sessions orphaned by a wedged conn —
			// each live session's own goroutine handles the normal expiry.
			c.sweepPairSessions(ctx)
		}
	}
}

// RunTaskMonitor runs the task-lifecycle half of the monitor for an embedded
// surface — the web panel, `panda web`, any long-lived process that executes
// tasks off the shared store without owning the peer connections. Without it
// that process's tasks stall silently: a parked waiting_context never times
// out, a queued-remotely orphan is never rescued, an expired lease is never
// enforced, and the store alone cannot tell queued work why nothing moved.
// The peer-directory sweeps stay daemon-only: a second process marking peers
// offline from a connection set it does not own would fight the real node's
// heartbeats.
func (c *Core) RunTaskMonitor(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.taskSweeps(ctx)
		}
	}
}

// RunDirMonitor runs the peer-directory half of the monitor for an embedded
// surface that owns the mesh — `panda web` and the standalone sidecar hold
// the identity lock, the listener and the peer set, so expiring rows for
// peers that went silent is their duty by the same rule that makes it the
// daemon's. RunTaskMonitor deliberately leaves this out (a borrowed engine
// marking peers offline from a connection set it does not own would fight
// the real node's heartbeats), which left a console-only node with no
// directory sweep at all: every dead peer stayed `online` for the life of
// the console, routing kept aiming ghosts, and neither `panda nodes remove`
// nor DELETE /api/nodes/{id} would delete a row that claims to be up.
func (c *Core) RunDirMonitor(ctx context.Context) {
	every := c.dirSweepEvery
	if every <= 0 {
		every = 5 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// S1-4: directory rows for silently-dead peers stay online
			// forever without this sweep. The rendezvous election, dial
			// checks and outbox flushes stay daemon-only: they belong to
			// the routing role, not to the row's liveness.
			c.sweepStalePeers(ctx)
		}
	}
}

// RunReconcile runs only the local-work reconcile — the minimal monitor for
// surfaces that submit inline (REPL, TUI, one-shot ask): they never own the
// queue, but a task they execute can still be cancelled or force-failed by
// another process, and this pass is what stops the orphaned execution instead
// of letting it run to the agent hard timeout.
func (c *Core) RunReconcile(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.reconcileLocalWork(ctx)
		}
	}
}

// taskSweeps is the task-lifecycle half of a monitor tick. Every pass touches
// only this node's task rows and its own in-memory executions, so any process
// sharing the store may run it — the state-guarded writes arbitrate the races.
func (c *Core) taskSweeps(ctx context.Context) {
	// Plan convergence does not depend on who finished a stage: a review
	// approved from the CLI or the console moves the row in another
	// process, and only this sweep would notice.
	c.sweepPlans(ctx)
	// S1-1: a restart orphans forwarded tasks in queued — no lease, no
	// waiter, nothing else touches them. Re-route them or fail them out
	// after the grace window so the upstream chain learns the outcome.
	c.rescueOrphanedForwards(ctx)
	// The same class mid-life: a process that died between create and
	// dispatch — or between claim and run — leaves rows no path back to the
	// queue (or to a verdict) touches. Requeue the submits/claims, fail the
	// leaseless executions, all past the grace window.
	c.sweepStrandedWork(ctx)
	// §8.3: a parked push-waiter's inputs can also land via the pull
	// path or a fat-bundle import — neither calls the wake directly —
	// and a staging dir whose sender died needs periodic reclamation.
	c.wakeSatisfiedArtifactWaiters(ctx)
	c.pruneStagedArtifacts(ctx)
	c.sweepArtAuth()
	c.sweepArtServe()
	c.expireLeases(ctx)
	c.sweepRetention(ctx)
	c.sweepTaskDirs(ctx)
	// A row can go terminal — or park in review — out of band: cancelled
	// from the CLI, rejected from the console, failed by another process's
	// monitor. ExpireTasks only cleans the rows it fails itself, so without
	// this pass an out-of-band verdict leaves the local agent running under
	// a state the store already discarded.
	c.reconcileLocalWork(ctx)
}

// retentionSweepInterval paces DeleteSettledBefore: the deadline it enforces
// is measured in days, so an hourly pass is both timely enough and cheap.
const retentionSweepInterval = time.Hour

// SetRetentionDays installs the settled-task age limit (config
// storage.task_retention_days). Zero disables the sweep — the task table
// then grows without bound, which is the pre-knob behaviour an operator can
// still choose explicitly.
func (c *Core) SetRetentionDays(days int) {
	if days < 0 {
		days = 0
	}
	c.retentionDays.Store(int64(days))
}

// sweepRetention deletes settled tasks older than the configured retention.
// It is deliberately part of taskSweeps rather than its own goroutine: every
// process that owns the lifecycle monitor inherits the cleanup, the 5s tick
// provides the pacing, and the state-guarded delete is idempotent across the
// processes that may share one store.
func (c *Core) sweepRetention(ctx context.Context) {
	days := c.retentionDays.Load()
	if days <= 0 {
		return
	}
	now := time.Now()
	last := c.retentionSweepAt.Load()
	if last > 0 && now.Sub(time.Unix(last, 0)) < retentionSweepInterval {
		return
	}
	if !c.retentionSweepAt.CompareAndSwap(last, now.Unix()) {
		return // another sweep pass won the slot
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour).Unix()
	deleted, err := c.store.DeleteSettledBefore(ctx, cutoff)
	if err != nil {
		c.retentionSweepAt.Store(last) // let the next tick retry
		c.logger.Warn("task retention sweep", "err", err)
		return
	}
	if deleted > 0 {
		c.logger.Info("task retention sweep", "days", days, "deleted", deleted)
		c.audit(ctx, "", "store:retention", "tasks", "swept",
			fmt.Sprintf("deleted %d settled task(s) older than %d days", deleted, days))
	}
}

// sweepTaskDirs reaps per-task workdirs (workDir/tasks/<id>) whose owning row
// is gone — swept by retention, removed by `task delete`, or orphaned by a
// store reset. A directory whose row still exists is never touched even if
// the task settled long ago: the tree may be the user's only copy of the
// deliverable, so row absence is the only safe reap signal. It runs even with
// retention disabled because rows also leave the table through explicit
// deletes. Paced on the same hourly interval as the row sweep.
func (c *Core) sweepTaskDirs(ctx context.Context) {
	now := time.Now()
	last := c.taskDirSweepAt.Load()
	if last > 0 && now.Sub(time.Unix(last, 0)) < retentionSweepInterval {
		return
	}
	if !c.taskDirSweepAt.CompareAndSwap(last, now.Unix()) {
		return
	}
	root := filepath.Join(c.scratchRoot(), "tasks")
	entries, err := os.ReadDir(root)
	if err != nil {
		if !os.IsNotExist(err) {
			c.taskDirSweepAt.Store(last)
			c.logger.Warn("task workdir sweep", "err", err)
		}
		return
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return
	}
	// One batched existence query for the whole dir set: the per-dir Get
	// this replaced cost a SELECT per workdir on every hourly pass, and a
	// tasks table that has seen retention sweep is exactly where the row
	// lookups pile up.
	live, err := c.store.ExistingTaskIDs(ctx, names)
	if err != nil {
		c.taskDirSweepAt.Store(last)
		c.logger.Warn("task workdir sweep lookup", "err", err)
		return
	}
	removed := 0
	for _, name := range names {
		if live[name] {
			continue // row lives: the dir stays, whatever state the task is in
		}
		if rerr := os.RemoveAll(filepath.Join(root, name)); rerr != nil {
			c.logger.Warn("task workdir sweep remove", "dir", name, "err", rerr)
			continue
		}
		removed++
	}
	if removed > 0 {
		c.logger.Info("task workdir sweep", "removed", removed)
	}
}

// expireLeases settles every active task whose lease or absolute deadline has
// passed. Phase one releases what the lapsed row still holds — the local
// execution (if this process drives it), the parked-context reservation, the
// remote executor still burning tokens — BEFORE any settle, so a re-queued
// row can never be re-claimed while its predecessor still runs. Phase two
// settles: lease-lapsed rows re-queue for re-route while retry budget lasts
// (a dead executor's work moves to a live peer instead of dying); budget-
// spent and deadline rows take their terminal verdict and propagate it up
// the chain so a root scheduler blocked in Submit unblocks (D3).
func (c *Core) expireLeases(ctx context.Context) {
	expired, err := c.store.ScanExpired(ctx)
	if err != nil {
		c.logger.Warn("expire tasks", "err", err)
		return
	}
	if len(expired) == 0 {
		return
	}
	var failedN, reroutedN int
	for _, r := range expired {
		switch c.settleExpired(ctx, r) {
		case ExpiryRerouted:
			reroutedN++
		case ExpiryFailed:
			failedN++
		}
	}
	c.logger.Info("monitor expired tasks", "count", failedN+reroutedN, "failed", failedN, "rerouted", reroutedN)
	if reroutedN > 0 && c.queueSched != nil {
		// Re-queued rows are scheduled-ready now; without the nudge the
		// idle-backoff poll (up to 2s) is the only thing that picks them up.
		c.queueSched.Wake()
	}
}

// settleExpired gives one scanned row its expiry verdict and cleans up
// everything attached to the run it ends: the downstream cancel rides the
// delegate target while it is still on the record, the row settles (re-queue
// with a fresh attempt while budget lasts, terminal when spent or past the
// deadline), and only then does the local process die — settling first means
// the dying runner's deferred 'failed' write sees the row moved on and
// no-ops instead of beating the re-queue to it.
//
// The process kill is handle-precise: the snapshot taken before settling can
// cancel only the run that held the lapsed lease — a fresh attempt that
// already re-registered under the same task id survives. A skipped settle is
// the exception: the row moved under a concurrent transition, so the kill
// only follows a verdict already closed (or the row deleted) — a row that
// re-entered an active state on a fresh lease is a live run and is left
// alone.
func (c *Core) settleExpired(ctx context.Context, r ExpiredRow) string {
	snap, wasRunning := c.running.Load(r.TaskID)
	// Capture the lapsed attempt's dispatch target BEFORE settling: a settle
	// re-queues and the re-dispatch rewrites the target record, so the
	// executor holding the dead attempt is unfindable by the time its
	// stand-down order would go out (review P1-4).
	target, terr := c.store.DispatchTarget(ctx, r.TaskID)
	if terr != nil {
		c.logger.Debug("expire: dispatch target lookup", "task", r.TaskID, "err", terr)
	}

	outcome, err := c.store.SettleExpired(ctx, r, c.nodeID)
	if err != nil {
		c.logger.Warn("expire task", "task", r.TaskID, "err", err)
		return ""
	}
	// Whether the scanned attempt is actually dead decides both cleanups
	// below. A settled outcome says so outright; a skipped settle means a
	// concurrent transition owns the row, so read it: a rotated attempt, a
	// non-executing state, or a vanished row all leave the executor's copy
	// a zombie that must stand down — but a row still AT the scanned
	// attempt is live custody (its lease merely renewed against the scan)
	// and a stand-down order there would kill a run we just agreed to keep.
	tk, gerr := c.store.Get(ctx, r.TaskID)
	attemptDead := outcome == ExpiryRerouted || outcome == ExpiryFailed ||
		gerr != nil || !execAlive(tk.State) ||
		(r.AttemptID != "" && tk.AttemptID != r.AttemptID)
	if attemptDead {
		// Without this the remote agent keeps burning tokens and writing
		// files under a task this node has already replaced — work that the
		// re-route would duplicate.
		c.forwardCancelTo(ctx, target, r.TaskID, r.AttemptID, "lease expired")
	}
	if attemptDead {
		// A task that timed out while paused in waiting_context would
		// otherwise leak its entry in pendingCtx (P2-7) — and with the
		// entry, the capacity reservation it carries. The drop is
		// attempt-scoped: a row that already rotated re-created its park
		// under the fresh dispensation, and that entry is not ours to free.
		c.dropPendingContextAttempt(r.TaskID, r.AttemptID)
		if wasRunning {
			if h, ok := snap.(*runningHandle); ok && c.running.CompareAndDelete(r.TaskID, snap) {
				h.cancel()
				c.logger.Info("aborted expired local execution", "task", r.TaskID)
			}
		}
	}
	switch outcome {
	case ExpiryRerouted:
		// The task lives on under a fresh attempt — tell the upstream copy
		// so its own lapsed lease renews instead of cascading the same expiry
		// one hop up. relayToParent no-ops for a root task.
		if tk, err := c.store.Get(ctx, r.TaskID); err == nil {
			note := "executor lease expired — re-queued for re-route"
			if rerr := c.store.RecordEvent(ctx, r.TaskID, EvProgress,
				map[string]any{"note": note}); rerr == nil {
				c.relayToParent(ctx, bus.MsgTaskProgress, tk.Chain,
					bus.TaskProgressPayload{TaskID: r.TaskID, AttemptID: tk.AttemptID, Note: note})
			}
		}
	case ExpiryFailed:
		// Propagate the terminal verdict up the delegation chain so a root
		// scheduler blocked in Submit unblocks (D3). relayToParent is a no-op
		// for a root task; signalResult no-ops without a waiter.
		if tk, err := c.store.Get(ctx, r.TaskID); err == nil {
			// The wire state mirrors the row; the reason follows the expired
			// arm, not the final state — a running row killed by its deadline
			// lands on 'failed' but its verdict is still the deadline.
			state, stderr := tk.State, "lease expired"
			if state == StateExpired || r.Deadline {
				stderr = "deadline exceeded"
			}
			if state != StateExpired {
				state = StateFailed
			}
			res := bus.TaskResultPayload{
				TaskID: r.TaskID, AttemptID: tk.AttemptID, State: state, OK: false, ExitCode: 1, Stderr: stderr,
				Chain: tk.Chain,
			}
			c.relayToParent(ctx, bus.MsgTaskResult, tk.Chain, res)
			c.signalResult(r.TaskID, res)
			c.reportLateChildResult(ctx, r.TaskID)
		}
	}
	return outcome
}

// reconcileLocalWork stops this process's own executions whose task rows left
// the active states out of band — a `panda cancel`, a console reject, a lease
// expiry claimed by another process's monitor, a review park written from
// elsewhere. The store row is the truth: once it no longer says the task is
// executing, the subprocess here is a zombie still burning tokens and writing
// files under a verdict nobody will read.
//
// execAlive reports whether the row's state means an execution can be live:
// the registerRunning window spans dispatch through run, so dispatched and
// waiting_context count alongside running. Anything else — queued, parked,
// every closed verdict — means the row moved on without this process.
func execAlive(state string) bool {
	switch state {
	case StateDispatched, StateWaitingCtx, StateRunning:
		return true
	}
	return false
}

func (c *Core) reconcileLocalWork(ctx context.Context) {
	c.running.Range(func(k, v any) bool {
		id, ok := k.(string)
		if !ok {
			return true
		}
		t, err := c.store.Get(ctx, id)
		if err == nil && !execAlive(t.State) {
			c.logger.Info("reconcile: stopping orphaned execution", "task", id, "state", t.State)
			// Compare-and-delete the ranged handle, not a blind cancelRunning:
			// a rerouted row may have re-registered a fresh run between our
			// Get and here — that run's verdict is not ours to take.
			if c.running.CompareAndDelete(id, v) {
				if h, ok := v.(*runningHandle); ok {
					h.cancel()
				}
				c.dropPendingContext(id)
			}
		}
		return true
	})
	c.pendingCtx.Range(func(k, _ any) bool {
		id, ok := k.(string)
		if !ok {
			return true
		}
		t, err := c.store.Get(ctx, id)
		if err == nil && !execAlive(t.State) {
			c.dropPendingContext(id)
		}
		return true
	})
}

// Shutdown marks the node offline and closes peers.
func (c *Core) Shutdown(ctx context.Context) {
	c.artMu.Lock()
	if !c.artClosing && c.workStop != nil {
		close(c.workStop)
	}
	c.artClosing = true
	c.artMu.Unlock()
	c.stopReachability(ctx)
	c.mu.Lock()
	for id, p := range c.peers {
		_ = p.conn.Close()
		delete(c.peers, id)
	}
	c.mu.Unlock()
	c.udpMu.Lock()
	if c.udp != nil {
		_ = c.udp.Close()
		c.udp = nil
	}
	c.udpMu.Unlock()
	// Only the process that owns the row may retire it: a borrowed engine
	// (CLI sharing the daemon's node id) must leave liveness to the owner's
	// heartbeat, or every CLI exit flips a running node offline.
	if c.ownsNodeRow {
		c.node.Shutdown(ctx)
	}
	// Detached adoptions outlive the result envelope by design; a daemon
	// exiting mid-extract would leave a worktree half-applied. Transfers
	// carry their own budget, so this drain is bounded — and the caller's
	// shutdown deadline binds it too: a wedged transfer must not stretch a
	// stop past the point the caller committed to, or the supervisor would
	// SIGKILL at its own timeout with nothing logged.
	done := make(chan struct{})
	go func() {
		c.workWG.Wait()
		c.artWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		c.logger.Warn("background work still in flight at shutdown deadline", "err", ctx.Err())
	}
}

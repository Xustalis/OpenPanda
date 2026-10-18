// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/scheduler/queue"
)

// Execution leases close the gap between "the queue registry orders starts in
// this process" and "the worktree or device admits one writer, period".
//
// The registry (queue.ResourceRegistry) is deliberately per-process: it
// decides WHEN a queued task starts. But three execution paths never pass
// through it — inbound delegated tasks run straight through
// handleLocalDelegate -> execute, inline submits run through runLocal, and a
// second core process on the same host (a daemon plus an embedded REPL/panel
// core) has a registry of its own. Two same-project tasks that arrive by
// different paths (a queued task this node owns and a delegated task that
// routed here because this node holds the project checkout) would therefore
// edit one worktree concurrently, and two processes could drive one physical
// actuator at once — the exact faults the project key and the actuator lock
// exist to prevent, only shifted across paths or process boundaries.
//
// exec_locks is the same lock table all paths converge on. Every execution
// funnels through claimForRun (runLocal, delegated accepts, resumes, and the
// queue runner's scheduler all call run), so that is where the task takes its
// keys in the shared database — inside the same BEGIN IMMEDIATE transaction
// every writer uses, making the check-and-take atomic across processes.
// Contention blocks (like the actuator lock), not declines: a sibling
// finishing on the same resource keeps the waiting row in dispatched instead
// of bouncing it into re-routing. The wait tracks the child-task protocol of
// plan.delegateChildren: the holder releases its worktree keys for the
// fan-out and re-takes them before touching the worktree again, so a parent
// waiting on a child that inherits its project key cannot deadlock. The
// actuator key is NOT part of that release: the parent keeps the device for
// the whole round, exactly as it keeps the in-process actuator mutex, so a
// task that needs the device can never take the parent's worktree key and
// then block on the mutex while the parent blocks on the worktree key.
//
// Lock rows die with the run that owns them, twice over. The exec_locks_gc
// trigger (migration 48) deletes a task's rows in the same transaction as any
// UPDATE that moves its row out of the executing states — every terminal
// mark, review park, requeue and cancel releases the locks atomically with
// the transition, no matter which code path wrote it. The steal check in
// TryAcquireExecLocks is the remaining backstop for the one window the
// trigger cannot see: a holder whose process died between acquiring and the
// transition that would have fired (or whose unlock write failed) — its rows
// are stolen once the task row is no longer live (missing, terminal, parked
// in review, or past its lease). See TryAcquireExecLocks.
//
// Every acquisition also stamps its rows with a per-run nonce (run_id,
// migration 49) that every release predicates on. That matters at exactly
// one moment: a run's deferred unlock fires after its terminal transition —
// by then the trigger has freed its rows and a re-execution of the SAME task
// may already hold fresh rows under the same task_id. An unkeyed delete by
// task alone would drop the new run's rows out from under it; keyed on
// run_id, the stale release matches nothing and the new run is untouched.

// execLockPollEvery is how often a run blocked on a foreign execution lease
// re-checks the table. The work behind the lock is a whole agent run (minutes
// at least), so a fifth of a second costs nothing, and 2-3 nodes on one host
// never contend hot. Acquisitions inside one process are additionally
// serialized by execAcquireMu, so polling never races itself.
const execLockPollEvery = 200 * time.Millisecond

// errExecLockBusy is the in-transaction signal that a key is held by a live
// run; it never leaves TryAcquireExecLocks (which converts it to false).
var errExecLockBusy = errors.New("exec lock busy")

// execLockKeys returns the resource keys a task's execution occupies on this
// node: the same set the queue registry orders on (explicit keys, else the
// project, else the shared anonymous lock), plus the resolved physical
// actuator. The actuator is added here because a delegated actuator task
// carries no queue keys at all, yet its driver must still exclude every other
// driver for the same device — the in-process actuatorLocks mutex covers one
// process, this key carries the exclusion to the rest.
func execLockKeys(task Task, actuatorID string) []string {
	keys := slices.Clone(queue.DefaultKeys(task.ResourceKeys, task.Project))
	if actuatorID != "" && !slices.Contains(keys, actuatorID) {
		keys = append(keys, actuatorID)
	}
	return keys
}

// TryAcquireExecLocks takes the exec_locks rows for every key, all-or-nothing,
// on behalf of taskID. A false return means at least one key is held by a
// live run; the caller polls or gives up when its context ends. Because the
// whole check-and-take runs in one BEGIN IMMEDIATE transaction, two processes
// racing for the same key cannot both win.
//
// "Live" is judged from the holder's task row: a holder is live while its row
// is mid-execution (dispatched owns the pre-accept window; running and
// waiting_context the run itself) with a lease that has not lapsed. Anything
// else — a missing row, a terminal state, a review park, a queued row with no
// lease, or a lease in the past — is the leftover of a crashed or finished
// run whose rows were never released, and its keys are stolen rather than
// allowed to wedge the resource. This applies to rows held by taskID itself
// too: a restarted task takes over its dead predecessor's rows this way.
func (s *TaskStore) TryAcquireExecLocks(ctx context.Context, taskID, nodeID, runID string, keys []string) (bool, error) {
	if len(keys) == 0 {
		return true, nil
	}
	now := s.now()
	acquired := false
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		acquired = false
		for _, key := range keys {
			var holder string
			err := tx.QueryRowContext(ctx, `SELECT task_id FROM exec_locks WHERE key=?`, key).Scan(&holder)
			if errors.Is(err, sql.ErrNoRows) {
				continue // free
			}
			if err != nil {
				return fmt.Errorf("read exec lock %q: %w", key, err)
			}
			live, err := liveExecHolder(ctx, tx, holder, now)
			if err != nil {
				return err
			}
			if live {
				return errExecLockBusy
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM exec_locks WHERE key=? AND task_id=?`, key, holder); err != nil {
				return fmt.Errorf("steal exec lock %q from %s: %w", key, holder, err)
			}
		}
		for _, key := range keys {
			res, err := tx.ExecContext(ctx, `INSERT INTO exec_locks(key, task_id, node_id, acquired_at, run_id) VALUES(?,?,?,?,?)
				ON CONFLICT(key) DO UPDATE SET node_id=excluded.node_id, acquired_at=excluded.acquired_at, run_id=excluded.run_id
				WHERE exec_locks.task_id = excluded.task_id`,
				key, taskID, nodeID, now, runID)
			if err != nil {
				return fmt.Errorf("take exec lock %q: %w", key, err)
			}
			// The WHERE lets a foreign-held conflict degrade into a silent
			// no-op — BEGIN IMMEDIATE already serialized the check-then-take
			// so this "cannot happen", and if it ever does the caller must
			// NOT believe the key is held.
			if n, rerr := res.RowsAffected(); rerr == nil && n == 0 {
				return fmt.Errorf("take exec lock %q: conflict with a foreign row the liveness check missed", key)
			}
		}
		acquired = true
		return nil
	})
	if errors.Is(err, errExecLockBusy) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return acquired, nil
}

// ReleaseExecLocks drops every exec_locks row held by taskID for this run.
// Release is by owner id AND run nonce, not by key set: a key the task no
// longer holds was stolen after going stale and must not be deleted from
// under its new owner — and neither may the fresh rows a re-execution of the
// same task took under a different run_id (see the file header).
func (s *TaskStore) ReleaseExecLocks(ctx context.Context, taskID, runID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM exec_locks WHERE task_id=? AND run_id=?`, taskID, runID)
	return err
}

// ReleaseExecLocksKeys drops just the named rows held by this run — the
// delegate wait frees its worktree keys while the actuator key (and anything
// else outside the set) stays held for the run.
func (s *TaskStore) ReleaseExecLocksKeys(ctx context.Context, taskID, runID string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")
	args := make([]any, 0, len(keys)+2)
	args = append(args, taskID, runID)
	for _, k := range keys {
		args = append(args, k)
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM exec_locks WHERE task_id=? AND run_id=? AND key IN (`+placeholders+`)`, args...)
	return err
}

// liveExecHolder reports whether the holder task is currently executing on
// some process (see TryAcquireExecLocks for the exact predicate).
func liveExecHolder(ctx context.Context, tx *sql.Tx, taskID string, now int64) (bool, error) {
	var state string
	var lease sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT state, lease_expires_at FROM tasks WHERE task_id=?`, taskID).Scan(&state, &lease)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read exec lock holder %s: %w", taskID, err)
	}
	switch state {
	case StateDispatched, StateRunning, StateWaitingCtx:
	default:
		return false, nil
	}
	if lease.Valid && lease.Int64 > 0 && lease.Int64 < now {
		return false, nil // lease lapsed: the holder's process is gone
	}
	return true, nil
}

// execHoldEntry is what a run's execHold marker records: hold keys are the
// worktree subset the delegate wait releases and re-takes, and runID is the
// nonce stamped on this acquisition's rows — the predicate every release
// carries so a stale release can only ever match this run's own rows.
type execHoldEntry struct {
	keys  []string
	runID string
}

// execLockAcquire blocks until the task's execution keys are held in
// exec_locks, or ctx ends. runID is the nonce stamped on the rows this call
// takes; one run keeps a single nonce across its acquire, the delegate
// dance's re-acquires and all releases, so rows written mid-run remain
// attributable to it. dupCheck makes it a duplicate-invocation guard: an
// in-process acquisition or run for the same task (an execHold entry) yields
// ErrAlreadyRunning instead of polling, because a second invocation must
// stand down, never steal a live run's rows. hold is what the entry records —
// the worktree subset the delegate wait releases and re-takes (the actuator
// key stays held across a fan-out: the in-process actuator mutex is held
// across it too, and a child needing the same device must wait for the
// round, exactly as it waits on the mutex). The re-acquire after that wait
// (execAcquireFor) skips the dup check — it is the owning run re-taking its
// own keys.
func (c *Core) execLockAcquire(ctx context.Context, taskID, runID string, keys, hold []string, dupCheck bool) error {
	if len(keys) == 0 {
		return nil
	}
	if dupCheck {
		if _, ok := c.execHold.Load(taskID); ok {
			return ErrAlreadyRunning
		}
	}
	tick := time.NewTicker(execLockPollEvery)
	defer tick.Stop()
	for {
		c.execAcquireMu.Lock()
		already := false
		var err error
		var ok bool
		if dupCheck {
			_, already = c.execHold.Load(taskID)
		}
		if !already {
			ok, err = c.store.TryAcquireExecLocks(ctx, taskID, c.nodeID, runID, keys)
			if err == nil && ok {
				c.execHold.Store(taskID, execHoldEntry{keys: slices.Clone(hold), runID: runID})
			}
		}
		c.execAcquireMu.Unlock()
		if already {
			return ErrAlreadyRunning
		}
		if err != nil {
			return fmt.Errorf("acquire exec locks: %w", err)
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// execReleaseFor drops taskID's worktree exec_locks rows for a wait — the
// delegate fan-out, whose children inherit the parent's project key — and
// returns the keys so the caller can re-acquire before the round resumes.
// Mirror of queueReleaseFor. The actuator row, if any, is deliberately NOT
// released (see execLockAcquire). The execHold entry stays in place: it marks
// the run as in flight for the duplicate guard, which the wait must not
// reopen. False when this process holds nothing for the task, and also when
// the release write failed: the rows are then still ours (no one else can
// take a live task's locks), so the wait proceeds without pretending they
// were freed.
func (c *Core) execReleaseFor(taskID string) ([]string, bool) {
	raw, ok := c.execHold.Load(taskID)
	if !ok {
		return nil, false
	}
	entry, _ := raw.(execHoldEntry)
	wCtx, cancel := c.storeWriteCtx(context.Background())
	defer cancel()
	if err := c.store.ReleaseExecLocksKeys(wCtx, taskID, entry.runID, entry.keys); err != nil {
		c.logger.Warn("release exec locks for delegate wait", "task", taskID, "err", err)
		return nil, false
	}
	return entry.keys, true
}

// execAcquireFor re-takes the keys after the wait under the run's original
// nonce (the execHold entry survives the dance exactly so it can be read
// back here), blocking like the initial acquisition — the round is about to
// touch the worktree again. A failure — only ctx ending — is logged by the
// caller: the run is already in flight and killing it over a lock write
// would lose work the user asked for.
func (c *Core) execAcquireFor(ctx context.Context, taskID string, keys []string) error {
	runID := ""
	if raw, ok := c.execHold.Load(taskID); ok {
		if entry, _ := raw.(execHoldEntry); entry.runID != "" {
			runID = entry.runID
		}
	}
	if runID == "" {
		var err error
		if runID, err = newUUID(); err != nil {
			return fmt.Errorf("acquire exec locks: %w", err)
		}
	}
	return c.execLockAcquire(ctx, taskID, runID, keys, keys, false)
}

// execLockUnlock drops the task's exec_locks rows at the end of a run. The
// marker is LoadAndDelete'd so a second deferred unlock is a no-op, and the
// release predicates on the run nonce recorded at acquisition: by the time
// this deferred call fires, the task's terminal transition has usually
// already freed the rows via the gc trigger and a re-execution may hold
// fresh rows under the same task_id — an unkeyed delete would drop those.
// The write runs on a detached context: the run's ctx may be dead (cancel)
// exactly when this cleanup matters most. Best effort by design: a failed
// delete leaves a stale row that the next acquirer steals once the task row
// is terminal or the lease lapses, so a hiccup can slow a resource down but
// never wedge it.
func (c *Core) execLockUnlock(taskID string) {
	raw, ok := c.execHold.LoadAndDelete(taskID)
	if !ok {
		return // this process holds nothing for the task
	}
	entry, _ := raw.(execHoldEntry)
	wCtx, cancel := c.storeWriteCtx(context.Background())
	defer cancel()
	if err := c.store.ReleaseExecLocks(wCtx, taskID, entry.runID); err != nil {
		c.logger.Warn("release exec locks", "task", taskID, "err", err)
	}
}

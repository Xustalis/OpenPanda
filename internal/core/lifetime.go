// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
)

// leaseRenewDivisor sets the heartbeat period as lease/N: renewing three times
// per lease window survives one missed tick (a GC pause, a busy SQLite writer)
// without the monitor seeing an expired lease.
const leaseRenewDivisor = 3

// minLeaseRenewInterval floors the heartbeat so a deliberately tiny test lease
// cannot spin the renewal goroutine.
const minLeaseRenewInterval = 200 * time.Millisecond

// SetTimeouts applies the configured execution timeouts: the agent-adapter
// budget (process-global, in commander) and this node's task lease.
//
// It enforces the ordering the two constants used to violate: the lease is
// raised, if necessary, to sit clearly past the agent's hard wall-clock limit.
// With a 600s adapter budget (630s hard limit) against a 600s lease, every long
// agent run outlived its own lease — the monitor force-failed it, the parent
// re-routed the same work to another node, and two agents then edited the same
// repository at once. The lease bounds *silence* from an executor, not total
// runtime: renewLease heartbeats it while execution is live.
func (c *Core) SetTimeouts(t config.TimeoutsConfig) {
	if t.AgentS > 0 && t.AgentS < 60 {
		// commander.SetAgentTimeout silently drops sub-minute budgets as
		// misconfiguration — say so, or the operator tunes a 45s agent cap,
		// sees it never fire, and files it as a scheduler bug.
		c.logger.Warn("agent_s under one minute ignored",
			"configured", t.AgentTimeout(), "floor", time.Minute)
	}
	commander.SetAgentTimeout(t.AgentTimeout())
	commander.SetSilenceTimeout(t.SilenceTimeout())
	// Native-command budget (timeouts.native_s). Zero keeps the executor's
	// own default, so a config that never names the key is unchanged.
	commander.SetNativeTimeout(t.NativeTimeout())
	lease := t.TaskLease()
	if floor := commander.AgentHardTimeout() * 2; lease < floor {
		// Info, not Warn: every CLI invocation builds a Core-shaped object and
		// stderr defaults to warn-and-up, so the auto-correction notice ended
		// up on `panda task add`/`ask` output — daemon logs still show it.
		c.logger.Info("task lease raised above the agent hard timeout",
			"configured", lease, "using", floor)
		lease = floor
	}
	c.mu.Lock()
	c.leaseTimeout = lease
	c.agentByKind = t.AgentByKind
	// §6.1 quota for locally-created top-level tasks. TokenBudget() resolves
	// an explicit disable (-1) to 0 here so createTask's "stamp when >0" test
	// is the only branch the write path needs.
	if tb := t.TokenBudget(); tb > 0 {
		c.taskTokenBudget = tb
	} else {
		c.taskTokenBudget = 0
	}
	c.mu.Unlock()
	c.SetSuperviseRounds(t.Rounds())
}

// agentTimeoutForKind returns the per-kind agent timeout when configured,
// falling back to the global agent timeout. executeTaskRound calls this to
// give training stages a larger budget without inflating the default.
func (c *Core) agentTimeoutForKind(kind string) time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.agentByKind != nil {
		if s, ok := c.agentByKind[kind]; ok && s > 0 {
			return time.Duration(s) * time.Second
		}
	}
	return 0 // 0 means "use the global default"
}

// lease returns this node's task lease duration.
func (c *Core) lease() time.Duration {
	c.mu.RLock()
	d := c.leaseTimeout
	c.mu.RUnlock()
	if d <= 0 {
		return defaultDelegateTimeout
	}
	return d
}

// runningHandle makes an execution's cancel func pointer-comparable, so the
// release path and a snapshot cancel both target exactly the run they saw.
// An unconditional Delete would let a dying run's late release erase the
// binding a re-registered run (a re-routed attempt's fresh execution) just
// stored — CompareAndDelete on the handle itself cannot.
type runningHandle struct{ cancel context.CancelFunc }

// registerRunning binds taskID to the CancelFunc of the context its execution
// runs under and returns a release func the caller must defer. While bound,
// cancelRunning can actually stop the work.
func (c *Core) registerRunning(taskID string, cancel context.CancelFunc) func() {
	h := &runningHandle{cancel: cancel}
	c.running.Store(taskID, h)
	return func() { c.running.CompareAndDelete(taskID, h) }
}

// cancelRunning aborts local execution of taskID, reporting whether this node
// was in fact running it. This is what makes a force-fail real: without it,
// ExpireTasks and CancelCascade only rewrite database rows while the agent
// subprocess keeps writing files and committing code under a task that has
// already been reported failed upstream.
func (c *Core) cancelRunning(taskID string) bool {
	v, ok := c.running.LoadAndDelete(taskID)
	if !ok {
		return false
	}
	h, ok := v.(*runningHandle)
	if !ok {
		return false
	}
	h.cancel()
	c.logger.Info("aborted local execution", "task", taskID)
	return true
}

// renewLease keeps taskID's lease ahead of the wall clock for as long as its
// execution is live, and returns a stop func the caller must defer. A task that
// legitimately runs for an hour (a training stage, a long agent session) is
// then never mistaken for a dead executor, while a process that actually dies
// stops renewing and expires within one lease window.
//
// Each tick also beats task_progress up the delegation chain: the delegator's
// own copy of the task carries a lease too, stamped once at dispatch, so
// without the beat the *origin* node would expire a task its executor is still
// working on — and re-route the same work to a second node.
//
// Renewal deliberately uses a context detached from execution: the point is to
// keep writing while the task's own context is alive, and to stop the moment
// the caller releases it.
func (c *Core) renewLease(ctx context.Context, taskID string, chain []string, attemptID string) func() {
	lease := c.lease()
	interval := lease / leaseRenewDivisor
	if interval < minLeaseRenewInterval {
		interval = minLeaseRenewInterval
	}
	// Stamp one immediately so a locally-submitted task (which never went
	// through dispatchDelegated) is covered by the monitor from the start.
	c.setLease(ctx, taskID, lease)

	done := make(chan struct{})
	var once sync.Once
	c.goGuarded("lease renew "+taskID, func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				// The owner may have called the stop function while the
				// previous write was blocked. Re-check before touching the
				// store again rather than waiting for the next tick.
				select {
				case <-done:
					return
				default:
				}
				c.setLease(ctx, taskID, lease)
				c.relayToParent(context.WithoutCancel(ctx), bus.MsgTaskProgress, chain,
					bus.TaskProgressPayload{TaskID: taskID, AttemptID: attemptID})
			}
		}
	})
	return func() { once.Do(func() { close(done) }) }
}

// leaseWriteTimeout bounds a single lease renewal. A SQLite write can block
// for as long as another process holds the write lock; without a deadline the
// renewal goroutine stops writing while the monitor still sees a live task,
// the expired lease is read as a dead executor, and the same work is routed
// to a second node. Bounding the write lets the next ticker tick renew.
const leaseWriteTimeout = 5 * time.Second

// setLease writes one lease renewal, tolerating a task that has already reached
// a terminal state (the write simply matches no active row).
func (c *Core) setLease(ctx context.Context, taskID string, lease time.Duration) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaseWriteTimeout)
	defer cancel()
	if err := c.store.SetLease(writeCtx, taskID, lease.Milliseconds()); err != nil {
		c.logger.Warn("renew lease", "task", taskID, "err", err)
	}
}

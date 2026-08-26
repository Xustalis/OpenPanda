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
	commander.SetAgentTimeout(t.AgentTimeout())
	lease := t.TaskLease()
	if floor := commander.AgentHardTimeout() * 2; lease < floor {
		c.logger.Warn("task lease raised above the agent hard timeout",
			"configured", lease, "using", floor)
		lease = floor
	}
	c.mu.Lock()
	c.leaseTimeout = lease
	c.mu.Unlock()
	c.SetSuperviseRounds(t.Rounds())
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

// registerRunning binds taskID to the CancelFunc of the context its execution
// runs under and returns a release func the caller must defer. While bound,
// cancelRunning can actually stop the work.
func (c *Core) registerRunning(taskID string, cancel context.CancelFunc) func() {
	c.running.Store(taskID, cancel)
	return func() { c.running.Delete(taskID) }
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
	cancel, ok := v.(context.CancelFunc)
	if !ok {
		return false
	}
	cancel()
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
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				c.setLease(ctx, taskID, lease)
				c.relayToParent(context.WithoutCancel(ctx), bus.MsgTaskProgress, chain,
					bus.TaskProgressPayload{TaskID: taskID, AttemptID: attemptID})
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}

// setLease writes one lease renewal, tolerating a task that has already reached
// a terminal state (the write simply matches no active row).
func (c *Core) setLease(ctx context.Context, taskID string, lease time.Duration) {
	if err := c.store.SetLease(context.WithoutCancel(ctx), taskID, lease.Milliseconds()); err != nil {
		c.logger.Warn("renew lease", "task", taskID, "err", err)
	}
}

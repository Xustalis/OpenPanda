package core

import (
	"context"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// sleepCard is a node whose one ability outlives a short lease, so a test can
// observe what happens to a task that legitimately runs past its lease window.
func sleepCard(seconds string) ledger.Card {
	return ledger.Card{
		Device:        "worker",
		ResourceClass: "Standard",
		Native:        []ledger.NativeAbility{{ID: "sys:sleep", Command: "sleep", Args: []string{seconds}}},
		Capacity:      ledger.Capacity{CPUCores: 2, RAMGB: 4, MaxConcurrent: 2},
	}
}

// TestLeaseRenewalOutlivesShortLease is the regression for the structural bug
// that made long delegated work impossible: the lease (600s) was shorter than
// one agent execution's own hard limit (630s) and was never renewed, so the
// monitor force-failed work that was still running and the parent re-routed the
// same job to a second node. With heartbeat renewal a task that runs several
// lease windows long finishes normally.
func TestLeaseRenewalOutlivesShortLease(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	c := NewCore(db, "worker", sleepCard("2"), 5, testLogger(), config.ModelConfig{})
	// A lease far shorter than the task: renewal must carry it anyway.
	c.leaseTimeout = 600 * time.Millisecond

	// Run the expiry monitor concurrently, exactly as the daemon does.
	monCtx, stop := context.WithCancel(ctx)
	defer stop()
	go c.RunMonitor(monCtx)

	_, res, err := c.SubmitLocal(ctx, TaskInput{
		Title:    "long native task",
		Intent:   "sleep",
		Requires: []string{"sys:sleep"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res.State != StateDone {
		t.Fatalf("task state = %s (%s), want done — a renewed lease must not expire under a live task",
			res.State, res.Stderr)
	}
}

// TestLeaseStopsRenewingAfterExecution verifies the other half: renewal is tied
// to the execution, so a dead executor still expires. Once the task's own
// context is released the heartbeat stops and the lease ages out.
func TestLeaseStopsRenewingAfterExecution(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	c := NewCore(db, "worker", sleepCard("0"), 5, testLogger(), config.ModelConfig{})
	c.leaseTimeout = 400 * time.Millisecond

	tk := createTask(t, c.store, "", "abandoned", "worker")
	if err := c.store.Queue(ctx, tk.TaskID, "worker"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.Dispatch(ctx, tk.TaskID, "worker", "worker"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := c.store.Accept(ctx, tk.TaskID, "worker"); err != nil {
		t.Fatalf("accept: %v", err)
	}

	execCtx, cancel := context.WithCancel(ctx)
	stopRenew := c.renewLease(execCtx, tk.TaskID, []string{"worker"}, tk.AttemptID)

	// While renewal runs the lease stays in the future.
	time.Sleep(500 * time.Millisecond)
	if expired, err := c.store.ExpireTasks(ctx); err != nil {
		t.Fatalf("expire: %v", err)
	} else if len(expired) != 0 {
		t.Fatalf("live task expired while its lease was being renewed: %v", expired)
	}

	// The executor dies: renewal stops and the lease ages out.
	stopRenew()
	cancel()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		expired, err := c.store.ExpireTasks(ctx)
		if err != nil {
			t.Fatalf("expire: %v", err)
		}
		if len(expired) == 1 && expired[0] == tk.TaskID {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("abandoned task never expired after renewal stopped")
}

// awaitRunning waits for exactly one task to register itself in the cancel
// registry and returns its id, so a test can act on live execution.
func awaitRunning(t *testing.T, c *Core) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var id string
		c.running.Range(func(k, _ any) bool {
			id, _ = k.(string)
			return false
		})
		if id != "" {
			return id
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no task registered as running")
	return ""
}

// TestCancelAbortsRunningSubprocess pins the cancel registry on the operator
// path. Before it existed, CancelCascade only rewrote database rows: the agent
// subprocess kept writing files and committing code under a task already
// reported cancelled upstream.
func TestCancelAbortsRunningSubprocess(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	c := NewCore(db, "worker", sleepCard("60"), 5, testLogger(), config.ModelConfig{})
	c.leaseTimeout = 10 * time.Minute // long: only the explicit cancel ends this

	done := make(chan string, 1)
	go func() {
		_, res, err := c.SubmitLocal(ctx, TaskInput{
			Title:    "abortable",
			Intent:   "sleep",
			Requires: []string{"sys:sleep"},
		})
		if err != nil {
			done <- "submit error: " + err.Error()
			return
		}
		done <- res.State
	}()

	taskID := awaitRunning(t, c)
	// Give the subprocess time to actually spawn, so a passing test really does
	// mean the kill landed on a live `sleep 60` rather than on a pre-exec race.
	time.Sleep(500 * time.Millisecond)
	start := time.Now()
	if err := c.CancelTree(ctx, taskID); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	select {
	case state := <-done:
		// `sleep 60` would still be running; returning promptly proves the
		// subprocess was killed rather than waited out.
		if elapsed := time.Since(start); elapsed > 15*time.Second {
			t.Fatalf("cancel took %v; the subprocess was not killed", elapsed)
		}
		if state == StateDone {
			t.Fatalf("cancelled task reported done")
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("cancelled task never returned: the subprocess outlived its cancel")
	}

	final, err := c.store.Get(ctx, taskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if final.State != StateCancelled {
		t.Fatalf("state = %s, want cancelled", final.State)
	}
}

// TestMonitorExpiryAbortsRunningExecution covers the same registry on the lease
// monitor's path, which is where the duplicate-execution bug actually bit: the
// monitor force-failed a task whose executor was still working, the parent
// re-routed the same job, and two agents edited one repository at once. A
// force-fail must cancel the execution context it force-fails.
func TestMonitorExpiryAbortsRunningExecution(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	c := NewCore(db, "worker", sleepCard("0"), 5, testLogger(), config.ModelConfig{})

	tk := createTask(t, c.store, "", "expiring", "worker")
	if err := c.store.Queue(ctx, tk.TaskID, "worker"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.Dispatch(ctx, tk.TaskID, "worker", "worker"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := c.store.Accept(ctx, tk.TaskID, "worker"); err != nil {
		t.Fatalf("accept: %v", err)
	}
	// The shortest lease the store accepts (it rounds up to a whole second):
	// by the monitor's first tick the executor looks dead, while its execution
	// context is still registered and live.
	if err := c.store.SetLease(ctx, tk.TaskID, 1); err != nil {
		t.Fatalf("set lease: %v", err)
	}

	execCtx, cancelExec := context.WithCancel(ctx)
	defer cancelExec()
	release := c.registerRunning(tk.TaskID, cancelExec)
	defer release()

	monCtx, stop := context.WithCancel(ctx)
	defer stop()
	go c.RunMonitor(monCtx)

	select {
	case <-execCtx.Done():
	case <-time.After(20 * time.Second):
		t.Fatalf("monitor force-failed the task but left its execution running")
	}

	final, err := c.store.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if final.State != StateFailed {
		t.Fatalf("state = %s, want failed after a force-fail on lease expiry", final.State)
	}
}

// TestProgressBeatRefreshesDelegatorLease covers the other half of the renewal
// story. A delegator stamps its own copy of the task with one lease at dispatch
// and never touches it again, so without an upstream beat the *origin* node
// expires a task its executor is still working on — and re-routes the same work
// to a second node. task_progress is that beat.
func TestProgressBeatRefreshesDelegatorLease(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	c := NewCore(db, "root", ledger.Card{}, 10, testLogger(), config.ModelConfig{})
	c.leaseTimeout = 30 * time.Minute

	tk := createTask(t, c.store, "", "delegated", "root")
	if err := c.store.Queue(ctx, tk.TaskID, "root"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.Dispatch(ctx, tk.TaskID, "root", "worker"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// The single short lease a dispatch leaves behind.
	if err := c.store.SetLease(ctx, tk.TaskID, 1000); err != nil {
		t.Fatalf("set lease: %v", err)
	}
	before, err := c.store.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	beat := func(from string) bus.Envelope {
		env, err := bus.NewEnvelope(bus.MsgTaskProgress, from, "msg-"+from,
			bus.TaskProgressPayload{TaskID: tk.TaskID, AttemptID: before.AttemptID})
		if err != nil {
			t.Fatalf("envelope: %v", err)
		}
		return env
	}

	// A beat from an authenticated peer that is not the executor must not hold
	// another node's task open: same rule as accept/decline/result.
	c.handleProgress(ctx, beat("intruder"))
	if got, err := c.store.Get(ctx, tk.TaskID); err != nil {
		t.Fatalf("get: %v", err)
	} else if got.LeaseExpires != before.LeaseExpires {
		t.Fatalf("lease moved from %d to %d on a beat from a non-executor",
			before.LeaseExpires, got.LeaseExpires)
	}

	// The real executor's beat carries the lease forward by a full window.
	c.handleProgress(ctx, beat("worker"))
	after, err := c.store.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.LeaseExpires <= before.LeaseExpires {
		t.Fatalf("lease = %d, want later than the dispatch-time %d",
			after.LeaseExpires, before.LeaseExpires)
	}

	// A beat from a superseded attempt must not extend the current one.
	stale, err := bus.NewEnvelope(bus.MsgTaskProgress, "worker", "msg-stale",
		bus.TaskProgressPayload{TaskID: tk.TaskID, AttemptID: "some-older-attempt"})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if err := c.store.SetLease(ctx, tk.TaskID, 1000); err != nil {
		t.Fatalf("set lease: %v", err)
	}
	pinned, err := c.store.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	c.handleProgress(ctx, stale)
	if got, err := c.store.Get(ctx, tk.TaskID); err != nil {
		t.Fatalf("get: %v", err)
	} else if got.LeaseExpires != pinned.LeaseExpires {
		t.Fatalf("lease moved from %d to %d on a beat from a superseded attempt",
			pinned.LeaseExpires, got.LeaseExpires)
	}
}

// TestSetTimeoutsKeepsLeaseAboveAgentLimit pins the ordering invariant. A lease
// at or below the agent's own hard wall-clock limit guarantees that every long
// agent run is force-failed mid-flight, so the configured value is raised.
func TestSetTimeoutsKeepsLeaseAboveAgentLimit(t *testing.T) {
	db := openTestDB(t)
	c := NewCore(db, "worker", ledger.Card{}, 5, testLogger(), config.ModelConfig{})
	t.Cleanup(func() { commander.SetAgentTimeout(config.DefaultAgentTimeoutS * time.Second) })

	// The historical numbers: a 600s agent budget against a 600s lease.
	c.SetTimeouts(config.TimeoutsConfig{AgentS: 600, TaskLeaseS: 600})
	if got := c.lease(); got <= commander.AgentHardTimeout() {
		t.Fatalf("lease = %v, want > agent hard timeout %v", got, commander.AgentHardTimeout())
	}

	// A lease that already clears the limit is left as configured.
	c.SetTimeouts(config.TimeoutsConfig{AgentS: 600, TaskLeaseS: 7200})
	if got := c.lease(); got != 2*time.Hour {
		t.Fatalf("lease = %v, want the configured 2h", got)
	}
}

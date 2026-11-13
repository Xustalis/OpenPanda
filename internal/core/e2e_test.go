// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/util"
)

// TestDelegateIdempotent sends the same task_delegate twice; the second must
// be ignored (P0-40).
func TestDelegateIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCore(t, "entry-idem")
	worker := newCore(t, "worker-idem")
	startPair(t, ctx, entry, worker)

	env, _ := bus.NewEnvelope(bus.MsgTaskDelegate, "entry-idem", "m1", bus.TaskDelegatePayload{
		TaskID: "idem-task", Title: "t", Intent: "x", Requires: []string{"sys:info"},
		Chain: []string{"entry-idem"},
	})
	if err := entry.sendTo("worker-idem", env); err != nil {
		t.Fatalf("send 1: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Re-send the same task_id with a different msg_id.
	env2, _ := bus.NewEnvelope(bus.MsgTaskDelegate, "entry-idem", "m2", bus.TaskDelegatePayload{
		TaskID: "idem-task", Title: "t", Intent: "x", Requires: []string{"sys:info"},
		Chain: []string{"entry-idem"},
	})
	if err := entry.sendTo("worker-idem", env2); err != nil {
		t.Fatalf("send 2: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	tasks, err := worker.store.ListByState(ctx, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	done := 0
	for _, tk := range tasks {
		if tk.TaskID == "idem-task" && tk.State == StateDone {
			done++
		}
	}
	if done != 1 {
		t.Fatalf("expected exactly 1 done task, got %d", done)
	}
}

// TestDelegateReplacesTerminalCopy covers the re-route landing case: a node
// already holds a CLOSED copy of a task from an older attempt (it ran the
// task once, the delegator's lease lapsed, the re-route came back here). The
// stale verdict must not dead-end the fresh attempt — the terminal row is
// replaced and the new delivery runs. An ACTIVE or review-parked copy still
// wins: a duplicate delivery must never kill live work.
func TestDelegateReplacesTerminalCopy(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	card := ledger.Card{
		Device:        "replacee",
		ResourceClass: "Standard",
		Native:        []ledger.NativeAbility{{ID: "sys:info", Command: "uname"}},
		Capacity:      ledger.Capacity{CPUCores: 2, RAMGB: 4, MaxConcurrent: 2},
	}
	c := NewCore(db, "replacee", card, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())
	c.SetSharedSecret(testSharedSecret)

	// The stale copy: attempt A already closed on this node.
	stale := createTask(t, c.store, "", "rr-task", "origin")
	if err := c.store.Queue(ctx, stale.TaskID, "origin"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.ForceFail(ctx, stale.TaskID, "old attempt died here"); err != nil {
		t.Fatalf("close old copy: %v", err)
	}
	storedAttempt := stale.AttemptID
	time.Sleep(3 * time.Millisecond)
	newAttempt, err := util.UUIDv7()
	if err != nil {
		t.Fatalf("mint attempt: %v", err)
	}
	if !attemptIsNewer(newAttempt, storedAttempt) {
		t.Fatalf("test premise broken: %s should order after %s", newAttempt, storedAttempt)
	}

	env, err := bus.NewEnvelope(bus.MsgTaskDelegate, "origin", "m-rr", bus.TaskDelegatePayload{
		TaskID: stale.TaskID, Title: "t", Intent: "x",
		Requires: []string{"sys:info"}, Chain: []string{"origin"},
		AttemptID: newAttempt,
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	c.handleDelegate(ctx, env)

	got, err := c.store.Get(ctx, stale.TaskID)
	if err != nil {
		t.Fatalf("replaced row missing: %v", err)
	}
	if got.AttemptID != newAttempt {
		t.Fatalf("attempt = %s, want the re-routed attempt adopted", got.AttemptID)
	}
	if Terminal(got.State) && got.State != StateDone {
		t.Fatalf("re-routed copy settled %s without running — stale verdict dead-ended it", got.State)
	}
	waitLocalTasksSettled(t, c)

	// Same-attempt duplicates still dead-end: re-delivering the delivered
	// attempt is idempotency, not replacement.
	env2, _ := bus.NewEnvelope(bus.MsgTaskDelegate, "origin", "m-rr2", bus.TaskDelegatePayload{
		TaskID: stale.TaskID, Title: "t", Intent: "x",
		Requires: []string{"sys:info"}, Chain: []string{"origin"},
		AttemptID: newAttempt,
	})
	c.handleDelegate(ctx, env2)
	got2, _ := c.store.Get(ctx, stale.TaskID)
	if got2.AttemptID != newAttempt {
		t.Fatalf("same-attempt re-delivery mutated the row: %s", got2.AttemptID)
	}

	// And a stale delivery of the ORIGINAL attempt — wandering in after the
	// re-route already landed — must not resurrect the closed copy either.
	env3, _ := bus.NewEnvelope(bus.MsgTaskDelegate, "origin", "m-rr3", bus.TaskDelegatePayload{
		TaskID: stale.TaskID, Title: "t", Intent: "x",
		Requires: []string{"sys:info"}, Chain: []string{"origin"},
		AttemptID: storedAttempt,
	})
	c.handleDelegate(ctx, env3)
	got3, _ := c.store.Get(ctx, stale.TaskID)
	if got3.AttemptID != newAttempt {
		t.Fatalf("older-attempt re-delivery evicted the live row: attempt = %s", got3.AttemptID)
	}
}

// TestDelegateSupersedesLiveAttempt covers the other half of the re-route
// landing case: the delegator rotated the attempt and its re-route landed on
// a node whose copy is still ACTIVE (running/queued/review) — the stand-down
// cancel the delegator sent first can be lost in a dying socket, so the
// delegate itself carries the order. The live copy is cancelled and replaced;
// ignoring it would leave a zombie running a verdict the origin rejects as
// stale while the fresh attempt never starts.
func TestDelegateSupersedesLiveAttempt(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	card := ledger.Card{
		Device:        "supersede",
		ResourceClass: "Standard",
		Native:        []ledger.NativeAbility{{ID: "sys:info", Command: "uname"}},
		Capacity:      ledger.Capacity{CPUCores: 2, RAMGB: 4, MaxConcurrent: 2},
	}
	c := NewCore(db, "supersede", card, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())
	c.SetSharedSecret(testSharedSecret)

	// The live copy: attempt A is running here, with a real registered run.
	stale := createTask(t, c.store, "", "rr-live", "origin")
	if err := c.store.Queue(ctx, stale.TaskID, "origin"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.Dispatch(ctx, stale.TaskID, "origin", "supersede"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := c.store.Accept(ctx, stale.TaskID, "supersede"); err != nil {
		t.Fatalf("accept: %v", err)
	}
	killed := make(chan struct{})
	rel := c.registerRunning(stale.TaskID, func() { close(killed) })
	defer rel()

	time.Sleep(3 * time.Millisecond)
	newAttempt, err := util.UUIDv7()
	if err != nil {
		t.Fatalf("mint attempt: %v", err)
	}
	env, err := bus.NewEnvelope(bus.MsgTaskDelegate, "origin", "m-sd", bus.TaskDelegatePayload{
		TaskID: stale.TaskID, Title: "t", Intent: "x",
		Requires: []string{"sys:info"}, Chain: []string{"origin"},
		AttemptID: newAttempt,
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	c.handleDelegate(ctx, env)

	select {
	case <-killed:
	case <-time.After(2 * time.Second):
		t.Fatal("the superseded attempt's run was not cancelled")
	}
	got, err := c.store.Get(ctx, stale.TaskID)
	if err != nil {
		t.Fatalf("replaced row missing: %v", err)
	}
	if got.AttemptID != newAttempt {
		t.Fatalf("attempt = %s, want the re-routed attempt adopted", got.AttemptID)
	}
	if got.State == StateCancelled {
		t.Fatalf("the live copy's cancel leaked onto the fresh row")
	}
	waitLocalTasksSettled(t, c)
	final, err := c.store.Get(ctx, stale.TaskID)
	if err != nil {
		t.Fatalf("row missing: %v", err)
	}
	if final.State != StateDone {
		t.Fatalf("superseded re-route ended %s, want done", final.State)
	}
}

// TestDelegateSupersedeRequiresUpstream pins the replacement bind: a wire
// delegate that mints a fresher attempt id may only replace the copy we hold
// when the sender actually sat upstream of us on that task's chain. A peer
// outside the delegation path must not be able to kill live work (or
// resurrect a settled verdict) by re-delivering somebody else's task id —
// without the check the same frame that re-routes legitimately is also a
// remote task-kill primitive.
func TestDelegateSupersedeRequiresUpstream(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	card := ledger.Card{
		Device:        "guarded",
		ResourceClass: "Standard",
		Native:        []ledger.NativeAbility{{ID: "sys:info", Command: "uname"}},
		Capacity:      ledger.Capacity{CPUCores: 2, RAMGB: 4, MaxConcurrent: 2},
	}
	c := NewCore(db, "guarded", card, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())
	c.SetSharedSecret(testSharedSecret)

	live := createTask(t, c.store, "", "guarded-task", "origin")
	if err := c.store.Queue(ctx, live.TaskID, "origin"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.Dispatch(ctx, live.TaskID, "origin", "guarded"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := c.store.Accept(ctx, live.TaskID, "guarded"); err != nil {
		t.Fatalf("accept: %v", err)
	}
	killed := make(chan struct{})
	rel := c.registerRunning(live.TaskID, func() { close(killed) })
	defer rel()

	time.Sleep(3 * time.Millisecond)
	forged, err := util.UUIDv7()
	if err != nil {
		t.Fatalf("mint attempt: %v", err)
	}
	env, err := bus.NewEnvelope(bus.MsgTaskDelegate, "stranger", "m-forge", bus.TaskDelegatePayload{
		TaskID: live.TaskID, Title: "t", Intent: "x",
		Requires: []string{"sys:info"}, Chain: []string{"stranger"},
		AttemptID: forged,
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	c.handleDelegate(ctx, env)

	select {
	case <-killed:
		t.Fatal("a non-upstream delegate cancelled the live run")
	case <-time.After(300 * time.Millisecond):
	}
	got, err := c.store.Get(ctx, live.TaskID)
	if err != nil {
		t.Fatalf("row gone: %v", err)
	}
	if got.AttemptID != live.AttemptID || got.State != StateRunning {
		t.Fatalf("forged supersede mutated the row: state=%s attempt=%s", got.State, got.AttemptID)
	}
}

// TestDispensationCancelIgnoresNewerAttempt pins the cancel-scope fence: a
// dispensation kill order (lease expiry, reroute supersede) stamped with
// attempt A lands after the row already moved to a newer dispensation —
// the live cancel and the parked re-delivery can reorder across connections
// — and must stand down only the attempt it named, not the fresh copy.
// A tree cancel (human reject/cancel, empty attempt) still kills whatever
// dispensation is current.
func TestDispensationCancelIgnoresNewerAttempt(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	c := NewCore(db, "cancel-scope", ledger.Card{
		Device:        "cancel-scope",
		ResourceClass: "Standard",
		Native:        []ledger.NativeAbility{{ID: "sys:info", Command: "uname"}},
		Capacity:      ledger.Capacity{CPUCores: 2, RAMGB: 4, MaxConcurrent: 2},
	}, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())
	c.SetSharedSecret(testSharedSecret)

	// The executor's delegated copy: remote-authored, chain ends at this
	// node so "origin" authenticates as the delegator predecessor.
	tk, err := c.store.CreateWithID(ctx, "scoped-cancel", "", "proj", "t",
		"cancel-scope", []string{"origin", "cancel-scope"}, true)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(err)
	must(c.store.Queue(ctx, tk.TaskID, "cancel-scope"))
	must(c.store.Dispatch(ctx, tk.TaskID, "cancel-scope", "cancel-scope"))
	must(c.store.Accept(ctx, tk.TaskID, "cancel-scope"))
	tk, _ = c.store.Get(ctx, tk.TaskID)
	oldAttempt := tk.AttemptID

	time.Sleep(3 * time.Millisecond)
	newAttempt, err := util.UUIDv7()
	if err != nil {
		t.Fatalf("mint attempt: %v", err)
	}
	// The delegator's re-route already landed: the row runs the fresh
	// dispensation while the old attempt's kill order is still in flight.
	must(c.store.AdoptAttempt(ctx, tk.TaskID, oldAttempt, newAttempt))

	env, err := bus.NewEnvelope(bus.MsgTaskCancel, "origin", "m-dc", bus.TaskCancelPayload{
		TaskID: tk.TaskID, AttemptID: oldAttempt, Reason: "lease expired",
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	c.handleCancel(ctx, env)
	got, err := c.store.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != StateRunning || got.AttemptID != newAttempt {
		t.Fatalf("stale dispensation cancel touched the fresh copy: state=%s attempt=%s",
			got.State, got.AttemptID)
	}

	// The same frame naming the CURRENT attempt still lands — the scope
	// narrows a kill order, it does not disarm it.
	env2, _ := bus.NewEnvelope(bus.MsgTaskCancel, "origin", "m-dc2", bus.TaskCancelPayload{
		TaskID: tk.TaskID, AttemptID: newAttempt, Reason: "lease expired",
	})
	c.handleCancel(ctx, env2)
	got, err = c.store.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != StateCancelled {
		t.Fatalf("matching dispensation cancel left state=%s, want cancelled", got.State)
	}
}

// TestDeclineFencesStaleAttempt pins the decline gate: a refusal stamped
// with attempt A arriving after the row rotated to a newer dispensation (a
// re-route landed back on the same node, or a declined copy that moved on)
// must not bounce the fresh attempt's dispatched row back to queued. A
// decline naming the current attempt still re-queues.
func TestDeclineFencesStaleAttempt(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	c := NewCore(db, "decline-scope", ledger.Card{
		Device:        "decline-scope",
		ResourceClass: "Standard",
		Native:        []ledger.NativeAbility{{ID: "sys:info", Command: "uname"}},
		Capacity:      ledger.Capacity{CPUCores: 2, RAMGB: 4, MaxConcurrent: 2},
	}, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())
	c.SetSharedSecret(testSharedSecret)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	tk := createTask(t, c.store, "", "decl-task", "decline-scope")
	must(c.store.Queue(ctx, tk.TaskID, "decline-scope"))
	must(c.store.Dispatch(ctx, tk.TaskID, "decline-scope", "exec-node"))
	tk, err := c.store.Get(ctx, tk.TaskID)
	must(err)
	oldAttempt := tk.AttemptID

	time.Sleep(3 * time.Millisecond)
	newAttempt, err := util.UUIDv7()
	must(err)
	must(c.store.AdoptAttempt(ctx, tk.TaskID, oldAttempt, newAttempt))

	// The stale refusal: the dispatch target answers for the attempt the row
	// already replaced — the fresh dispensation must stay dispatched.
	env, err := bus.NewEnvelope(bus.MsgTaskDecline, "exec-node", "m-ds", bus.TaskDeclinePayload{
		TaskID: tk.TaskID, AttemptID: oldAttempt, Reason: "capacity full",
	})
	must(err)
	c.handleDecline(ctx, env)
	got, err := c.store.Get(ctx, tk.TaskID)
	must(err)
	if got.State != StateDispatched || got.AttemptID != newAttempt {
		t.Fatalf("stale decline bounced the fresh copy: state=%s attempt=%s",
			got.State, got.AttemptID)
	}

	// The matching decline requeues and feeds the re-router's seen set.
	env2, _ := bus.NewEnvelope(bus.MsgTaskDecline, "exec-node", "m-ds2", bus.TaskDeclinePayload{
		TaskID: tk.TaskID, AttemptID: newAttempt, Reason: "capacity full",
	})
	c.handleDecline(ctx, env2)
	got, err = c.store.Get(ctx, tk.TaskID)
	must(err)
	if got.State != StateQueued && got.State != StateDispatched {
		t.Fatalf("matching decline left state=%s, want queued or re-dispatched", got.State)
	}
}

// TestAcceptFencesStaleAttempt pins the accept gate: a delayed task_accept
// for a superseded dispensation must not mark the fresh attempt's row
// running — the executor it names is claiming OLD work.
func TestAcceptFencesStaleAttempt(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	c := NewCore(db, "accept-scope", ledger.Card{
		Device:        "accept-scope",
		ResourceClass: "Standard",
		Native:        []ledger.NativeAbility{{ID: "sys:info", Command: "uname"}},
		Capacity:      ledger.Capacity{CPUCores: 2, RAMGB: 4, MaxConcurrent: 2},
	}, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())
	c.SetSharedSecret(testSharedSecret)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	tk := createTask(t, c.store, "", "acc-task", "accept-scope")
	must(c.store.Queue(ctx, tk.TaskID, "accept-scope"))
	must(c.store.Dispatch(ctx, tk.TaskID, "accept-scope", "exec-node"))
	tk, err := c.store.Get(ctx, tk.TaskID)
	must(err)
	oldAttempt := tk.AttemptID

	time.Sleep(3 * time.Millisecond)
	newAttempt, err := util.UUIDv7()
	must(err)
	must(c.store.AdoptAttempt(ctx, tk.TaskID, oldAttempt, newAttempt))

	env, err := bus.NewEnvelope(bus.MsgTaskAccept, "exec-node", "m-as", bus.TaskAcceptPayload{
		TaskID: tk.TaskID, AttemptID: oldAttempt,
	})
	must(err)
	c.handleAccept(ctx, env)
	got, err := c.store.Get(ctx, tk.TaskID)
	must(err)
	if got.State != StateDispatched || got.AttemptID != newAttempt {
		t.Fatalf("stale accept claimed the fresh copy: state=%s attempt=%s",
			got.State, got.AttemptID)
	}

	// A store-level CAS on a stale expected attempt must conflict too —
	// the wire gate and the write share the same fence.
	if err := c.store.Accept(ctx, tk.TaskID, "exec-node", oldAttempt); !errors.Is(err, ErrConflict) {
		t.Fatalf("Accept with stale attempt = %v, want ErrConflict", err)
	}
	env2, _ := bus.NewEnvelope(bus.MsgTaskAccept, "exec-node", "m-as2", bus.TaskAcceptPayload{
		TaskID: tk.TaskID, AttemptID: newAttempt,
	})
	c.handleAccept(ctx, env2)
	got, err = c.store.Get(ctx, tk.TaskID)
	must(err)
	if got.State != StateRunning || got.OwnerNode != "exec-node" {
		t.Fatalf("matching accept left state=%s owner=%s, want running/exec-node",
			got.State, got.OwnerNode)
	}
}

// TestTaskTimeoutFails verifies the lease-expiry lifecycle: a running task
// whose lease lapses re-queues under a fresh attempt while retry budget
// remains — an expired lease means the supervisor died, not that the work
// was judged — and only fails once the persisted retry budget is spent
// (P0-36).
func TestTaskTimeoutFails(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := createTask(t, s, "", "slow", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(ctx, tk.TaskID, "root"))
	must(s.Dispatch(ctx, tk.TaskID, "root", "root"))
	must(s.Accept(ctx, tk.TaskID, "root"))
	// Stamp an already-expired lease directly (SetLease ignores non-positive).
	expire := func() {
		if _, err := s.db.Exec(`UPDATE tasks SET lease_expires_at=? WHERE task_id=?`, s.now()-1, tk.TaskID); err != nil {
			t.Fatalf("stamp lease: %v", err)
		}
	}
	expire()

	expired, err := s.ExpireTasks(ctx, "root")
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if len(expired) != 1 || expired[0].TaskID != tk.TaskID || !expired[0].Rerouted {
		t.Fatalf("expected 1 rerouted row for %s, got %+v", tk.TaskID, expired)
	}
	got, _ := s.Get(ctx, tk.TaskID)
	if got.State != StateQueued {
		t.Fatalf("state = %s, want queued for re-route", got.State)
	}
	if got.AttemptID == tk.AttemptID {
		t.Fatalf("attempt not rotated on re-route: %s", got.AttemptID)
	}
	if got.LeaseExpires != 0 {
		t.Fatalf("stale lease survived re-route: %d", got.LeaseExpires)
	}

	// Each requeue burns one persisted retry. Drive it around the loop until
	// the budget is spent — then expiry must fail terminally again.
	for i := 0; i < maxPersistedRetries; i++ {
		must(s.Dispatch(ctx, tk.TaskID, "root", "root"))
		must(s.Accept(ctx, tk.TaskID, "root"))
		expire()
		if _, err := s.ExpireTasks(ctx, "root"); err != nil {
			t.Fatalf("expire %d: %v", i, err)
		}
		got, _ = s.Get(ctx, tk.TaskID)
		if got.State == StateFailed {
			break
		}
		if got.State != StateQueued {
			t.Fatalf("cycle %d: state = %s, want queued or failed", i, got.State)
		}
	}
	got, _ = s.Get(ctx, tk.TaskID)
	if got.State != StateFailed {
		t.Fatalf("state = %s, want failed after retry budget spent", got.State)
	}
}

// TestRecoverRestoresState verifies a restart normalizes interrupted tasks
// (P0-39).
func TestRecoverRestoresState(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	running := createTask(t, s, "", "running", "root")
	dispatched := createTask(t, s, "", "dispatched", "root")
	done := createTask(t, s, "", "done", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(ctx, running.TaskID, "root"))
	must(s.Dispatch(ctx, running.TaskID, "root", "root"))
	must(s.Accept(ctx, running.TaskID, "root"))

	must(s.Queue(ctx, dispatched.TaskID, "root"))
	must(s.Dispatch(ctx, dispatched.TaskID, "root", "root"))

	must(s.Queue(ctx, done.TaskID, "root"))
	must(s.Dispatch(ctx, done.TaskID, "root", "root"))
	must(s.Accept(ctx, done.TaskID, "root"))
	must(s.Complete(ctx, done.TaskID, "root", map[string]any{"ok": true}))

	if _, err := s.Recover(ctx, "root"); err != nil {
		t.Fatalf("recover: %v", err)
	}

	for _, want := range []struct{ id, state string }{
		{running.TaskID, StateFailed},
		{dispatched.TaskID, StateQueued},
		{done.TaskID, StateDone},
	} {
		got, err := s.Get(ctx, want.id)
		if err != nil {
			t.Fatalf("get %s: %v", want.id, err)
		}
		if got.State != want.state {
			t.Fatalf("%s state = %s, want %s", want.id, got.State, want.state)
		}
	}
}

// TestRecoverPreservesReviewQueue pins the invariant that a restart must not
// touch the human-approval queue: a task parked in review keeps its state and
// its result_json (the evidence the human came back for), while an interrupted
// running task is failed and gets an audit event for the transition.
func TestRecoverPreservesReviewQueue(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	drive := func(tk Task) {
		t.Helper()
		must(s.Queue(ctx, tk.TaskID, "root"))
		must(s.Dispatch(ctx, tk.TaskID, "root", "root"))
		must(s.Accept(ctx, tk.TaskID, "root"))
	}

	review := createTask(t, s, "", "awaiting-approval", "root")
	drive(review)
	must(s.PauseWithResult(ctx, review.TaskID, "root", map[string]any{
		"ok": true, "stdout": "deleted 3 files, confirm?",
	}))
	before, err := s.Get(ctx, review.TaskID)
	if err != nil {
		t.Fatalf("get review task: %v", err)
	}
	if before.State != StateReview || before.ResultJSON == "" {
		t.Fatalf("setup: state=%s result=%q, want review with a result", before.State, before.ResultJSON)
	}

	running := createTask(t, s, "", "interrupted", "root")
	drive(running)

	if _, err := s.Recover(ctx, "root"); err != nil {
		t.Fatalf("recover: %v", err)
	}

	after, err := s.Get(ctx, review.TaskID)
	if err != nil {
		t.Fatalf("get review task after recover: %v", err)
	}
	if after.State != StateReview {
		t.Fatalf("review task state = %s, want review (restart must not drain the approval queue)", after.State)
	}
	if after.ResultJSON != before.ResultJSON {
		t.Fatalf("review result_json = %q, want preserved %q", after.ResultJSON, before.ResultJSON)
	}

	got, err := s.Get(ctx, running.TaskID)
	if err != nil {
		t.Fatalf("get running task: %v", err)
	}
	if got.State != StateFailed {
		t.Fatalf("running task state = %s, want failed", got.State)
	}
	evs, err := s.Events(ctx, running.TaskID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var recovered bool
	for _, e := range evs {
		if e.Type == EvRecover {
			recovered = true
		}
	}
	if !recovered {
		t.Fatalf("no %s event on the audit chain after recovery", EvRecover)
	}
	if evs[len(evs)-1].Type != EvRecover {
		t.Fatalf("last event = %s, want %s", evs[len(evs)-1].Type, EvRecover)
	}
}

// TestCancelPropagates verifies a cancel message reaches the executor and the
// task is marked cancelled (P0-37).
func TestCancelPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCore(t, "entry-cancel")
	// Worker has a slow native command so the task stays running long
	// enough to receive the cancel.
	worker := newCoreWithNative(t, "worker-cancel", ledger.NativeAbility{
		ID: "sys:sleep", Command: "sleep", Args: []string{"5"},
	})
	startPair(t, ctx, entry, worker)

	// Delegate a task.
	env, _ := bus.NewEnvelope(bus.MsgTaskDelegate, "entry-cancel", "m1", bus.TaskDelegatePayload{
		TaskID: "cancel-task", Title: "t", Intent: "x", Requires: []string{"sys:sleep"},
		Chain: []string{"entry-cancel"},
	})
	if err := entry.sendTo("worker-cancel", env); err != nil {
		t.Fatalf("delegate: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Send cancel while the task is still sleeping.
	cenv, _ := bus.NewEnvelope(bus.MsgTaskCancel, "entry-cancel", "m2", bus.TaskCancelPayload{
		TaskID: "cancel-task", Reason: "changed mind",
	})
	if err := entry.sendTo("worker-cancel", cenv); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	tk, err := worker.store.Get(ctx, "cancel-task")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if tk.State != StateCancelled {
		t.Fatalf("worker state = %s, want cancelled", tk.State)
	}
}

// TestRejectPropagates covers the review-side of the same hole: `panda reject`
// on the delegator must stand down the executor's delegated review copy, not
// just fail the local row. Without the forward the executor's copy stayed in
// review and a local approve there ran work the origin explicitly denied.
func TestRejectPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCore(t, "entry-reject")
	worker := newCore(t, "worker-reject")
	startPair(t, ctx, entry, worker)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	const taskID = "reject-task"
	// Executor copy: parked in review awaiting consent, owned by the
	// executor (delegated rows name the delegator in the chain so its
	// kill is authorized).
	_, err := worker.store.CreateWithID(ctx, taskID, "", "proj", "t", "worker-reject",
		[]string{"entry-reject", "worker-reject"}, true)
	must(err)
	must(worker.store.Queue(ctx, taskID, "worker-reject"))
	must(worker.store.Dispatch(ctx, taskID, "worker-reject", "worker-reject"))
	must(worker.store.Accept(ctx, taskID, "worker-reject"))
	must(worker.store.PauseWithDisposition(ctx, taskID, "worker-reject", "parked", ApprovalResumeExecution))

	// Delegator copy: same wire id, review, dispatch target = the executor.
	_, err = entry.store.CreateWithID(ctx, taskID, "", "proj", "t", "entry-reject", nil, false)
	must(err)
	must(entry.store.Queue(ctx, taskID, "entry-reject"))
	must(entry.store.Dispatch(ctx, taskID, "entry-reject", "worker-reject"))
	must(entry.store.Accept(ctx, taskID, "entry-reject"))
	must(entry.store.PauseWithDisposition(ctx, taskID, "entry-reject", "parked", ApprovalResumeExecution))

	must(entry.RejectTree(ctx, taskID, "not what I asked"))
	if tk, _ := entry.store.Get(ctx, taskID); tk.State != StateFailed {
		t.Fatalf("origin state = %s, want failed", tk.State)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		tk, err := worker.store.Get(ctx, taskID)
		if err != nil {
			t.Fatalf("worker get: %v", err)
		}
		if tk.State == StateCancelled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker state = %s, want cancelled — reject never crossed the wire", tk.State)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The kill must hold: the executor's copy can no longer be approved.
	if err := worker.store.Approve(ctx, taskID); err == nil {
		t.Fatal("approve on rejected executor copy succeeded — denied work is still runnable")
	}
}

// TestResultFromRotatedAttemptLands is the resume/retry convergence case: the
// executor re-ran the task under a fresh attempt (task_resume re-run or a
// supervisor retry), and its result must land on the delegator's row rather
// than being dropped as stale — the pre-fix split that left the origin in
// running while the executor parked back in review.
func TestResultFromRotatedAttemptLands(t *testing.T) {
	ctx := context.Background()
	entry := newCore(t, "entry-rot")
	if err := entry.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	// An attempt minted BEFORE the task exists is strictly older than the
	// row's own — the stale-drop control case below.
	past, err := util.UUIDv7()
	must(err)
	time.Sleep(3 * time.Millisecond)

	tk, err := entry.store.CreateWithID(ctx, "rot-task", "", "proj", "t", "entry-rot", nil, false)
	must(err)
	must(entry.store.Queue(ctx, "rot-task", "entry-rot"))
	must(entry.store.Dispatch(ctx, "rot-task", "entry-rot", "worker-rot"))
	must(entry.store.Accept(ctx, "rot-task", "entry-rot"))
	stored := tk.AttemptID

	time.Sleep(3 * time.Millisecond)
	rotated, err := util.UUIDv7()
	must(err)
	if !attemptIsNewer(rotated, stored) {
		t.Fatalf("test premise broken: %s should order after %s", rotated, stored)
	}
	env, _ := bus.NewEnvelope(bus.MsgTaskResult, "worker-rot", "m-rot", bus.TaskResultPayload{
		TaskID: "rot-task", AttemptID: rotated, State: StateDone, OK: true,
		Stdout: "finished on the retry",
	})
	entry.handleResult(ctx, env)
	got, err := entry.store.Get(ctx, "rot-task")
	must(err)
	if got.State != StateDone {
		t.Fatalf("rotated-attempt result dropped: state = %s, want done", got.State)
	}
	if got.AttemptID != rotated {
		t.Fatalf("attempt = %s, want adopted %s", got.AttemptID, rotated)
	}

	// The inverse must still hold: a genuinely OLDER attempt — the replay
	// shape the check exists for — is dropped, not adopted.
	tk2, err := entry.store.CreateWithID(ctx, "stale-task", "", "proj", "t", "entry-rot", nil, false)
	must(err)
	must(entry.store.Queue(ctx, "stale-task", "entry-rot"))
	must(entry.store.Dispatch(ctx, "stale-task", "entry-rot", "worker-rot"))
	must(entry.store.Accept(ctx, "stale-task", "entry-rot"))
	env2, _ := bus.NewEnvelope(bus.MsgTaskResult, "worker-rot", "m-stale", bus.TaskResultPayload{
		TaskID: "stale-task", AttemptID: past, State: StateDone, OK: true,
		Stdout: "replay of a superseded run",
	})
	entry.handleResult(ctx, env2)
	got2, err := entry.store.Get(ctx, "stale-task")
	must(err)
	if got2.State == StateDone {
		t.Fatal("older-attempt result landed — stale protection regressed")
	}
	if got2.AttemptID != tk2.AttemptID {
		t.Fatalf("attempt = %s, want unchanged %s after dropped result", got2.AttemptID, tk2.AttemptID)
	}
}

// seedAcceptWorkPair parks the same AcceptWork review copy on both cores:
// the executor's copy (owned by itself, chain naming the delegator) and the
// delegator's copy (dispatch target = the executor). Returns nothing — both
// rows are addressable by taskID on their respective stores.
func seedAcceptWorkPair(t *testing.T, ctx context.Context, entry, worker *Core, taskID string) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("seed %s: %v", taskID, err)
		}
	}
	_, err := worker.store.CreateWithID(ctx, taskID, "", "proj", "t", worker.nodeID,
		[]string{entry.nodeID, worker.nodeID}, true)
	must(err)
	must(worker.store.Queue(ctx, taskID, worker.nodeID))
	must(worker.store.Dispatch(ctx, taskID, worker.nodeID, worker.nodeID))
	must(worker.store.Accept(ctx, taskID, worker.nodeID))
	must(worker.store.PauseWithDisposition(ctx, taskID, worker.nodeID, "awaiting approval", ApprovalAcceptWork))

	_, err = entry.store.CreateWithID(ctx, taskID, "", "proj", "t", entry.nodeID, nil, false)
	must(err)
	must(entry.store.Queue(ctx, taskID, entry.nodeID))
	must(entry.store.Dispatch(ctx, taskID, entry.nodeID, worker.nodeID))
	must(entry.store.Accept(ctx, taskID, entry.nodeID))
	must(entry.store.PauseWithDisposition(ctx, taskID, entry.nodeID, "awaiting approval", ApprovalAcceptWork))
}

// TestAcceptMirrorsToSiblingCopy: approving an AcceptWork park on one node
// must close the sibling copy parked on the other — before the mirror frame,
// whichever side approved first left the other's copy in review forever.
func TestAcceptMirrorsToSiblingCopy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCore(t, "entry-accept")
	worker := newCore(t, "worker-accept")
	startPair(t, ctx, entry, worker)

	const taskID = "accept-task"
	seedAcceptWorkPair(t, ctx, entry, worker, taskID)

	final, _, err := entry.ResumeApproved(ctx, taskID)
	if err != nil {
		t.Fatalf("resume approved: %v", err)
	}
	if final.State != StateDone {
		t.Fatalf("origin state = %s, want done", final.State)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		tk, err := worker.store.Get(ctx, taskID)
		if err != nil {
			t.Fatalf("worker get: %v", err)
		}
		if tk.State == StateDone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker state = %s, want done — the accept never mirrored", tk.State)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestAcceptMirrorsFromExecutorSide is the reverse direction: the human
// approves on the EXECUTOR's copy, and the origin's parked copy must close.
func TestAcceptMirrorsFromExecutorSide(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCore(t, "entry-accept2")
	worker := newCore(t, "worker-accept2")
	startPair(t, ctx, entry, worker)

	const taskID = "accept-task2"
	seedAcceptWorkPair(t, ctx, entry, worker, taskID)

	final, _, err := worker.ResumeApproved(ctx, taskID)
	if err != nil {
		t.Fatalf("resume approved: %v", err)
	}
	if final.State != StateDone {
		t.Fatalf("executor state = %s, want done", final.State)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		tk, err := entry.store.Get(ctx, taskID)
		if err != nil {
			t.Fatalf("entry get: %v", err)
		}
		if tk.State == StateDone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("origin state = %s, want done — the accept never mirrored back", tk.State)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestAcceptParksForOfflineExecutor: approving while the sibling's node is
// offline must park the accept in the resume outbox (not drop it), and the
// executor's next hello must deliver it — its copy closes without its own
// human having to re-approve.
func TestAcceptParksForOfflineExecutor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCore(t, "entry-park")
	worker := newCore(t, "worker-park")

	const taskID = "accept-parked"
	seedAcceptWorkPair(t, ctx, entry, worker, taskID)

	// No link yet: the approve succeeds locally and the mirror is parked.
	final, _, err := entry.ResumeApproved(ctx, taskID)
	if err != nil {
		t.Fatalf("resume approved: %v", err)
	}
	if final.State != StateDone {
		t.Fatalf("origin state = %s, want done", final.State)
	}
	var raw string
	if err := entry.db.QueryRow(
		`SELECT payload_json FROM resume_outbox WHERE task_id=?`, taskID).Scan(&raw); err != nil {
		t.Fatalf("parked accept missing from resume_outbox: %v", err)
	}
	if !strings.Contains(raw, `"accept":true`) {
		t.Fatalf("parked payload = %s, want an accept mirror", raw)
	}

	// The executor comes online: its hello flushes the parked accept and the
	// copy closes without a second human decision.
	startPair(t, ctx, entry, worker)
	deadline := time.Now().Add(5 * time.Second)
	for {
		tk, err := worker.store.Get(ctx, taskID)
		if err != nil {
			t.Fatalf("worker get: %v", err)
		}
		if tk.State == StateDone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker state = %s, want done — the parked accept was not flushed", tk.State)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestPrepareToleratesRacedQueue: the delegate path's prepare must adopt a
// row another path already moved — a plan sweep that raced the delegation, a
// duplicate delivery's first invocation — instead of declining the task on a
// bookkeeping conflict. A terminal row is the one real stand-down.
func TestPrepareToleratesRacedQueue(t *testing.T) {
	ctx := context.Background()
	worker := newCore(t, "worker-prep")
	if err := worker.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	_, err := worker.store.CreateWithID(ctx, "raced-task", "", "proj", "t",
		"worker-prep", []string{"worker-prep"}, false)
	must(err)
	// Another path queued the row first (the plan sweep's release).
	must(worker.store.Queue(ctx, "raced-task", "worker-prep"))
	if err := worker.prepare(ctx, "raced-task"); err != nil {
		t.Fatalf("prepare on a raced queue = %v, want adopted", err)
	}
	if got, _ := worker.store.Get(ctx, "raced-task"); got.State != StateDispatched {
		t.Fatalf("state = %s, want dispatched", got.State)
	}
	// A terminal row reports ErrCancelled so the caller stands down silently
	// instead of declining a task the delegator already closed.
	must(worker.store.Cancel(ctx, "raced-task"))
	if err := worker.prepare(ctx, "raced-task"); !errors.Is(err, ErrCancelled) {
		t.Fatalf("prepare on a cancelled row = %v, want ErrCancelled", err)
	}
}

// TestExecWorkDirPersistedAndReported: a run must record WHERE it happened —
// the row's exec_work_dir (for the queue board / task show / panel) and the
// result payload's work_dir (so a delegator can locate the output on the
// machine that made it). work_dir itself stays the submitter's pin. An
// anonymous task runs in its private per-task dir under the node work root.
func TestExecWorkDirPersistedAndReported(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithNative(t, "ws-node", ledger.NativeAbility{
		ID: "sys:probe", Command: "echo", Args: []string{"hi"},
	})
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	dir := t.TempDir()
	c.SetWorkDir(dir)

	task, result, err := c.Submit(ctx, TaskInput{
		Title: "ws probe", Intent: "run the probe", Requires: []string{"sys:probe"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	want := filepath.Join(dir, "tasks", task.TaskID)
	if result.WorkDir != want {
		t.Fatalf("result work_dir = %q, want %q", result.WorkDir, want)
	}
	row, err := c.store.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.ExecWorkDir != want {
		t.Fatalf("row exec_work_dir = %q, want %q", row.ExecWorkDir, want)
	}
	if row.WorkDir != "" {
		t.Fatalf("row work_dir = %q, want empty (pin untouched)", row.WorkDir)
	}
}

// TestResumeRaceAlreadyRunningDoesNotFailOrigin is the double-approval race:
// the human approved on BOTH copies, and the delegator's task_resume arrives
// while the executor's copy is already running. The old synthesized
// "resume: task state is running" FAILURE landed on the delegator as a
// task_result, killed its copy, and cascaded its plan. The reply must echo
// the real state instead: in-flight is informational — the delegator keeps
// waiting for the executor's eventual result.
func TestResumeRaceAlreadyRunningDoesNotFailOrigin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCore(t, "entry-race")
	worker := newCore(t, "worker-race")
	startPair(t, ctx, entry, worker)

	const taskID = "race-task"
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	// Executor copy: already RUNNING (its local approval won the race).
	_, err := worker.store.CreateWithID(ctx, taskID, "", "proj", "t", worker.nodeID,
		[]string{entry.nodeID, worker.nodeID}, true)
	must(err)
	must(worker.store.Queue(ctx, taskID, worker.nodeID))
	must(worker.store.Dispatch(ctx, taskID, worker.nodeID, worker.nodeID))
	must(worker.store.Accept(ctx, taskID, worker.nodeID))

	// Delegator copy: dispatched to the executor (its approval claimed it).
	// The attempt must match the executor's — the delegation's AdoptAttempt
	// does this in the real flow, and a mismatched attempt is (correctly)
	// dropped as stale before any of this logic runs.
	wt, err := worker.store.Get(ctx, taskID)
	must(err)
	et, err := entry.store.CreateWithID(ctx, taskID, "", "proj", "t", entry.nodeID, nil, false)
	must(err)
	must(entry.store.AdoptAttempt(ctx, taskID, et.AttemptID, wt.AttemptID))
	must(entry.store.Queue(ctx, taskID, entry.nodeID))
	must(entry.store.Dispatch(ctx, taskID, entry.nodeID, worker.nodeID))

	// The late resume crosses the wire; the executor is already running it.
	env, _ := bus.NewEnvelope(bus.MsgTaskResume, entry.nodeID, "m-race",
		bus.TaskResumePayload{TaskID: taskID, AttemptID: wt.AttemptID})
	worker.handleResume(ctx, env)

	deadline := time.Now().Add(5 * time.Second)
	for {
		tk, err := entry.store.Get(ctx, taskID)
		if err != nil {
			t.Fatalf("entry get: %v", err)
		}
		if tk.State == StateFailed {
			t.Fatalf("delegator copy failed on the benign double approval (result %s)", tk.ResultJSON)
		}
		if taskEventsContain(entry, ctx, taskID, "executor reports task running") {
			break // informational report recorded; the wait continues
		}
		if time.Now().After(deadline) {
			t.Fatalf("no informational report reached the delegator; state=%s", tk.State)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if tk, _ := entry.store.Get(ctx, taskID); tk.State != StateDispatched {
		t.Fatalf("delegator state = %s, want dispatched (still waiting for the executor's result)", tk.State)
	}
}

// TestResumeRaceTerminalEchoesVerdict: when the executor's copy already
// CLOSED (failed here), the resume reply must carry that row's real verdict —
// the delegator converges on the same reason instead of a synthesized
// "resume: task state is failed" string.
func TestResumeRaceTerminalEchoesVerdict(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCore(t, "entry-echo")
	worker := newCore(t, "worker-echo")
	startPair(t, ctx, entry, worker)

	const taskID = "echo-task"
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	_, err := worker.store.CreateWithID(ctx, taskID, "", "proj", "t", worker.nodeID,
		[]string{entry.nodeID, worker.nodeID}, true)
	must(err)
	must(worker.store.Queue(ctx, taskID, worker.nodeID))
	must(worker.store.Dispatch(ctx, taskID, worker.nodeID, worker.nodeID))
	must(worker.store.Accept(ctx, taskID, worker.nodeID))
	must(worker.store.Fail(ctx, taskID, worker.nodeID, "disk full"))
	wt, err := worker.store.Get(ctx, taskID)
	must(err)

	et, err := entry.store.CreateWithID(ctx, taskID, "", "proj", "t", entry.nodeID, nil, false)
	must(err)
	must(entry.store.AdoptAttempt(ctx, taskID, et.AttemptID, wt.AttemptID))
	must(entry.store.Queue(ctx, taskID, entry.nodeID))
	must(entry.store.Dispatch(ctx, taskID, entry.nodeID, worker.nodeID))

	env, _ := bus.NewEnvelope(bus.MsgTaskResume, entry.nodeID, "m-echo",
		bus.TaskResumePayload{TaskID: taskID, AttemptID: wt.AttemptID})
	worker.handleResume(ctx, env)

	deadline := time.Now().Add(5 * time.Second)
	for {
		tk, err := entry.store.Get(ctx, taskID)
		if err != nil {
			t.Fatalf("entry get: %v", err)
		}
		if tk.State == StateFailed {
			if !strings.Contains(tk.ResultJSON, "disk full") {
				t.Fatalf("delegator verdict = %s, want the executor's real reason (disk full)", tk.ResultJSON)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("delegator state = %s, want failed with the executor's verdict", tk.State)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestHelloRejectAuthSurfaces exercises the whole rejection verdict: a peer
// whose secret does not match gets an explicit hello_reject before the close,
// its MaintainPeer reports ErrAuthRejected (not a dropped-link nil), and a
// one-shot probe reads "online but refusing our credential" instead of the
// generic handshake timeout that used to read like a network flap.
func TestHelloRejectAuthSurfaces(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := newCore(t, "srv-auth")
	dialer := newCore(t, "cli-auth")
	dialer.SetSharedSecret("wrong-secret")

	if err := server.Register(ctx); err != nil {
		t.Fatalf("register server: %v", err)
	}
	if err := dialer.Register(ctx); err != nil {
		t.Fatalf("register dialer: %v", err)
	}
	serverAddr := startListen(t, ctx, server, "127.0.0.1:0")

	// The listener binds asynchronously; the first dial attempts may hit a
	// not-yet-bound socket — real errors until the reject verdict arrives.
	var merr error
	deadline := time.Now().Add(10 * time.Second)
	for {
		merr = dialer.MaintainPeer(ctx, serverAddr)
		if errors.Is(merr, ErrAuthRejected) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("MaintainPeer = %v, want ErrAuthRejected (rejected dial must not read as a dropped link)", merr)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// A one-shot probe reports the same verdict distinctly from a timeout —
	// this is what `panda doctor`/`nodes add` prints for the user.
	_, err := ProbePeer(ctx, "probe-auth", ledger.Card{Device: "probe-auth"},
		config.ModelConfig{}, config.NetworkConfig{SharedSecret: "wrong-secret"},
		serverAddr, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "rejected our authentication") {
		t.Fatalf("ProbePeer err = %v, want the auth-rejection diagnosis", err)
	}
}

// registerAndListen registers c and starts its WebSocket listener on addr,
// returning a channel that delivers the Listen result: a bind failure lands
// in it as soon as it happens, for linkPair to surface. t.Cleanup drains the
// channel, so a following test never dials a draining listener.
//
// The addr may end in ":0", which asks the kernel for a free port. The
// address the listener actually bound is returned, so a caller can dial it —
// the same contract startListen offers. Fixed ports are what made these
// tests flaky: the whole package shares one port table, `go test ./...` runs
// the packages in parallel, and a port another package (or the previous
// -count iteration) still holds is a bind failure that has nothing to do
// with the code under test.
func registerAndListen(t *testing.T, ctx context.Context, c *Core, addr string) (<-chan error, string) {
	t.Helper()
	if err := c.Register(ctx); err != nil {
		t.Fatalf("startup: %v", err)
	}
	done := make(chan error, 1)
	// Derive a cancellable child: several wire tests pass context.Background(),
	// and a listener whose ctx is never cancelled stays bound for the rest of
	// the process — the next test on that port (or the next -count iteration)
	// then dies with EADDRINUSE. See startListen.
	lctx, cancel := context.WithCancel(ctx)
	go func() { done <- c.Listen(lctx, addr) }()

	// The test's own defer cancel() stops the listener, but ctx→Shutdown is
	// not synchronous — the socket can still be accepting (and answering
	// hellos under the shared test secret with a DIFFERENT node id) while the
	// next test starts reusing the same port. Wait, bounded, for Listen to
	// return so a following test never dials a draining listener.
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("listener on %s did not stop", addr)
		}
	})
	// ":0" is the kernel's choice: the core publishes the port it actually
	// bound (bus.Server.OnBound → ListenAddr), so a test can ask for an
	// ephemeral listener and still know where to dial.
	if strings.HasSuffix(addr, ":0") {
		deadline := time.Now().Add(10 * time.Second)
		for {
			// Read once: Listen first publishes the requested ":0", then
			// OnBound publishes the real address. Two reads in one condition
			// could straddle that first store and incorrectly accept port 0.
			bound := c.ListenAddr()
			if bound != "" && !strings.HasSuffix(bound, ":0") {
				addr = bound
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("core never published the port chosen for %s (last published %q)", addr, bound)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	return done, addr
}

// The paired-node fixture must return a bound port, never the intermediate
// requested address published by Listen before OnBound runs.
func TestRegisterAndListenPublishesEphemeralPort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newCore(t, "register-ephemeral-node")
	_, bound := registerAndListen(t, ctx, c, "127.0.0.1:0")
	if bound == "" || strings.HasSuffix(bound, ":0") {
		t.Fatalf("registerAndListen returned unbound address %q", bound)
	}
	waitListening(t, bound)
}

// linkPair dials entry → workerAddr and waits until both directions see a
// live conn. The listenErr channels (from registerAndListen) are polled each
// round: a Listen that already returned — a bind failure on a contended port,
// or a second bind of a port the test already holds, which Linux rejects with
// EADDRINUSE while macOS tolerates it — fails fast instead of waiting out the
// dial deadline against a listener that will never come up.
//
// Listeners bind inside Listen; retry the dial until both are up instead of
// sleeping a fixed 200ms — loaded CI runners can exceed that.
//
// The ports are shared across tests, and a finished test's listener can still
// be accepting during its ctx-cancel shutdown window: a dial that lands on it
// gets a hello reply signed by the WRONG node id. A transport-level "success"
// is therefore not enough — keep redialing until the conn actually registers
// the expected id.
func linkPair(t *testing.T, ctx context.Context, entry, worker *Core, workerAddr string, listenErrs ...<-chan error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, ch := range listenErrs {
			select {
			case err := <-ch:
				if err != nil {
					t.Fatalf("startup: %v", err)
				}
			default:
			}
		}
		if err := entry.DialPeer(ctx, workerAddr); err != nil {
			if time.Now().After(deadline) {
				t.Fatalf("startup: %v", err)
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		// Dial "succeeded" — but which node answered? Poll briefly for the
		// expected id; a stale listener's wrong-id conn expires and we redial.
		regDeadline := time.Now().Add(2 * time.Second)
		for entry.connFor(worker.node.id) == nil && time.Now().Before(regDeadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if entry.connFor(worker.node.id) != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("peer %s never connected", worker.node.id)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Peer registration lands asynchronously: DialPeer returns once our hello
	// went out, but peers[id] is only populated once handleInbound reads the
	// reply. A fixed sleep raced slow runners (send → "peer not connected"),
	// so wait until both directions see a live conn.
	waitPeer(t, entry, worker.node.id)
	waitPeer(t, worker, entry.node.id)
	// A live conn is not yet a routable peer. ensurePeer registers the
	// connection before the hello handler ingests the card, so a test that
	// submits the moment connFor() turns non-nil can route against a node whose
	// abilities have not been written yet: it fails as "no capability matches
	// required: code:modify" with nothing wrong in the code under test. That is
	// what the race job hit on TestClarificationCrossDevice while ten local
	// runs passed — the window is a few microseconds wide on a fast machine.
	waitPeerCard(t, entry, worker.node.id)
	waitPeerCard(t, worker, entry.node.id)
}

// waitPeerCard blocks until the peer's advertised card is in this core's
// directory. UpsertRemote writes the row in one statement, so the row existing
// means the card landed; before that the peer is connected but unknown.
//
// A session-shaped id is skipped: hello and heartbeat both refuse to store a
// client seat's card on purpose (one ghost row per session otherwise), so a
// wait on it can only time out.
func waitPeerCard(t *testing.T, c *Core, id string) {
	t.Helper()
	if ledger.SessionRowID(id) {
		return
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var one int
		err := c.db.QueryRow(`SELECT 1 FROM employee_cache WHERE id = ?`, id).Scan(&one)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("peer %s never advertised a card: %v", id, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// startPair boots two cores and wires them together, failing the test on
// error. Neither core may already hold a listener: startPair binds both
// ports, and a rebind of a held one is an EADDRINUSE on Linux (macOS quietly
// allows it, which is how such a double bind can pass every local run and
// still break CI). To extend an existing pair, use registerAndListen for the
// new core and linkPair for the edge.
//
// Both listeners take an ephemeral port and the addresses they bound are
// returned, so a caller that needs to dial one (or point a third core at it)
// uses the returned value rather than a hardcoded one. Fixed ports made this
// package the flakiest in the repo: the port table was shared across every
// test file, `go test ./...` runs the packages in parallel, and a port some
// other package still held failed the test for reasons that had nothing to
// do with the code.
func startPair(t *testing.T, ctx context.Context, entry, worker *Core) (string, string) {
	t.Helper()
	ed, entryAddr := registerAndListen(t, ctx, entry, "127.0.0.1:0")
	wd, workerAddr := registerAndListen(t, ctx, worker, "127.0.0.1:0")
	linkPair(t, ctx, entry, worker, workerAddr, ed, wd)
	return entryAddr, workerAddr
}

// waitListening blocks until addr accepts a TCP connection.
//
// Tests that start Listen in a goroutine used to follow it with a fixed
// 200ms sleep. That is a race, not a wait: on an idle laptop the socket is
// bound long before the sleep ends, but with several packages running at
// once the goroutine can be scheduled late enough that the next DialPeer
// sees ECONNREFUSED. Polling removes the guess.
// beaconPort returns the port half of a host:port pair in the shape a
// discovery beacon carries it (":7836" — no host, because the receiver
// substitutes the datagram's source IP). A test that binds an ephemeral
// listener has to derive the port the kernel chose from the address the
// core published.
func beaconPort(address string) string {
	if _, port, err := net.SplitHostPort(address); err == nil && port != "" {
		return ":" + port
	}
	return ""
}

// waitListening blocks until addr accepts a TCP connection.
func waitListening(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("nothing listening on %s after 10s: %v", addr, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// startListen runs c.Listen in the background, waits until the socket accepts,
// and guarantees the listener is stopped when the test ends — even when the
// caller passed context.Background().
//
// That guarantee is the point. Tests used to write
//
//	go func() { _ = c.Listen(ctx, addr) }()
//
// against a never-cancelled context: the goroutine outlived the test, the port
// stayed bound for the rest of the process, and a later test — or the second
// iteration of `go test -count=2` — failed with "bind: address already in
// use". The listener is derived from the caller's ctx, so a test that does
// cancel still shuts down early; the cleanup is the safety net for the ones
// that do not.
//
// It returns the address that was actually bound, so a caller may pass
// "127.0.0.1:0" and dial the result — the path that lets the remaining
// fixed-port tests migrate one at a time.
func startListen(t *testing.T, ctx context.Context, c *Core, addr string) string {
	t.Helper()
	lctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- c.Listen(lctx, addr) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("listener on %s did not stop", addr)
		}
	})
	// A bind that already failed must be reported as itself, not as a
	// ten-second dial timeout in waitListening.
	select {
	case err := <-done:
		t.Fatalf("listen on %s: %v", addr, err)
	default:
	}
	// ":0" is the kernel's choice: the core publishes the port it actually
	// bound (bus.Server.OnBound → ListenAddr), so a test can ask for an
	// ephemeral listener and still know where to dial.
	//
	// The wait is on the published value, never on the literal: a listener
	// whose bind failed after the check above left this loop publishing ":0"
	// and then dialing it, which surfaced as "nothing listening on
	// 127.0.0.1:0" — a timeout about an address no one was meant to dial,
	// with the real error sitting unread in done.
	if strings.HasSuffix(addr, ":0") {
		deadline := time.Now().Add(10 * time.Second)
		for {
			bound := c.ListenAddr()
			if bound != "" && bound != addr && !strings.HasSuffix(bound, ":0") {
				addr = bound
				break
			}
			if time.Now().After(deadline) {
				select {
				case err := <-done:
					t.Fatalf("listen on %s: %v", addr, err)
				default:
				}
				t.Fatalf("core never published the port chosen for %s (last published %q)", addr, bound)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitListening(t, addr)
	return addr
}

// TestListenPublishesEphemeralPort pins the contract startListen relies on
// for ":0": the node advertises the port the kernel chose, not the literal it
// was configured with. Without that, an ephemeral test listener would bind on
// one port while hello payloads and discovery beacons named port 0 — and a
// peer could never dial back.
func TestListenPublishesEphemeralPort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := newCore(t, "ephemeral-node")
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	bound := startListen(t, ctx, c, "127.0.0.1:0")
	if bound == "" || strings.HasSuffix(bound, ":0") || !strings.HasPrefix(bound, "127.0.0.1:") {
		t.Fatalf("startListen returned %q, want the chosen loopback port", bound)
	}
	if got := c.ListenAddr(); got != bound {
		t.Fatalf("ListenAddr = %q, startListen returned %q", got, bound)
	}
	addrs := c.selfAddrs()
	if len(addrs) != 1 || addrs[0] != bound {
		t.Fatalf("advertised addrs = %v, bound = %s", addrs, bound)
	}
}

// waitPeer polls until core c has a live conn to the given peer id.
func waitPeer(t *testing.T, c *Core, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for c.connFor(id) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("peer %s never connected", id)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// newCoreWithNative builds a Core whose card has the given native ability.
func newCoreWithNative(t *testing.T, id string, native ledger.NativeAbility) *Core {
	t.Helper()
	db := openTestDB(t)
	card := ledger.Card{
		Device:        id,
		ResourceClass: "Standard",
		Native:        []ledger.NativeAbility{native},
		Capacity:      ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
	c := NewCore(db, id, card, 5, testLogger(), config.ModelConfig{})
	c.SetSharedSecret(testSharedSecret)
	// A dedicated work dir keeps staged/attached trees out of the package
	// directory — NewCore defaults to ".", which would litter test artifacts
	// into the source tree the moment a task unpacks inputs there.
	c.SetWorkDir(t.TempDir())
	return c
}

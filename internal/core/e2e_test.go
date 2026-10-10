// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
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

	entry := newCore(t, "entry-idem", "127.0.0.1:17846")
	worker := newCore(t, "worker-idem", "127.0.0.1:17847")
	startPair(t, ctx, entry, worker, "127.0.0.1:17846", "127.0.0.1:17847")

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

// TestTaskTimeoutFails verifies a running task with an expired lease becomes
// failed (P0-36).
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
	now := s.now()
	if _, err := s.db.Exec(`UPDATE tasks SET lease_expires_at=? WHERE task_id=?`, now-1, tk.TaskID); err != nil {
		t.Fatalf("stamp lease: %v", err)
	}

	expired, err := s.ExpireTasks(ctx)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if len(expired) != 1 {
		t.Fatalf("expected 1 expired, got %d", len(expired))
	}
	got, _ := s.Get(ctx, tk.TaskID)
	if got.State != StateFailed {
		t.Fatalf("state = %s, want failed", got.State)
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

	if _, err := s.Recover(ctx); err != nil {
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

	if _, err := s.Recover(ctx); err != nil {
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

	entry := newCore(t, "entry-cancel", "127.0.0.1:17856")
	// Worker has a slow native command so the task stays running long
	// enough to receive the cancel.
	worker := newCoreWithNative(t, "worker-cancel", "127.0.0.1:17857", ledger.NativeAbility{
		ID: "sys:sleep", Command: "sleep", Args: []string{"5"},
	})
	startPair(t, ctx, entry, worker, "127.0.0.1:17856", "127.0.0.1:17857")

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

	entry := newCore(t, "entry-reject", "127.0.0.1:17966")
	worker := newCore(t, "worker-reject", "127.0.0.1:17967")
	startPair(t, ctx, entry, worker, "127.0.0.1:17966", "127.0.0.1:17967")

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
	entry := newCore(t, "entry-rot", "127.0.0.1:17976")
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

// TestHelloRejectAuthSurfaces exercises the whole rejection verdict: a peer
// whose secret does not match gets an explicit hello_reject before the close,
// its MaintainPeer reports ErrAuthRejected (not a dropped-link nil), and a
// one-shot probe reads "online but refusing our credential" instead of the
// generic handshake timeout that used to read like a network flap.
func TestHelloRejectAuthSurfaces(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := newCore(t, "srv-auth", "127.0.0.1:17986")
	dialer := newCore(t, "cli-auth", "127.0.0.1:17987")
	dialer.SetSharedSecret("wrong-secret")

	if err := server.Register(ctx); err != nil {
		t.Fatalf("register server: %v", err)
	}
	if err := dialer.Register(ctx); err != nil {
		t.Fatalf("register dialer: %v", err)
	}
	go func() { _ = server.Listen(ctx, "127.0.0.1:17986") }()

	// The listener binds asynchronously; the first dial attempts may hit a
	// not-yet-bound socket — real errors until the reject verdict arrives.
	var merr error
	deadline := time.Now().Add(10 * time.Second)
	for {
		merr = dialer.MaintainPeer(ctx, "127.0.0.1:17986")
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
		"127.0.0.1:17986", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "rejected our authentication") {
		t.Fatalf("ProbePeer err = %v, want the auth-rejection diagnosis", err)
	}
}

// startPair boots two cores and wires them together, failing the test on error.
func startPair(t *testing.T, ctx context.Context, entry, worker *Core, entryAddr, workerAddr string) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("startup: %v", err)
		}
	}
	must(entry.Register(ctx))
	must(worker.Register(ctx))

	ed := make(chan error, 1)
	wd := make(chan error, 1)
	go func() { ed <- entry.Listen(ctx, entryAddr) }()
	go func() { wd <- worker.Listen(ctx, workerAddr) }()

	// Listeners bind inside Listen; retry the dial until both are up instead
	// of sleeping a fixed 200ms — loaded CI runners can exceed that.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := entry.DialPeer(ctx, workerAddr); err == nil {
			break
		} else if time.Now().After(deadline) {
			must(err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Peer registration lands asynchronously: DialPeer returns once our hello
	// went out, but peers[id] is only populated once handleInbound reads the
	// reply. A fixed sleep raced slow runners (send → "peer not connected"),
	// so wait until both directions see a live conn.
	waitPeer(t, entry, worker.node.id)
	waitPeer(t, worker, entry.node.id)
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
func newCoreWithNative(t *testing.T, id, addr string, native ledger.NativeAbility) *Core {
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

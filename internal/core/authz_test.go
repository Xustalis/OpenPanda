package core

import (
	"context"
	"testing"
	"time"

	"github.com/xenith/panda/internal/bus"
	"github.com/xenith/panda/internal/ctxstore"
)

// Wire-protocol authorization regression tests (P1-1/2/11, batch 1 of the
// 2026-08-15 deep review): an authenticated peer that is NOT the task's
// current executor must not be able to move the task's state.

// setupDispatchedTask creates a task on c's store as if c had delegated it to
// target: submitted -> queued -> dispatched(target), with a lease.
func setupDispatchedTask(t *testing.T, c *Core, taskID, target string) Task {
	t.Helper()
	ctx := context.Background()
	tk, err := c.store.CreateWithID(ctx, taskID, "", "", "test task", c.nodeID, []string{c.nodeID})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := c.store.Queue(ctx, taskID, c.nodeID); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.Dispatch(ctx, taskID, c.nodeID, target); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	tk, err = c.store.Get(ctx, taskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return tk
}

// TestResultFromNonExecutorIgnored verifies P1-1: a task_result from a peer
// that is neither the lease holder nor the dispatch target must not change the
// task, even with a matching attempt id.
func TestResultFromNonExecutorIgnored(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "victim", "127.0.0.1:17901")
	tk := setupDispatchedTask(t, c, "t-forge", "real-worker")

	env, err := bus.NewEnvelope(bus.MsgTaskResult, "mallory", "m1", bus.TaskResultPayload{
		TaskID: "t-forge", AttemptID: tk.AttemptID, OK: true, Stdout: "forged",
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	c.handleResult(ctx, env)

	got, err := c.store.Get(ctx, "t-forge")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != StateDispatched {
		t.Fatalf("forged result moved task to %s, want dispatched", got.State)
	}
	if got.ResultJSON != "" {
		t.Fatalf("forged result stored: %s", got.ResultJSON)
	}
}

// TestResultFromExecutorAccepted is the positive control: the recorded
// dispatch target's result is applied.
func TestResultFromExecutorAccepted(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "parent", "127.0.0.1:17903")
	tk := setupDispatchedTask(t, c, "t-legit", "worker-b")

	env, err := bus.NewEnvelope(bus.MsgTaskResult, "worker-b", "m2", bus.TaskResultPayload{
		TaskID: "t-legit", AttemptID: tk.AttemptID, OK: true, Stdout: "real output",
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	c.handleResult(ctx, env)

	got, err := c.store.Get(ctx, "t-legit")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != StateDone {
		t.Fatalf("legit result left task %s, want done", got.State)
	}
}

// TestResultEmptyAttemptRejected verifies P1-1's second half: an empty
// AttemptID must not skip the stale-attempt check — it is rejected outright,
// including on the unknown-task reconstruction path.
func TestResultEmptyAttemptRejected(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "parent", "127.0.0.1:17905")

	// Unknown task + empty attempt: must not reconstruct.
	env, err := bus.NewEnvelope(bus.MsgTaskResult, "worker-b", "m3", bus.TaskResultPayload{
		TaskID: "t-noattempt", OK: true, Stdout: "x",
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	c.handleResult(ctx, env)
	if _, err := c.store.Get(ctx, "t-noattempt"); err == nil {
		t.Fatalf("empty-attempt result created a task row")
	}

	// Known task + empty attempt: must not terminate.
	tk := setupDispatchedTask(t, c, "t-known", "worker-b")
	_ = tk
	env2, _ := bus.NewEnvelope(bus.MsgTaskResult, "worker-b", "m4", bus.TaskResultPayload{
		TaskID: "t-known", OK: true, Stdout: "x",
	})
	c.handleResult(ctx, env2)
	got, _ := c.store.Get(ctx, "t-known")
	if got.State != StateDispatched {
		t.Fatalf("empty-attempt result moved task to %s", got.State)
	}
}

// TestDeclineFromNonExecutorIgnored verifies P1-2: only the current executor
// may bounce a dispatched task back to queued.
func TestDeclineFromNonExecutorIgnored(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "parent", "127.0.0.1:17907")
	setupDispatchedTask(t, c, "t-decline", "worker-c")

	env, err := bus.NewEnvelope(bus.MsgTaskDecline, "mallory", "m5", bus.TaskDeclinePayload{
		TaskID: "t-decline", Reason: "forged decline",
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	c.handleDecline(ctx, env)

	got, _ := c.store.Get(ctx, "t-decline")
	if got.State != StateDispatched {
		t.Fatalf("forged decline moved task to %s, want dispatched", got.State)
	}

	// Positive control: the real executor's decline applies.
	env2, _ := bus.NewEnvelope(bus.MsgTaskDecline, "worker-c", "m6", bus.TaskDeclinePayload{
		TaskID: "t-decline", Reason: "busy",
	})
	c.handleDecline(ctx, env2)
	got, _ = c.store.Get(ctx, "t-decline")
	if got.State != StateQueued {
		t.Fatalf("legit decline left task %s, want queued", got.State)
	}
}

// TestAcceptFromNonTargetIgnored verifies the handleAccept half of P1-3: a
// peer that was not dispatched to must not be able to claim the lease.
func TestAcceptFromNonTargetIgnored(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "parent", "127.0.0.1:17909")
	setupDispatchedTask(t, c, "t-accept", "worker-d")

	env, err := bus.NewEnvelope(bus.MsgTaskAccept, "mallory", "m7", bus.TaskAcceptPayload{TaskID: "t-accept"})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	c.handleAccept(ctx, env)

	got, _ := c.store.Get(ctx, "t-accept")
	if got.State != StateDispatched || got.OwnerNode == "mallory" {
		t.Fatalf("lease stolen: state=%s owner=%s", got.State, got.OwnerNode)
	}
}

// TestContextAckFromNonSourceIgnored verifies P1-11: a context_ack from a
// node other than the fetch target must not fail the parked task.
func TestContextAckFromNonSourceIgnored(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "exec", "127.0.0.1:17911")
	tk := setupDispatchedTask(t, c, "t-ctx", "exec")
	if err := c.store.SetWaitingContext(ctx, "t-ctx", c.nodeID); err != nil {
		t.Fatalf("waiting: %v", err)
	}
	c.pendingCtx.Store("t-ctx", &pendingContext{intent: "x", ctxType: "file", source: "context-src"})

	env, err := bus.NewEnvelope(bus.MsgContextAck, "mallory", "m8", bus.ContextAckPayload{
		TaskID: "t-ctx", OK: false,
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	c.handleContextAck(ctx, env)

	got, _ := c.store.Get(ctx, "t-ctx")
	if got.State != StateWaitingCtx {
		t.Fatalf("forged context_ack moved task to %s, want waiting_context", got.State)
	}
	if _, ok := c.pendingCtx.Load("t-ctx"); !ok {
		t.Fatalf("pending entry consumed by forged ack")
	}

	// Positive control: the real source's ack is processed (hash-verified).
	data := []byte("snapshot")
	env2, _ := bus.NewEnvelope(bus.MsgContextAck, "context-src", "m9", bus.ContextAckPayload{
		TaskID: "t-ctx", Hash: ctxstore.Hash(data), OK: true, Data: data,
	})
	// The resumed run has no matching executor command here ("x" matches
	// sys:info? no intent routing), so it may fail — what matters is the ack
	// was accepted and the pending entry consumed.
	c.handleContextAck(ctx, env2)
	if _, ok := c.pendingCtx.Load("t-ctx"); ok {
		t.Fatalf("pending entry not consumed by legit ack")
	}
	_ = tk
}

// TestAcceptGuardedAgainstCancel verifies the store-level guard of P1-3: once
// a task leaves dispatched, Accept fails instead of resurrecting it.
func TestAcceptGuardedAgainstCancel(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	s := NewTaskStore(db, testLogger())
	tk, err := s.Create(ctx, "", "", "guarded", "owner", []string{"owner"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, fn := range []func() error{
		func() error { return s.Queue(ctx, tk.TaskID, "owner") },
		func() error { return s.Dispatch(ctx, tk.TaskID, "owner", "worker") },
		func() error { return s.Cancel(ctx, tk.TaskID) },
	} {
		if err := fn(); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	if err := s.Accept(ctx, tk.TaskID, "worker"); err == nil {
		t.Fatalf("Accept on cancelled task must fail")
	}
	got, _ := s.Get(ctx, tk.TaskID)
	if got.State != StateCancelled {
		t.Fatalf("cancelled task resurrected to %s", got.State)
	}
}

// TestApproveRejectGuarded verifies P2-8: approve/reject carry a state guard,
// so a second concurrent review decision loses instead of overwriting.
func TestApproveRejectGuarded(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	s := NewTaskStore(db, testLogger())
	tk, _ := s.Create(ctx, "", "", "reviewed", "owner", []string{"owner"})
	if err := s.Queue(ctx, tk.TaskID, "owner"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := s.Dispatch(ctx, tk.TaskID, "owner", "owner"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := s.Accept(ctx, tk.TaskID, "owner"); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := s.Pause(ctx, tk.TaskID, "owner", "drift"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err := s.Approve(ctx, tk.TaskID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := s.Reject(ctx, tk.TaskID, "second opinion"); err == nil {
		t.Fatalf("Reject after Approve must fail")
	}
	got, _ := s.Get(ctx, tk.TaskID)
	if got.State != StateDone {
		t.Fatalf("approved task overwritten to %s", got.State)
	}
}

// TestReviewLeaseClearedAndNotExpired verifies P1-8: entering review clears
// the lease, and ExpireTasks never scans review tasks.
func TestReviewLeaseClearedAndNotExpired(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	s := NewTaskStore(db, testLogger())
	tk, _ := s.Create(ctx, "", "", "review-lease", "owner", []string{"owner"})
	if err := s.Queue(ctx, tk.TaskID, "owner"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := s.Dispatch(ctx, tk.TaskID, "owner", "owner"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := s.Accept(ctx, tk.TaskID, "owner"); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := s.SetLease(ctx, tk.TaskID, 1000); err != nil {
		t.Fatalf("lease: %v", err)
	}
	if err := s.Pause(ctx, tk.TaskID, "owner", "check"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	got, _ := s.Get(ctx, tk.TaskID)
	if got.LeaseExpires != 0 {
		t.Fatalf("lease not cleared on review: %d", got.LeaseExpires)
	}
	// Even with a forged past-due lease, review tasks must not be expired.
	if _, err := s.db.Exec(`UPDATE tasks SET lease_expires_at=? WHERE task_id=?`, s.now()-1, tk.TaskID); err != nil {
		t.Fatalf("stamp lease: %v", err)
	}
	expired, err := s.ExpireTasks(ctx)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if len(expired) != 0 {
		t.Fatalf("review task expired: %v", expired)
	}
	got, _ = s.Get(ctx, tk.TaskID)
	if got.State != StateReview {
		t.Fatalf("review task killed: %s", got.State)
	}
}

// TestWaitingContextCarriesLease verifies P1-6 end-to-end at the handler
// level: a pointer-miss delegation parks the task with a non-zero lease even
// when the wire payload carried no timeout.
func TestWaitingContextCarriesLease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := newCore(t, "ctx-src", "127.0.0.1:17913")
	dst := newCore(t, "ctx-dst", "127.0.0.1:17914")
	startPair(t, ctx, src, dst, "127.0.0.1:17913", "127.0.0.1:17914")

	env, err := bus.NewEnvelope(bus.MsgTaskDelegate, "ctx-src", "m10", bus.TaskDelegatePayload{
		TaskID: "t-waitlease", Title: "needs ctx", ContextType: "file",
		ContextLevel: "pointer", ContextHash: "sha256:missing",
		Intent: "do something", Requires: []string{"sys:info"},
		Chain: []string{"ctx-src"},
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if err := src.sendTo("ctx-dst", env); err != nil {
		t.Fatalf("send: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tk, err := dst.store.Get(ctx, "t-waitlease")
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		switch tk.State {
		case StateWaitingCtx:
			if tk.LeaseExpires == 0 {
				t.Fatalf("waiting_context task has no lease")
			}
			return
		case StateFailed:
			// The context source answered OK:false before we polled; the fast
			// path is fine, but the lease must still have been stamped (P1-6).
			if tk.LeaseExpires == 0 {
				t.Fatalf("task parked without a lease before failing")
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	tk, _ := dst.store.Get(ctx, "t-waitlease")
	t.Fatalf("task not parked: %+v", tk)
}

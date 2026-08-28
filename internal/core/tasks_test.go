package core

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/storage"
)

func newTestStore(t *testing.T) *TaskStore {
	t.Helper()
	return NewTaskStore(openTestDB(t), testLogger())
}

func createTask(t *testing.T, s *TaskStore, parent, title, owner string) Task {
	t.Helper()
	tk, err := s.Create(context.Background(), parent, "proj", title, owner, []string{owner})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return tk
}

// TestRecordEventBumpsUpdatedAt guards the web-console live-refresh path (P0
// §1.5): an appended event that does not change state must still move
// tasks.updated_at, because the panel's SSE change detector fingerprints on
// (id, state, updated_at). Without the bump, progress/trace/tool events never
// flip the fingerprint and the console silently misses them.
func TestRecordEventBumpsUpdatedAt(t *testing.T) {
	s := newTestStore(t)
	// A monotonic clock so each store timestamp is strictly newer than the last,
	// making the bump observable regardless of wall-clock resolution.
	var tick int64
	s.now = func() int64 { tick++; return tick }
	ctx := context.Background()

	tk := createTask(t, s, "", "event bump", "node")
	before, err := s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get before: %v", err)
	}
	if err := s.RecordEvent(ctx, tk.TaskID, EvProgress, map[string]any{"note": "step"}); err != nil {
		t.Fatalf("record event: %v", err)
	}
	after, err := s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get after: %v", err)
	}
	if !(after.UpdatedAt > before.UpdatedAt) {
		t.Fatalf("updated_at not bumped by event: before=%d after=%d", before.UpdatedAt, after.UpdatedAt)
	}
	if after.State != before.State {
		t.Fatalf("state changed unexpectedly: %s -> %s", before.State, after.State)
	}
}

// TestConcurrentTransitionSingleWinner races two goroutines closing the same
// running task. The state/owner CAS guard (P1-2) must let exactly one win; the
// other loses with ErrConflict (or ErrIllegal if it observed the new state in a
// pre-check). Run under -race this also exercises the SetOnReview lock (P2-6).
func TestConcurrentTransitionSingleWinner(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := createTask(t, s, "", "race", "node")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Queue(ctx, tk.TaskID, "node"))
	must(s.Dispatch(ctx, tk.TaskID, "node", "node"))
	must(s.Accept(ctx, tk.TaskID, "node"))

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = s.Complete(ctx, tk.TaskID, "node", map[string]any{"ok": true})
	}()
	go func() {
		defer wg.Done()
		errs[1] = s.Fail(ctx, tk.TaskID, "node", "boom")
	}()
	wg.Wait()

	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
			continue
		}
		if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrIllegal) {
			t.Fatalf("unexpected transition error: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("exactly one transition must win, got %d (errs=%v)", wins, errs)
	}
	got, err := s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != StateDone && got.State != StateFailed {
		t.Fatalf("final state = %s, want done or failed", got.State)
	}
}

func TestSetOnReviewFires(t *testing.T) {
	s := newTestStore(t)
	fired := make(chan Task, 2)
	s.SetOnReview(func(tk Task) { fired <- tk })

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	// Pause path: running -> review (scope-drift style intercept).
	tk := createTask(t, s, "", "pause case", "node")
	must(s.Queue(context.Background(), tk.TaskID, "node"))
	must(s.Dispatch(context.Background(), tk.TaskID, "node", "node"))
	must(s.Accept(context.Background(), tk.TaskID, "node"))
	must(s.Pause(context.Background(), tk.TaskID, "node", "scope drift"))

	// Review path: failed -> review (retry budget spent).
	tk2 := createTask(t, s, "", "review case", "node")
	must(s.Queue(context.Background(), tk2.TaskID, "node"))
	must(s.Dispatch(context.Background(), tk2.TaskID, "node", "node"))
	must(s.Accept(context.Background(), tk2.TaskID, "node"))
	must(s.Fail(context.Background(), tk2.TaskID, "node", "boom"))
	must(s.Review(context.Background(), tk2.TaskID, "node", "retry spent"))

	got := make(map[string]string)
	deadline := time.After(5 * time.Second)
	for len(got) < 2 {
		select {
		case fired := <-fired:
			got[fired.TaskID] = fired.State
		case <-deadline:
			t.Fatalf("only %d review callbacks fired, want 2", len(got))
		}
	}
	if got[tk.TaskID] != StateReview || got[tk2.TaskID] != StateReview {
		t.Fatalf("callbacks = %v, want both in review", got)
	}
}

func TestStateMachineHappyPath(t *testing.T) {
	s := newTestStore(t)
	tk := createTask(t, s, "", "build ios", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	must(s.Queue(context.Background(), tk.TaskID, "root"))
	must(s.Dispatch(context.Background(), tk.TaskID, "root", "win"))
	must(s.Accept(context.Background(), tk.TaskID, "win"))
	must(s.Complete(context.Background(), tk.TaskID, "win", map[string]any{"exit_code": 0}))

	got, err := s.Get(context.Background(), tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != StateDone {
		t.Fatalf("state = %s, want done", got.State)
	}
}

func TestIllegalTransitions(t *testing.T) {
	s := newTestStore(t)
	tk := createTask(t, s, "", "t", "root")

	// submitted -> running is illegal (must pass through queued/dispatched).
	if err := s.Accept(context.Background(), tk.TaskID, "root"); !errors.Is(err, ErrIllegal) &&
		!errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrIllegal/ErrConflict for submitted->running, got %v", err)
	}
	// done is terminal.
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(context.Background(), tk.TaskID, "root"))
	must(s.Dispatch(context.Background(), tk.TaskID, "root", "win"))
	must(s.Accept(context.Background(), tk.TaskID, "win"))
	must(s.Complete(context.Background(), tk.TaskID, "win", nil))
	// failed transition from done must be rejected.
	if err := s.Fail(context.Background(), tk.TaskID, "win", "late"); !errors.Is(err, ErrIllegal) {
		t.Fatalf("expected ErrIllegal for done->failed, got %v", err)
	}
}

func TestOwnerEnforcement(t *testing.T) {
	s := newTestStore(t)
	tk := createTask(t, s, "", "t", "root")

	if err := s.Queue(context.Background(), tk.TaskID, "intruder"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for non-owner, got %v", err)
	}
}

func TestCanTransitionCancelEdges(t *testing.T) {
	for _, from := range []string{StateSubmitted, StateQueued, StateDispatched, StateWaitingCtx, StateRunning, StateReview, StateFailed} {
		if !CanTransition(from, StateCancelled) {
			t.Fatalf("%s -> cancelled must be legal", from)
		}
	}
	for _, term := range []string{StateDone, StateCancelled, StateExpired} {
		if CanTransition(term, StateCancelled) {
			t.Fatalf("%s -> cancelled must be illegal (terminal)", term)
		}
	}
}

func TestCancelFromWaitingContext(t *testing.T) {
	s := newTestStore(t)
	tk := createTask(t, s, "", "pause", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(context.Background(), tk.TaskID, "root"))
	must(s.Dispatch(context.Background(), tk.TaskID, "root", "root"))
	must(s.SetWaitingContext(context.Background(), tk.TaskID, "root"))

	// A task parked waiting for context must still be cancellable (the state
	// machine previously rejected waiting_context -> cancelled, so Cancel
	// would have written an illegal state).
	if err := s.Cancel(context.Background(), tk.TaskID); err != nil {
		t.Fatalf("cancel from waiting_context: %v", err)
	}
	got, err := s.Get(context.Background(), tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != StateCancelled {
		t.Fatalf("state = %s, want cancelled", got.State)
	}
}

func TestCancelCascade(t *testing.T) {
	s := newTestStore(t)
	parent := createTask(t, s, "", "parent", "root")
	child1 := createTask(t, s, parent.TaskID, "child1", "root")
	child2 := createTask(t, s, parent.TaskID, "child2", "root")
	grand := createTask(t, s, child1.TaskID, "grandchild", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	for _, id := range []string{parent.TaskID, child1.TaskID, child2.TaskID, grand.TaskID} {
		must(s.Queue(context.Background(), id, "root"))
	}

	ids, err := s.CancelCascade(context.Background(), parent.TaskID)
	if err != nil {
		t.Fatalf("cancel cascade: %v", err)
	}
	if len(ids) != 4 {
		t.Fatalf("cancelled %d, want 4", len(ids))
	}
	for _, id := range []string{parent.TaskID, child1.TaskID, child2.TaskID, grand.TaskID} {
		got, err := s.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if got.State != StateCancelled {
			t.Fatalf("task %s state = %s, want cancelled", id, got.State)
		}
	}
}

// TestCancelCascadeSkipsTerminalRoot verifies the count reflects only tasks
// actually transitioned: a done root with an active child cancels just the child.
func TestCancelCascadeSkipsTerminalRoot(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := createTask(t, s, "", "root", "root")
	child := createTask(t, s, root.TaskID, "child", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	// Root is done (terminal); child is queued (active).
	must(s.Queue(ctx, root.TaskID, "root"))
	must(s.Dispatch(ctx, root.TaskID, "root", "win"))
	must(s.Accept(ctx, root.TaskID, "win"))
	must(s.Complete(ctx, root.TaskID, "win", nil))
	must(s.Queue(ctx, child.TaskID, "root"))

	ids, err := s.CancelCascade(ctx, root.TaskID)
	if err != nil {
		t.Fatalf("cancel cascade: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("cancelled %d, want 1 (root already terminal)", len(ids))
	}
	got, _ := s.Get(ctx, child.TaskID)
	if got.State != StateCancelled {
		t.Fatalf("child state = %s, want cancelled", got.State)
	}
}

// TestCancelRetriesAfterConcurrentAccept reproduces the cancel-propagation
// flake deterministically: a task_cancel and the executor's accept race on a
// dispatched task, and the accept commits between Cancel's pre-check and its
// guarded UPDATE. The cancel must not be dropped with ErrConflict — the task
// merely moved between active states and is still cancellable — so Cancel
// retries against the fresh state and lands cancelled. The pre-fix behavior
// returned ErrConflict here, the wire handler dropped the cancel, and the
// executor ran to completion under a node that believed the task cancelled.
//
// The race is staged through the store's now hook: it fires while Cancel
// evaluates its UPDATE arguments (before the statement executes, while the
// deferred transaction holds no locks) and commits the concurrent accept
// through a second connection. A second connection is required because the
// store's pool is capped at one connection, and a :memory: database is not
// shared between pools — hence the temp file.
func TestCancelRetriesAfterConcurrentAccept(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cancel.db")
	db1, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open db1: %v", err)
	}
	defer db1.Close()
	if err := storage.Migrate(db1); err != nil {
		t.Fatalf("migrate db1: %v", err)
	}
	db2, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open db2: %v", err)
	}
	defer db2.Close()
	if err := storage.Migrate(db2); err != nil {
		t.Fatalf("migrate db2: %v", err)
	}

	canceller := NewTaskStore(db1, testLogger()) // the node cancelling
	executor := NewTaskStore(db2, testLogger())  // the node accepting

	tk := createTask(t, canceller, "", "race", "root")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(canceller.Queue(ctx, tk.TaskID, "root"))
	must(canceller.Dispatch(ctx, tk.TaskID, "root", "leaf"))

	// Arm a one-shot hook: the next now() call (Cancel's UPDATE timestamp)
	// first commits the executor's accept on the second connection.
	armed := true
	canceller.now = func() int64 {
		if !armed {
			return storage.Now()
		}
		armed = false
		if err := executor.Accept(ctx, tk.TaskID, "leaf"); err != nil {
			t.Errorf("staged concurrent accept: %v", err)
		}
		return storage.Now()
	}

	if err := canceller.Cancel(ctx, tk.TaskID); err != nil {
		t.Fatalf("cancel after concurrent accept: %v", err)
	}
	got, err := canceller.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != StateCancelled {
		t.Fatalf("state = %s, want cancelled (cancel was dropped)", got.State)
	}
}

// TestCancelIdempotentWhenConcurrentlyClosed verifies the other outcome of
// the same race: when the task reaches a terminal state (here done) between
// Cancel's pre-check and its guarded write, Cancel returns nil instead of
// ErrConflict — the recorded result wins and a concurrent cancel is
// idempotent success, matching the pre-check's behavior for an already-
// terminal task.
func TestCancelIdempotentWhenConcurrentlyClosed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "closed.db")
	db1, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open db1: %v", err)
	}
	defer db1.Close()
	if err := storage.Migrate(db1); err != nil {
		t.Fatalf("migrate db1: %v", err)
	}
	db2, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open db2: %v", err)
	}
	defer db2.Close()
	if err := storage.Migrate(db2); err != nil {
		t.Fatalf("migrate db2: %v", err)
	}

	canceller := NewTaskStore(db1, testLogger())
	executor := NewTaskStore(db2, testLogger())

	tk := createTask(t, canceller, "", "closed", "root")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(canceller.Queue(ctx, tk.TaskID, "root"))
	must(canceller.Dispatch(ctx, tk.TaskID, "root", "leaf"))

	armed := true
	canceller.now = func() int64 {
		if !armed {
			return storage.Now()
		}
		armed = false
		if err := executor.Accept(ctx, tk.TaskID, "leaf"); err != nil {
			t.Errorf("staged accept: %v", err)
			return storage.Now()
		}
		if err := executor.Complete(ctx, tk.TaskID, "leaf", map[string]any{"ok": true}); err != nil {
			t.Errorf("staged complete: %v", err)
		}
		return storage.Now()
	}

	if err := canceller.Cancel(ctx, tk.TaskID); err != nil {
		t.Fatalf("cancel on concurrently completed task: %v", err)
	}
	got, err := canceller.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != StateDone {
		t.Fatalf("state = %s, want done (recorded result must win over cancel)", got.State)
	}
}

// TestCancelCascadeCycle verifies a parent_id cycle terminates instead of
// recursing forever, cancelling every node on the cycle exactly once.
func TestCancelCascadeCycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := createTask(t, s, "", "a", "root")
	b := createTask(t, s, a.TaskID, "b", "root")
	// Close the cycle: a's parent is now b.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET parent_id=? WHERE task_id=?`, b.TaskID, a.TaskID); err != nil {
		t.Fatalf("link cycle: %v", err)
	}
	for _, id := range []string{a.TaskID, b.TaskID} {
		if err := s.Queue(ctx, id, "root"); err != nil {
			t.Fatalf("queue %s: %v", id, err)
		}
	}
	ids, err := s.CancelCascade(ctx, a.TaskID)
	if err != nil {
		t.Fatalf("cancel cascade: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("cancelled %d, want 2", len(ids))
	}
	for _, id := range []string{a.TaskID, b.TaskID} {
		got, err := s.Get(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if got.State != StateCancelled {
			t.Fatalf("task %s state = %s, want cancelled", id, got.State)
		}
	}
}

// TestRotateAttemptOwnerGuarded verifies a non-owner cannot rotate another
// owner's attempt id.
func TestRotateAttemptOwnerGuarded(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := createTask(t, s, "", "t", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(ctx, tk.TaskID, "root"))
	must(s.Dispatch(ctx, tk.TaskID, "root", "win"))
	must(s.Accept(ctx, tk.TaskID, "win"))

	if _, err := s.RotateAttempt(ctx, tk.TaskID, "intruder"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for non-owner rotation, got %v", err)
	}
}

func TestAttemptRotationRejectsOldResult(t *testing.T) {
	s := newTestStore(t)
	tk := createTask(t, s, "", "t", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(context.Background(), tk.TaskID, "root"))
	must(s.Dispatch(context.Background(), tk.TaskID, "root", "win"))
	must(s.Accept(context.Background(), tk.TaskID, "win"))

	oldAttempt := tk.AttemptID
	// Rotate attempt to simulate a transfer/retry.
	newAttempt, err := s.RotateAttempt(context.Background(), tk.TaskID, "win")
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if newAttempt == oldAttempt {
		t.Fatalf("attempt did not rotate")
	}

	// Old attempt completes: must be rejected because the current state
	// after rotation is still running but the attempt id differs. Our
	// Complete() validates state (running) but not attempt id — so this
	// test documents that attempt id matching is enforced at the message
	// layer (recv/handlers), not in the store.
	got, err := s.Get(context.Background(), tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AttemptID != newAttempt {
		t.Fatalf("attempt = %s, want %s", got.AttemptID, newAttempt)
	}
}

func TestEventsRecorded(t *testing.T) {
	s := newTestStore(t)
	tk := createTask(t, s, "", "t", "root")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(context.Background(), tk.TaskID, "root"))

	events, err := s.Events(context.Background(), tk.TaskID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("expected >= 2 events, got %d", len(events))
	}
	if events[0].Type != EvSubmit || events[1].Type != EvQueue {
		t.Fatalf("unexpected event sequence: %v", []string{events[0].Type, events[1].Type})
	}
}

// TestApplyStateGuardRejectsStaleWrite exercises the state-only guard used by
// the remote-result writers: a transition that read a stale state (dispatched)
// must conflict once the task has actually moved on (running), rather than
// silently overwriting the newer state.
func TestApplyStateGuardRejectsStaleWrite(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := createTask(t, s, "", "t", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(ctx, tk.TaskID, "root"))
	must(s.Dispatch(ctx, tk.TaskID, "root", "win"))
	// Move to running, invalidating any transition that read dispatched.
	must(s.Accept(ctx, tk.TaskID, "win"))

	if err := s.applyState(ctx, tk.TaskID, StateDispatched, StateDone, "root", tk.AttemptID, EvResult, nil, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale write = %v, want ErrConflict", err)
	}
	got, err := s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != StateRunning {
		t.Fatalf("state = %s, want running (stale write must not overwrite)", got.State)
	}
}

// TestRemoteResultDoesNotOverwriteTerminal verifies CompleteFromRemote and
// FailFromRemote are idempotent against an already-closed task: a late result
// (or failure) must not resurrect a done task.
func TestRemoteResultDoesNotOverwriteTerminal(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := createTask(t, s, "", "t", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(ctx, tk.TaskID, "root"))
	must(s.Dispatch(ctx, tk.TaskID, "root", "win"))
	must(s.Accept(ctx, tk.TaskID, "win"))
	must(s.Complete(ctx, tk.TaskID, "win", map[string]any{"ok": true}))

	if err := s.FailFromRemote(ctx, tk.TaskID, "root", "late failure"); err != nil {
		t.Fatalf("fail from remote on done: %v", err)
	}
	got, err := s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != StateDone {
		t.Fatalf("state = %s, want done (late failure must not overwrite)", got.State)
	}
}

// TestForceFailDoesNotOverwriteTerminal verifies the timeout monitor cannot
// resurrect a task that completed concurrently with the lease expiring.
func TestForceFailDoesNotOverwriteTerminal(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := createTask(t, s, "", "t", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(ctx, tk.TaskID, "root"))
	must(s.Dispatch(ctx, tk.TaskID, "root", "root"))
	must(s.Accept(ctx, tk.TaskID, "root"))
	must(s.Complete(ctx, tk.TaskID, "root", map[string]any{"ok": true}))

	if err := s.ForceFail(ctx, tk.TaskID, "lease expired"); err != nil {
		t.Fatalf("force fail on done: %v", err)
	}
	got, err := s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != StateDone {
		t.Fatalf("state = %s, want done (force fail must not overwrite)", got.State)
	}
}

// TestTaskEventChainValid verifies the per-task hash chain is intact after a
// normal state-machine walk.
func TestTaskEventChainValid(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := createTask(t, s, "", "chain", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(ctx, tk.TaskID, "root"))
	must(s.Dispatch(ctx, tk.TaskID, "root", "win"))
	must(s.Accept(ctx, tk.TaskID, "win"))
	must(s.Complete(ctx, tk.TaskID, "win", map[string]any{"ok": true}))

	if err := s.VerifyTaskEventChain(ctx, tk.TaskID); err != nil {
		t.Fatalf("verify task event chain: %v", err)
	}
}

// TestTaskEventChainTamperDetect verifies VerifyTaskEventChain detects a
// mutated event payload: changing event 1's data invalidates event 2's link.
func TestTaskEventChainTamperDetect(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := createTask(t, s, "", "tamper", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(ctx, tk.TaskID, "root"))
	// Create a second event so tampering the first breaks the forward link.
	must(s.Dispatch(ctx, tk.TaskID, "root", "win"))

	res, err := s.db.ExecContext(ctx,
		`UPDATE task_events SET data_json=? WHERE task_id=? AND type=?`,
		`{"tampered":true}`, tk.TaskID, EvQueue)
	if err != nil {
		t.Fatalf("tamper event: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("expected to tamper 1 event, got %d", n)
	}

	if err := s.VerifyTaskEventChain(ctx, tk.TaskID); err == nil {
		t.Fatalf("expected tamper detection error, got nil")
	}
}

// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/defense"
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

// TestListBySession pins the read behind the chat header's orchestration card:
// only the tasks linked to the asked-for conversation, newest first, and an
// empty session id returns nothing instead of the whole table.
func TestListBySession(t *testing.T) {
	s := newTestStore(t)
	var tick int64
	s.now = func() int64 { tick++; return tick }
	ctx := context.Background()

	a := createTask(t, s, "", "thread a, first", "node")
	b := createTask(t, s, "", "thread b, only", "node")
	c := createTask(t, s, "", "thread a, second", "node")
	for _, link := range []struct{ id, session string }{
		{a.TaskID, "sess-a"},
		{b.TaskID, "sess-b"},
		{c.TaskID, "sess-a"},
	} {
		if err := s.SetSessionID(ctx, link.id, link.session); err != nil {
			t.Fatalf("link %s to %s: %v", link.id, link.session, err)
		}
	}

	got, err := s.ListBySession(ctx, "sess-a")
	if err != nil {
		t.Fatalf("list by session: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 tasks for sess-a, got %d", len(got))
	}
	// Newest first: `c` was created after `a`.
	if got[0].TaskID != c.TaskID || got[1].TaskID != a.TaskID {
		t.Fatalf("unexpected order: %s, %s", got[0].TaskID, got[1].TaskID)
	}

	// An unlinked task must not leak in, and an empty id is "no thread yet".
	if other, err := s.ListBySession(ctx, "sess-b"); err != nil || len(other) != 1 || other[0].TaskID != b.TaskID {
		t.Fatalf("sess-b: got %d rows (err=%v), want just %s", len(other), err, b.TaskID)
	}
	if none, err := s.ListBySession(ctx, ""); err != nil || len(none) != 0 {
		t.Fatalf("empty session id: got %d rows (err=%v), want none", len(none), err)
	}
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

// TestEventsSinceAdvancesCursor pins the incremental timeline read the
// askengine's settle wait polls with: rows after the cursor only, oldest
// first, and a cursor at the tail yields nothing.
func TestEventsSinceAdvancesCursor(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := createTask(t, s, "", "cursor", "node")
	for _, note := range []string{"one", "two"} {
		if err := s.RecordEvent(ctx, tk.TaskID, EvProgress, map[string]any{"note": note}); err != nil {
			t.Fatalf("record %s: %v", note, err)
		}
	}
	all, err := s.Events(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(all) < 3 { // submit + two progress rows
		t.Fatalf("timeline = %d events, want at least 3", len(all))
	}
	tail := all[len(all)-1]
	rest, err := s.EventsSince(ctx, tk.TaskID, all[len(all)-2].ID)
	if err != nil {
		t.Fatalf("events since: %v", err)
	}
	if len(rest) != 1 || rest[0].ID != tail.ID {
		t.Fatalf("events since cursor = %+v, want just the tail row", rest)
	}
	if past, err := s.EventsSince(ctx, tk.TaskID, tail.ID); err != nil || len(past) != 0 {
		t.Fatalf("events past the tail = %+v (%v), want empty", past, err)
	}
	// Another task's rows never leak into the window.
	other := createTask(t, s, "", "other", "node")
	if err := s.RecordEvent(ctx, other.TaskID, EvProgress, map[string]any{"note": "x"}); err != nil {
		t.Fatalf("record other: %v", err)
	}
	if again, err := s.EventsSince(ctx, tk.TaskID, all[len(all)-2].ID); err != nil || len(again) != 1 {
		t.Fatalf("other task's events leaked: %+v (%v)", again, err)
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
// The race is staged through the store's preTxHook: it fires after Cancel's
// unlocked pre-check read but before BEGIN IMMEDIATE takes the write lock —
// the only window a concurrent commit can still hit now that transactions
// lock at BEGIN (a mid-transaction hook would deadlock on the lock the txn
// already holds). The staged accept commits through a second connection; a
// :memory: database is not shared between pools — hence the temp file.
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

	// Arm a one-shot hook: the first write transaction Cancel opens first
	// commits the executor's accept on the second connection, so the guarded
	// UPDATE lands on a row that moved since the pre-check read.
	armed := true
	canceller.preTxHook = func() {
		if !armed {
			return
		}
		armed = false
		if err := executor.Accept(ctx, tk.TaskID, "leaf"); err != nil {
			t.Errorf("staged concurrent accept: %v", err)
		}
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
	canceller.preTxHook = func() {
		if !armed {
			return
		}
		armed = false
		if err := executor.Accept(ctx, tk.TaskID, "leaf"); err != nil {
			t.Errorf("staged accept: %v", err)
			return
		}
		if err := executor.Complete(ctx, tk.TaskID, "leaf", map[string]any{"ok": true}); err != nil {
			t.Errorf("staged complete: %v", err)
		}
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

// TestRequeueForRetryAtomic verifies the retry loop's one-shot move: a failed
// task lands in dispatched with a fresh attempt_id and BOTH audit events
// (EvRetry + EvDelegate to self) in a single transaction — and a non-owner or
// a non-failed task gets nothing written at all.
func TestRequeueForRetryAtomic(t *testing.T) {
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
	must(s.Fail(ctx, tk.TaskID, "root", "boom"))

	// Wrong owner: conflict, and the row stays failed with its old attempt.
	if _, err := s.RequeueForRetry(ctx, tk.TaskID, "intruder", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for non-owner, got %v", err)
	}
	cur, err := s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if cur.State != StateFailed || cur.AttemptID != tk.AttemptID {
		t.Fatalf("conflict wrote state=%s attempt=%s", cur.State, cur.AttemptID)
	}

	aid, err := s.RequeueForRetry(ctx, tk.TaskID, "root", false)
	if err != nil {
		t.Fatalf("requeue for retry: %v", err)
	}
	if aid == "" || aid == tk.AttemptID {
		t.Fatalf("attempt not rotated: %q", aid)
	}
	cur, err = s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if cur.State != StateDispatched || cur.AttemptID != aid {
		t.Fatalf("state=%s attempt=%s, want dispatched/%s", cur.State, cur.AttemptID, aid)
	}
	// Both audit events landed: DispatchTarget (latest EvDelegate) points back
	// at the owner, and RetryCount saw the EvRetry.
	if tgt, err := s.DispatchTarget(ctx, tk.TaskID); err != nil || tgt != "root" {
		t.Fatalf("dispatch target = %q err=%v, want root", tgt, err)
	}
	if n, err := s.RetryCount(ctx, tk.TaskID); err != nil || n != 1 {
		t.Fatalf("retry count = %d err=%v, want 1", n, err)
	}
	// An unflagged retry is a hard failure: the checkpoint counter stays zero.
	if n, err := s.CheckpointRetries(ctx, tk.TaskID); err != nil || n != 0 {
		t.Fatalf("checkpoint count = %d err=%v, want 0", n, err)
	}
}

// TestCheckpointRetriesCountsSinceReview verifies the split retry accounting:
// a checkpoint-flagged EvRetry counts on the checkpoint budget only, the
// all-time hard RetryCount still sees it, and a review park resets the
// checkpoint count while leaving hard retries untouched (a human sending the
// task back grants fresh continuation rounds, not fresh failure budget).
func TestCheckpointRetriesCountsSinceReview(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := createTask(t, s, "", "t", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	// One fail→retry cycle: RequeueForRetry moves failed -> dispatched, the
	// run accepts it and fails again, leaving the row ready for the next.
	requeue := func(checkpoint bool) {
		t.Helper()
		_, err := s.RequeueForRetry(ctx, tk.TaskID, "root", checkpoint)
		must(err)
		must(s.Accept(ctx, tk.TaskID, "root"))
		must(s.Fail(ctx, tk.TaskID, "root", "boom again"))
	}

	must(s.Queue(ctx, tk.TaskID, "root"))
	must(s.Dispatch(ctx, tk.TaskID, "root", "root"))
	must(s.Accept(ctx, tk.TaskID, "root"))
	must(s.Fail(ctx, tk.TaskID, "root", "boom"))

	requeue(true)  // checkpoint 1
	requeue(false) // hard retry 1
	requeue(true)  // checkpoint 2

	if n, err := s.RetryCount(ctx, tk.TaskID); err != nil || n != 3 {
		t.Fatalf("retry count = %d err=%v, want 3 (all retries audit)", n, err)
	}
	if n, err := s.CheckpointRetries(ctx, tk.TaskID); err != nil || n != 2 {
		t.Fatalf("checkpoint count = %d err=%v, want 2", n, err)
	}

	// The review park splits the checkpoint epoch: retries after it count
	// fresh, retries before it are already accounted to the parked episode.
	// Reject stands in for "send it back": review -> failed so the next
	// checkpoint retry can land.
	must(s.Review(ctx, tk.TaskID, "root", "looked"))
	if n, err := s.CheckpointRetries(ctx, tk.TaskID); err != nil || n != 0 {
		t.Fatalf("post-review checkpoint count = %d err=%v, want 0", n, err)
	}
	if n, err := s.RetryCount(ctx, tk.TaskID); err != nil || n != 3 {
		t.Fatalf("post-review hard count = %d err=%v, want 3 (audit keeps history)", n, err)
	}
	must(s.Reject(ctx, tk.TaskID, "keep going"))
	requeue(true)
	if n, err := s.CheckpointRetries(ctx, tk.TaskID); err != nil || n != 1 {
		t.Fatalf("post-review checkpoint count = %d err=%v, want 1", n, err)
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

// TestLeaseExpiryReasonCarriesLastActivity pins the failure text a lease
// expiry leaves behind. A bare "lease expired" cannot tell a wedged adapter
// apart from a slow-but-alive one after the fact, so the newest progress note
// rides along — that is the only record of what the run was doing when the
// monitor gave up on it.
func TestLeaseExpiryReasonCarriesLastActivity(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tk := createTask(t, s, "", "wedged", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(ctx, tk.TaskID, "root"))
	must(s.Dispatch(ctx, tk.TaskID, "root", "root"))
	must(s.Accept(ctx, tk.TaskID, "root"))
	must(s.RecordEvent(ctx, tk.TaskID, "progress",
		map[string]any{"note": "no output for 3m45s — agent still running"}))
	// Terminal expiry verdicts only land once the persisted retry budget is
	// spent — with budget left the row would re-queue for re-route instead.
	for i := 0; i < maxPersistedRetries; i++ {
		must(s.RecordEvent(ctx, tk.TaskID, EvRetry, nil))
	}
	if _, err := s.db.Exec(`UPDATE tasks SET lease_expires_at=? WHERE task_id=?`,
		s.now()-1, tk.TaskID); err != nil {
		t.Fatalf("stamp lease: %v", err)
	}

	expired, err := s.ExpireTasks(ctx, "root")
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if len(expired) != 1 || expired[0].TaskID != tk.TaskID {
		t.Fatalf("expired = %v, want [%s]", expired, tk.TaskID)
	}
	got, err := s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != StateFailed {
		t.Fatalf("state = %s, want %s", got.State, StateFailed)
	}
	want := `"failed":"lease expired (last activity: no output for 3m45s — agent still running)"`
	if !strings.Contains(got.ResultJSON, want) {
		t.Fatalf("result_json = %s, want it to carry %s", got.ResultJSON, want)
	}
}

// TestLeaseExpiryReasonWithoutNotes keeps the old wording for a task that
// never emitted progress — an expiry before the adapter started has nothing
// to fold in, and "(last activity: )" would read as a bug.
func TestLeaseExpiryReasonWithoutNotes(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tk := createTask(t, s, "", "silent", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(ctx, tk.TaskID, "root"))
	must(s.Dispatch(ctx, tk.TaskID, "root", "root"))
	must(s.Accept(ctx, tk.TaskID, "root"))
	// Same terminal precondition as above: the bare-reason path only exists
	// past the persisted retry budget.
	for i := 0; i < maxPersistedRetries; i++ {
		must(s.RecordEvent(ctx, tk.TaskID, EvRetry, nil))
	}
	if _, err := s.db.Exec(`UPDATE tasks SET lease_expires_at=? WHERE task_id=?`,
		s.now()-1, tk.TaskID); err != nil {
		t.Fatalf("stamp lease: %v", err)
	}
	if _, err := s.ExpireTasks(ctx, "root"); err != nil {
		t.Fatalf("expire: %v", err)
	}
	got, err := s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ResultJSON != `{"failed":"lease expired"}` {
		t.Fatalf("result_json = %s, want the bare reason", got.ResultJSON)
	}
}

// TestLeaseExpiryReroutesRemoteCustody pins the HA settle for a dead remote
// executor: the delegator's running copy (owner=peer) must re-queue under a
// fresh attempt with the delegate aim cleared — the task re-routes to a live
// peer instead of dying, the zombie's late beats drop as stale, and the
// foreign-minted session handle it could never resume is discarded.
func TestLeaseExpiryReroutesRemoteCustody(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tk := createTask(t, s, "", "delegated", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(ctx, tk.TaskID, "root"))
	must(s.Dispatch(ctx, tk.TaskID, "root", "peer-b"))
	must(s.Accept(ctx, tk.TaskID, "peer-b")) // executor accepted → running, owner=peer
	must(s.SetAgentSession(ctx, tk.TaskID, "ses-remote", "peer-b", "opencode"))
	if _, err := s.db.Exec(`UPDATE tasks SET lease_expires_at=? WHERE task_id=?`,
		s.now()-1, tk.TaskID); err != nil {
		t.Fatalf("stamp lease: %v", err)
	}

	expired, err := s.ExpireTasks(ctx, "root")
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if len(expired) != 1 || !expired[0].Rerouted {
		t.Fatalf("expired = %+v, want 1 rerouted row", expired)
	}
	got, err := s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != StateQueued || got.OwnerNode != "root" {
		t.Fatalf("row = %s/%s, want queued under root custody", got.State, got.OwnerNode)
	}
	if got.AttemptID == tk.AttemptID {
		t.Fatalf("attempt not rotated — a partitioned executor's late result would pass")
	}
	if !got.Scheduled {
		t.Fatalf("re-queued row lacks scheduled=1 — invisible to the queue scheduler")
	}
	if got.AgentSessionID != "" {
		t.Fatalf("foreign session handle survived re-route: %s", got.AgentSessionID)
	}
	// The delegate aim is cleared — a late beat/result from the dead executor
	// authenticates against neither the new attempt nor the cleared target.
	if target, err := s.DispatchTarget(ctx, tk.TaskID); err != nil || target != "" {
		t.Fatalf("dispatch target = %q err=%v, want cleared", target, err)
	}
}

// TestLeaseExpirySelfRunKeepsSession is the local mirror: when THIS node's
// own supervised run expires, the re-queued retry keeps the self-minted
// session handle so the next run resumes the checkpoint instead of starting
// over.
func TestLeaseExpirySelfRunKeepsSession(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tk := createTask(t, s, "", "selfrun", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(ctx, tk.TaskID, "root"))
	must(s.Dispatch(ctx, tk.TaskID, "root", "root"))
	must(s.Accept(ctx, tk.TaskID, "root"))
	must(s.SetAgentSession(ctx, tk.TaskID, "ses-local", "root", "claude"))
	if _, err := s.db.Exec(`UPDATE tasks SET lease_expires_at=? WHERE task_id=?`,
		s.now()-1, tk.TaskID); err != nil {
		t.Fatalf("stamp lease: %v", err)
	}

	expired, err := s.ExpireTasks(ctx, "root")
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if len(expired) != 1 || !expired[0].Rerouted {
		t.Fatalf("expired = %+v, want 1 rerouted row", expired)
	}
	got, _ := s.Get(ctx, tk.TaskID)
	if got.AgentSessionID != "ses-local" {
		t.Fatalf("self-minted session lost on re-route: %q", got.AgentSessionID)
	}
}

// TestLeaseExpirySettleSkipsConcurrentTerminal proves the settle's CAS guard:
// a row that completed between scan and settle must not be re-queued or
// failed — the real result won the race.
func TestLeaseExpirySettleSkipsConcurrentTerminal(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tk := createTask(t, s, "", "racer", "root")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(ctx, tk.TaskID, "root"))
	must(s.Dispatch(ctx, tk.TaskID, "root", "root"))
	must(s.Accept(ctx, tk.TaskID, "root"))
	if _, err := s.db.Exec(`UPDATE tasks SET lease_expires_at=? WHERE task_id=?`,
		s.now()-1, tk.TaskID); err != nil {
		t.Fatalf("stamp lease: %v", err)
	}
	rows, err := s.ScanExpired(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("scan = %+v err=%v", rows, err)
	}
	must(s.Complete(ctx, tk.TaskID, "root", map[string]any{"done": "won"}))
	outcome, err := s.SettleExpired(ctx, rows[0], "root")
	if err != nil || outcome != ExpirySkipped {
		t.Fatalf("settle = %s err=%v, want skipped", outcome, err)
	}
	got, _ := s.Get(ctx, tk.TaskID)
	if got.State != StateDone {
		t.Fatalf("concurrent result overwritten: state=%s", got.State)
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

// TestLeaseExpiryDeadlineStaysTerminal proves the deadline branch ignores the
// retry budget: a task whose absolute lifetime ran out is never re-queued —
// re-routing would extend a lifetime the deadline exists to bound. Queued
// rows mark 'expired'; a running row force-fails so its monitor kills the
// subprocess.
func TestLeaseExpiryDeadlineStaysTerminal(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	past := s.now() - 1

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	queued := createTask(t, s, "", "queued-dl", "root")
	must(s.Queue(ctx, queued.TaskID, "root"))
	if _, err := s.db.Exec(`UPDATE tasks SET deadline_unix=? WHERE task_id=?`, past, queued.TaskID); err != nil {
		t.Fatalf("stamp deadline: %v", err)
	}

	running := createTask(t, s, "", "running-dl", "root")
	must(s.Queue(ctx, running.TaskID, "root"))
	must(s.Dispatch(ctx, running.TaskID, "root", "root"))
	must(s.Accept(ctx, running.TaskID, "root"))
	if _, err := s.db.Exec(`UPDATE tasks SET deadline_unix=? WHERE task_id=?`, past, running.TaskID); err != nil {
		t.Fatalf("stamp deadline: %v", err)
	}

	rows, err := s.ScanExpired(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("scan = %+v err=%v, want both deadline rows", rows, err)
	}
	for _, r := range rows {
		if !r.Deadline {
			t.Fatalf("row %+v not flagged deadline", r)
		}
		outcome, err := s.SettleExpired(ctx, r, "root")
		if err != nil || outcome != ExpiryFailed {
			t.Fatalf("settle %s = %s err=%v, want terminal", r.TaskID, outcome, err)
		}
	}
	gq, _ := s.Get(ctx, queued.TaskID)
	if gq.State != StateExpired {
		t.Fatalf("queued deadline row = %s, want expired", gq.State)
	}
	gr, _ := s.Get(ctx, running.TaskID)
	if gr.State != StateFailed {
		t.Fatalf("running deadline row = %s, want failed", gr.State)
	}
}

// TestDeadlineWinsOverExpiredLease covers the double-armed row: a task whose
// lease AND deadline both lapsed must settle as a deadline expiry — never as
// a re-route — even when the lease entry reaches SettleExpired first (the
// scan's deadline-first ordering is belt; the settle-time recheck is the
// suspenders that also catches a deadline stamped between scan and settle).
func TestDeadlineWinsOverExpiredLease(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tk := createTask(t, s, "", "double-expired", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(ctx, tk.TaskID, "root"))
	must(s.Dispatch(ctx, tk.TaskID, "root", "root"))
	must(s.Accept(ctx, tk.TaskID, "root"))
	// Lease expired AND deadline past — the lease entry alone reaches settle.
	if _, err := s.db.Exec(
		`UPDATE tasks SET lease_expires_at=?, deadline_unix=? WHERE task_id=?`,
		s.now()-1, s.now()-1, tk.TaskID); err != nil {
		t.Fatalf("stamp both: %v", err)
	}

	// Simulate the lease arm winning the race: settle a lease-only row view.
	outcome, err := s.SettleExpired(ctx, ExpiredRow{
		TaskID: tk.TaskID, State: StateRunning, Owner: "root", Deadline: false,
	}, "root")
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if outcome != ExpiryFailed {
		t.Fatalf("outcome = %s, want failed — a lease-armed settle on a deadline-expired row must still be terminal", outcome)
	}
	got, _ := s.Get(ctx, tk.TaskID)
	if got.State != StateFailed {
		t.Fatalf("state = %s, want failed — the deadline must not be re-queued past", got.State)
	}
	if got.AttemptID != tk.AttemptID {
		t.Fatalf("attempt rotated on a terminal deadline settle: %s", got.AttemptID)
	}
}

// TestLeaseExpiryRecordsEvictedExecutor pins the re-route exclusion source:
// settling a remote-custody expiry must stamp the dead executor on the retry
// event so the re-router never hands the task straight back to a node that
// just went silent on it.
func TestLeaseExpiryRecordsEvictedExecutor(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tk := createTask(t, s, "", "evict", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(ctx, tk.TaskID, "root"))
	must(s.Dispatch(ctx, tk.TaskID, "root", "peer-dead"))
	must(s.Accept(ctx, tk.TaskID, "peer-dead"))
	if _, err := s.db.Exec(`UPDATE tasks SET lease_expires_at=? WHERE task_id=?`,
		s.now()-1, tk.TaskID); err != nil {
		t.Fatalf("stamp lease: %v", err)
	}

	if _, err := s.ExpireTasks(ctx, "root"); err != nil {
		t.Fatalf("expire: %v", err)
	}
	evicted, err := s.EvictedExecutors(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("evicted executors: %v", err)
	}
	if len(evicted) != 1 || evicted[0] != "peer-dead" {
		t.Fatalf("evicted = %v, want [peer-dead]", evicted)
	}

	// A task that never left this node (dispatched but never accepted —
	// owner stays self) evicts the dispatch TARGET, not ourselves.
	pre := createTask(t, s, "", "pre-accept", "root")
	must(s.Queue(ctx, pre.TaskID, "root"))
	must(s.Dispatch(ctx, pre.TaskID, "root", "peer-ghost"))
	if _, err := s.db.Exec(`UPDATE tasks SET lease_expires_at=? WHERE task_id=?`,
		s.now()-1, pre.TaskID); err != nil {
		t.Fatalf("stamp lease: %v", err)
	}
	if _, err := s.ExpireTasks(ctx, "root"); err != nil {
		t.Fatalf("expire pre-accept: %v", err)
	}
	evicted, err = s.EvictedExecutors(ctx, pre.TaskID)
	if err != nil {
		t.Fatalf("evicted executors: %v", err)
	}
	if len(evicted) != 1 || evicted[0] != "peer-ghost" {
		t.Fatalf("evicted = %v, want [peer-ghost] — a silent target must be excluded, not self", evicted)
	}
}

// TestDeleteTerminal proves the handleDelegate stale-copy replacement guard:
// only a closed row is deletable (its event history is kept), while an active
// or review-parked copy always wins over an incoming attempt.
func TestDeleteTerminal(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	done := createTask(t, s, "", "closed", "root")
	must(s.Queue(ctx, done.TaskID, "root"))
	must(s.Dispatch(ctx, done.TaskID, "root", "root"))
	must(s.Accept(ctx, done.TaskID, "root"))
	must(s.Complete(ctx, done.TaskID, "root", map[string]any{"ok": true}))
	if err := s.DeleteTerminal(ctx, done.TaskID); err != nil {
		t.Fatalf("delete done row: %v", err)
	}
	if _, err := s.Get(ctx, done.TaskID); err == nil {
		t.Fatalf("done row still readable after DeleteTerminal")
	}
	if evs, err := s.Events(ctx, done.TaskID); err != nil || len(evs) == 0 {
		t.Fatalf("event history lost with the row: n=%d err=%v", len(evs), err)
	}

	for _, st := range []struct {
		name  string
		setup func(t *testing.T, s *TaskStore, id string)
	}{
		{"queued", func(t *testing.T, s *TaskStore, id string) {
			must(s.Queue(ctx, id, "root"))
		}},
		{"running", func(t *testing.T, s *TaskStore, id string) {
			must(s.Queue(ctx, id, "root"))
			must(s.Dispatch(ctx, id, "root", "root"))
			must(s.Accept(ctx, id, "root"))
		}},
		{"review", func(t *testing.T, s *TaskStore, id string) {
			must(s.Queue(ctx, id, "root"))
			must(s.Dispatch(ctx, id, "root", "root"))
			must(s.Accept(ctx, id, "root"))
			must(s.ForceFail(ctx, id, "spend"))
			must(s.Review(ctx, id, "root", "parked"))
		}},
	} {
		tk := createTask(t, s, "", "live-"+st.name, "root")
		st.setup(t, s, tk.TaskID)
		if err := s.DeleteTerminal(ctx, tk.TaskID); err == nil {
			t.Fatalf("%s row deleted — a live copy must never lose to an incoming attempt", st.name)
		}
		if got, err := s.Get(ctx, tk.TaskID); err != nil || got.State == "" {
			t.Fatalf("%s row gone after refused delete", st.name)
		}
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

// TestTaskEventSigning (P2-9): a store with the node key signs every event;
// the rows verify under the recorded public key; unsigned legacy rows remain
// legal.
func TestTaskEventSigning(t *testing.T) {
	s := newTestStore(t)
	pub, priv, err := bus.GenerateNodeKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	s.SetEventSigner(pub, priv)
	ctx := context.Background()
	tk := createTask(t, s, "", "signed", "root")

	if err := s.Queue(ctx, tk.TaskID, "root"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := s.VerifyTaskEventChain(ctx, tk.TaskID); err != nil {
		t.Fatalf("verify signed chain: %v", err)
	}
	events, err := s.Events(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	for _, e := range events {
		if e.Sig == "" || e.SigPub == "" {
			t.Fatalf("event %d unsigned on a keyed store", e.ID)
		}
		if e.SigPub != hex.EncodeToString(pub) {
			t.Fatalf("event %d sig_pub = %s, want this node's key", e.ID, e.SigPub)
		}
	}
}

// TestTaskEventSigForgeryDetect (P2-9): an attacker who rewrites a payload AND
// repairs the chain — re-hashing forward links — still fails, because the
// forged row's signature no longer verifies under the node's key.
func TestTaskEventSigForgeryDetect(t *testing.T) {
	s := newTestStore(t)
	pub, priv, err := bus.GenerateNodeKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	s.SetEventSigner(pub, priv)
	ctx := context.Background()
	tk := createTask(t, s, "", "forge", "root")
	if err := s.Queue(ctx, tk.TaskID, "root"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := s.Dispatch(ctx, tk.TaskID, "root", "win"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// Rewrite event 1's payload and repair event 2's prev_hash — the
	// classic defeat of the unkeyed chain. Event 1's own sig now lies.
	events, err := s.Events(ctx, tk.TaskID)
	if err != nil || len(events) < 2 {
		t.Fatalf("events: %v n=%d", err, len(events))
	}
	forged := `{"tampered":true}`
	newHash1 := hashEvent(events[0].PrevHash, tk.TaskID, events[0].TS, events[0].Type, forged)
	if _, err := s.db.ExecContext(ctx,
		`UPDATE task_events SET data_json=? WHERE id=?`, forged, events[0].ID); err != nil {
		t.Fatalf("tamper payload: %v", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE task_events SET prev_hash=? WHERE id=?`, newHash1, events[1].ID); err != nil {
		t.Fatalf("repair chain: %v", err)
	}

	if err := s.VerifyTaskEventChain(ctx, tk.TaskID); err == nil {
		t.Fatal("repaired forgery passed verification — signature check missing")
	}
}

// TestTaskEventChainMixedSigned: legacy unsigned rows next to signed rows
// still verify — the signature is attestation, not a schema gate.
func TestTaskEventChainMixedSigned(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := createTask(t, s, "", "mixed", "root") // unsigned: no key yet
	if err := s.Queue(ctx, tk.TaskID, "root"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	pub, priv, err := bus.GenerateNodeKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	s.SetEventSigner(pub, priv)
	if err := s.Dispatch(ctx, tk.TaskID, "root", "win"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := s.VerifyTaskEventChain(ctx, tk.TaskID); err != nil {
		t.Fatalf("mixed chain verify: %v", err)
	}
}

// TestTaskEventForeignReSignRejected (P2-9 hardening): a DB writer who
// rewrites the chain AND re-signs every row with a keypair of their own
// still fails verification, because the store knows this node's key and the
// signatures must come from it.
func TestTaskEventForeignReSignRejected(t *testing.T) {
	s := newTestStore(t)
	pub, priv, err := bus.GenerateNodeKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	s.SetEventSigner(pub, priv)
	ctx := context.Background()
	tk := createTask(t, s, "", "resign", "root")
	if err := s.Queue(ctx, tk.TaskID, "root"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := s.Dispatch(ctx, tk.TaskID, "root", "win"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// Attacker keypair: rewrite payloads and re-sign the whole chain under it.
	attackerPub, attackerPriv, err := bus.GenerateNodeKey()
	if err != nil {
		t.Fatalf("attacker key: %v", err)
	}
	events, err := s.Events(ctx, tk.TaskID)
	if err != nil || len(events) < 2 {
		t.Fatalf("events: %v n=%d", err, len(events))
	}
	prevHash := ""
	for i, e := range events {
		forged := `{"forged":true}`
		h := hashEvent(prevHash, e.TaskID, e.TS, e.Type, forged)
		sig := hex.EncodeToString(ed25519.Sign(attackerPriv, []byte(h)))
		if _, err := s.db.ExecContext(ctx,
			`UPDATE task_events SET data_json=?, prev_hash=?, sig=?, sig_pub=? WHERE id=?`,
			forged, prevHash, sig, hex.EncodeToString(attackerPub), e.ID); err != nil {
			t.Fatalf("re-sign event %d: %v", i, err)
		}
		prevHash = h
	}

	if err := s.VerifyTaskEventChain(ctx, tk.TaskID); err == nil {
		t.Fatal("a chain re-signed under a foreign key passed verification")
	}
}

// TestTaskEventSignatureStrippingRejected (P2-9 hardening): deleting the
// signature columns from the tail of a signed chain is detected — the
// unsigned prefix may only precede the first signed row.
func TestTaskEventSignatureStrippingRejected(t *testing.T) {
	s := newTestStore(t)
	pub, priv, err := bus.GenerateNodeKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	s.SetEventSigner(pub, priv)
	ctx := context.Background()
	tk := createTask(t, s, "", "strip", "root")
	if err := s.Queue(ctx, tk.TaskID, "root"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := s.Dispatch(ctx, tk.TaskID, "root", "win"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	events, err := s.Events(ctx, tk.TaskID)
	if err != nil || len(events) < 2 {
		t.Fatalf("events: %v n=%d", err, len(events))
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE task_events SET sig='', sig_pub='' WHERE id=?`, events[len(events)-1].ID); err != nil {
		t.Fatalf("strip tail signature: %v", err)
	}

	if err := s.VerifyTaskEventChain(ctx, tk.TaskID); err == nil {
		t.Fatal("a stripped tail signature passed verification")
	}
}

// TestTerminalSessionTasksWithoutEvent pins the finalizer's candidate probe:
// exactly the terminal, session-linked tasks that still lack the marker
// event, and nothing else.
func TestTerminalSessionTasksWithoutEvent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	finish := func(tk Task) {
		t.Helper()
		must(s.Queue(ctx, tk.TaskID, "root"))
		must(s.Dispatch(ctx, tk.TaskID, "root", "win"))
		must(s.Accept(ctx, tk.TaskID, "win"))
		must(s.Complete(ctx, tk.TaskID, "win", map[string]any{"ok": true}))
	}

	// Terminal + session + no marker: the one row the sweep must find.
	wanted := createTask(t, s, "", "summarize me", "root")
	must(s.SetSessionID(ctx, wanted.TaskID, "sess-1"))
	finish(wanted)

	// Marker already recorded: not a candidate.
	summarized := createTask(t, s, "", "already summarized", "root")
	must(s.SetSessionID(ctx, summarized.TaskID, "sess-1"))
	finish(summarized)
	must(s.RecordEvent(ctx, summarized.TaskID, "session_summary", map[string]string{"session_id": "sess-1"}))

	// Terminal but session-less: not a candidate.
	sessionless := createTask(t, s, "", "no session", "root")
	finish(sessionless)

	// Session-linked but still running: not a candidate.
	running := createTask(t, s, "", "still running", "root")
	must(s.SetSessionID(ctx, running.TaskID, "sess-2"))
	must(s.Queue(ctx, running.TaskID, "root"))

	got, err := s.TerminalSessionTasksWithoutEvent(ctx, "session_summary")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(got) != 1 || got[0].TaskID != wanted.TaskID {
		t.Fatalf("candidates = %+v, want exactly %s", got, wanted.TaskID)
	}
	if got[0].SessionID != "sess-1" {
		t.Fatalf("candidate session = %q, want sess-1", got[0].SessionID)
	}
}

// TestTaskStoreDelete tests single task and subtree deletion:
// - deleting queued or finished tasks succeeds and cascades to children
// - deleting active tasks fails with ErrTaskActive
func TestTaskStoreDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	parent := createTask(t, s, "", "parent task", "root")
	child := createTask(t, s, parent.TaskID, "child task", "root")

	if err := s.Queue(ctx, parent.TaskID, "root"); err != nil {
		t.Fatalf("queue parent: %v", err)
	}
	if err := s.Queue(ctx, child.TaskID, "root"); err != nil {
		t.Fatalf("queue child: %v", err)
	}
	if err := s.Dispatch(ctx, child.TaskID, "root", "worker"); err != nil {
		t.Fatalf("dispatch child: %v", err)
	}
	if err := s.Accept(ctx, child.TaskID, "worker"); err != nil {
		t.Fatalf("accept child: %v", err)
	}
	if err := s.Complete(ctx, child.TaskID, "worker", nil); err != nil {
		t.Fatalf("complete child: %v", err)
	}

	// Active check: dispatch parent so it becomes active
	if err := s.Dispatch(ctx, parent.TaskID, "root", "worker"); err != nil {
		t.Fatalf("dispatch parent: %v", err)
	}
	_, err := s.Delete(ctx, parent.TaskID)
	if !errors.Is(err, ErrTaskActive) {
		t.Fatalf("expected ErrTaskActive when deleting active task, got %v", err)
	}

	// Cancel parent so it is terminal
	if err := s.Cancel(ctx, parent.TaskID); err != nil {
		t.Fatalf("cancel parent: %v", err)
	}

	// Now delete parent, which should delete parent + child
	n, err := s.Delete(ctx, parent.TaskID)
	if err != nil {
		t.Fatalf("delete parent: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 tasks deleted, got %d", n)
	}

	// Verify both are gone
	if _, err := s.Get(ctx, parent.TaskID); err == nil {
		t.Fatalf("parent task still exists")
	}
	if _, err := s.Get(ctx, child.TaskID); err == nil {
		t.Fatalf("child task still exists")
	}
}

// TestTaskStoreClearQueue tests wiping the whole queue:
// active tasks are cancelled, and all tasks are removed.
func TestTaskStoreClearQueue(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	t1 := createTask(t, s, "", "task 1 queued", "root")
	t2 := createTask(t, s, "", "task 2 running", "root")
	t3 := createTask(t, s, "", "task 3 done", "root")

	if err := s.Queue(ctx, t1.TaskID, "root"); err != nil {
		t.Fatalf("queue t1: %v", err)
	}

	if err := s.Queue(ctx, t2.TaskID, "root"); err != nil {
		t.Fatalf("queue t2: %v", err)
	}
	if err := s.Dispatch(ctx, t2.TaskID, "root", "worker"); err != nil {
		t.Fatalf("dispatch t2: %v", err)
	}

	if err := s.Queue(ctx, t3.TaskID, "root"); err != nil {
		t.Fatalf("queue t3: %v", err)
	}
	if err := s.Dispatch(ctx, t3.TaskID, "root", "worker"); err != nil {
		t.Fatalf("dispatch t3: %v", err)
	}
	if err := s.Accept(ctx, t3.TaskID, "worker"); err != nil {
		t.Fatalf("accept t3: %v", err)
	}
	if err := s.Complete(ctx, t3.TaskID, "worker", nil); err != nil {
		t.Fatalf("complete t3: %v", err)
	}

	cancelled, deleted, err := s.ClearQueue(ctx)
	if err != nil {
		t.Fatalf("ClearQueue: %v", err)
	}
	if cancelled != 2 {
		t.Fatalf("expected 2 tasks cancelled, got %d", cancelled)
	}
	if deleted != 3 {
		t.Fatalf("expected 3 tasks deleted, got %d", deleted)
	}

	remaining, err := s.ListByState(ctx, "")
	if err != nil {
		t.Fatalf("ListByState: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected queue to be empty, found %d tasks", len(remaining))
	}
}

func TestCountScheduledActiveLocalOnly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Task 1: scheduled, dispatched to remote peer "worker"
	t1 := createTask(t, s, "", "task 1", "root")
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET scheduled=1 WHERE task_id=?`, t1.TaskID); err != nil {
		t.Fatalf("set scheduled: %v", err)
	}
	if err := s.Queue(ctx, t1.TaskID, "root"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := s.Dispatch(ctx, t1.TaskID, "root", "worker"); err != nil {
		t.Fatalf("dispatch to worker: %v", err)
	}

	// Task 2: scheduled, running locally on "root"
	t2 := createTask(t, s, "", "task 2", "root")
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET scheduled=1 WHERE task_id=?`, t2.TaskID); err != nil {
		t.Fatalf("set scheduled: %v", err)
	}
	if err := s.Queue(ctx, t2.TaskID, "root"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := s.Dispatch(ctx, t2.TaskID, "root", "root"); err != nil {
		t.Fatalf("dispatch to root: %v", err)
	}
	if err := s.Accept(ctx, t2.TaskID, "root"); err != nil {
		t.Fatalf("accept: %v", err)
	}

	// For root: only task 2 is counted (task 1 is dispatched to worker).
	cntRoot, err := s.CountScheduledActive(ctx, "root")
	if err != nil {
		t.Fatalf("count root: %v", err)
	}
	if cntRoot != 1 {
		t.Fatalf("count root = %d, want 1", cntRoot)
	}

	// When task 1 is retargeted back to root (e.g. after sendTo failure),
	// root counts both tasks (dispatched-to-self + running).
	if err := s.RetargetDelegation(ctx, t1.TaskID, "root"); err != nil {
		t.Fatalf("retarget to root: %v", err)
	}
	cntRootRetargeted, err := s.CountScheduledActive(ctx, "root")
	if err != nil {
		t.Fatalf("count root after retarget: %v", err)
	}
	if cntRootRetargeted != 2 {
		t.Fatalf("count root after retarget = %d, want 2", cntRootRetargeted)
	}
}

// CountScheduledActive must reflect where execution actually happens, not
// which row carries scheduled=1: a queue task accepted by a peer runs THERE
// (owner_node=<peer>) and frees this node's slot, while a delegated task
// running here (scheduled=0, owner=self) burns one the queue must see.
func TestCountScheduledActiveTracksOwner(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Forwarded to a peer and accepted there: running with owner=worker —
	// occupies the peer's budget, not root's.
	fwd := createTask(t, s, "", "forwarded to peer", "root")
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET scheduled=1 WHERE task_id=?`, fwd.TaskID); err != nil {
		t.Fatalf("set scheduled: %v", err)
	}
	if err := s.Queue(ctx, fwd.TaskID, "root"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := s.Dispatch(ctx, fwd.TaskID, "root", "worker"); err != nil {
		t.Fatalf("dispatch to worker: %v", err)
	}
	if err := s.Accept(ctx, fwd.TaskID, "worker"); err != nil {
		t.Fatalf("peer accept: %v", err)
	}

	// Delegated in and accepted locally: running with owner=root but no
	// scheduled flag — the queue scheduler still has to count the slot.
	del := createTask(t, s, "", "delegated in", "root")
	if err := s.Queue(ctx, del.TaskID, "root"); err != nil {
		t.Fatalf("queue delegated: %v", err)
	}
	if err := s.Dispatch(ctx, del.TaskID, "root", "root"); err != nil {
		t.Fatalf("dispatch to root: %v", err)
	}
	if err := s.Accept(ctx, del.TaskID, "root"); err != nil {
		t.Fatalf("local accept: %v", err)
	}

	cnt, err := s.CountScheduledActive(ctx, "root")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("count = %d, want 1 (only the locally-executing delegated row)", cnt)
	}
}

func TestRecoverPreservesUnreleasedPlanStages(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Task 1: regular ask, submitted
	t1 := createTask(t, s, "", "regular submitted", "root")

	// Task 2: plan stage, unreleased (submitted)
	t2 := createTask(t, s, "", "plan stage unreleased", "root")
	if err := s.SetStage(ctx, t2.TaskID, "plan-1", "stage-2", nil); err != nil {
		t.Fatalf("set stage: %v", err)
	}

	// Task 3: plan stage, released and dispatched
	t3 := createTask(t, s, "", "plan stage dispatched", "root")
	if err := s.SetStage(ctx, t3.TaskID, "plan-1", "stage-1", nil); err != nil {
		t.Fatalf("set stage: %v", err)
	}
	if err := s.Queue(ctx, t3.TaskID, "root"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := s.Dispatch(ctx, t3.TaskID, "root", "root"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if _, err := s.Recover(ctx, "root"); err != nil {
		t.Fatalf("recover: %v", err)
	}

	g1, _ := s.Get(ctx, t1.TaskID)
	if g1.State != StateQueued {
		t.Fatalf("t1 state = %s, want %s", g1.State, StateQueued)
	}

	g2, _ := s.Get(ctx, t2.TaskID)
	if g2.State != StateSubmitted {
		t.Fatalf("t2 state = %s, want %s (unreleased plan stage must stay submitted)", g2.State, StateSubmitted)
	}

	g3, _ := s.Get(ctx, t3.TaskID)
	if g3.State != StateQueued {
		t.Fatalf("t3 state = %s, want %s (released plan stage should requeue)", g3.State, StateQueued)
	}
}

// TestRecoverResumesSessionedRuns pins the restart-resume contract: a running
// (or context-parked) task whose agent session was minted on THIS node goes
// back to queued so the retry resumes the live conversation — daemon restart
// must not burn the whole interrupted exploration. Runs without a consumable
// checkpoint (no session, or a session minted by a peer) still take the honest
// interrupted-failed verdict: a foreign session id is not resumable here.
func TestRecoverResumesSessionedRuns(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	mkRunning := func(title string) Task {
		tk := createTask(t, s, "", title, "node-a")
		if err := s.Queue(ctx, tk.TaskID, "node-a"); err != nil {
			t.Fatalf("queue %s: %v", title, err)
		}
		if err := s.Dispatch(ctx, tk.TaskID, "node-a", "node-a"); err != nil {
			t.Fatalf("dispatch %s: %v", title, err)
		}
		if err := s.Accept(ctx, tk.TaskID, "node-a"); err != nil {
			t.Fatalf("accept %s: %v", title, err)
		}
		return tk
	}

	// Local session — resumable.
	t1 := mkRunning("local session")
	if err := s.SetAgentSession(ctx, t1.TaskID, "ses-local", "node-a", "opencode.py"); err != nil {
		t.Fatalf("set session t1: %v", err)
	}
	// Legacy/unknown-node session — still resumes locally.
	t2 := mkRunning("legacy session")
	if err := s.SetAgentSessionID(ctx, t2.TaskID, "ses-legacy"); err != nil {
		t.Fatalf("set session t2: %v", err)
	}
	// Session minted by a peer — not consumable here, must fail.
	t3 := mkRunning("remote session")
	if err := s.SetAgentSession(ctx, t3.TaskID, "ses-remote", "node-b", "opencode.py"); err != nil {
		t.Fatalf("set session t3: %v", err)
	}
	// No session at all — nothing to resume, must fail.
	t4 := mkRunning("no session")

	failed, err := s.Recover(ctx, "node-a")
	_ = failed
	if err != nil {
		t.Fatalf("recover: %v", err)
	}

	for _, id := range []string{t1.TaskID, t2.TaskID} {
		g, _ := s.Get(ctx, id)
		if g.State != StateQueued {
			t.Fatalf("task %s state = %s, want queued (session resumable)", id, g.State)
		}
		if !g.Scheduled {
			t.Fatalf("task %s scheduled = false, want true (queued must be schedulable)", id)
		}
	}
	for _, id := range []string{t3.TaskID, t4.TaskID} {
		g, _ := s.Get(ctx, id)
		if g.State != StateFailed {
			t.Fatalf("task %s state = %s, want failed (no consumable checkpoint)", id, g.State)
		}
	}
}

// TestRecoverSparesPeerExecutedRows is the live restart regression: the
// delegator's copy of a task executing on a peer sits in running with
// owner_node=<peer> (handleAccept stamped the executor). A restart of the
// delegator must not stamp "interrupted" onto work the peer is still doing —
// the observed failure killed a node2-pinned run mid-flight and reported a
// verdict the executor never gave.
func TestRecoverSparesPeerExecutedRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	remote := createTask(t, s, "", "delegated out", "node-a")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Queue(ctx, remote.TaskID, "node-a"))
	must(s.ClaimLocal(ctx, remote.TaskID, "node-a"))
	must(s.RetargetDelegation(ctx, remote.TaskID, "node-b"))
	// The peer's accept flips the origin row to running under the peer's
	// custody — the state signature of remote execution.
	must(s.Accept(ctx, remote.TaskID, "node-b"))

	// Self-owned waiting_context — no peer custody, no session — still takes
	// the interrupted verdict (control for the owner gate).
	local := createTask(t, s, "", "ctx wait here", "node-a")
	must(s.Queue(ctx, local.TaskID, "node-a"))
	must(s.Dispatch(ctx, local.TaskID, "node-a", "node-a"))
	must(s.SetWaitingContext(ctx, local.TaskID, "node-a"))

	if _, err := s.Recover(ctx, "node-a"); err != nil {
		t.Fatalf("recover: %v", err)
	}
	g, err := s.Get(ctx, remote.TaskID)
	if err != nil {
		t.Fatalf("get remote: %v", err)
	}
	if g.State == StateFailed || g.State == StateQueued {
		t.Fatalf("peer-executed task state = %s, want untouched (running)", g.State)
	}
	g, err = s.Get(ctx, local.TaskID)
	if err != nil {
		t.Fatalf("get local: %v", err)
	}
	if g.State != StateFailed {
		t.Fatalf("self-executed task state = %s, want failed (control)", g.State)
	}
}

func TestCancelCascadePlanSiblings(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s1 := createTask(t, s, "", "stage 1", "root")
	if err := s.SetStage(ctx, s1.TaskID, "plan-99", "st-1", nil); err != nil {
		t.Fatalf("set stage 1: %v", err)
	}
	if err := s.Queue(ctx, s1.TaskID, "root"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := s.Dispatch(ctx, s1.TaskID, "root", "root"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := s.Accept(ctx, s1.TaskID, "root"); err != nil {
		t.Fatalf("accept: %v", err)
	}

	s2 := createTask(t, s, "", "stage 2", "root")
	if err := s.SetStage(ctx, s2.TaskID, "plan-99", "st-2", []string{"st-1"}); err != nil {
		t.Fatalf("set stage 2: %v", err)
	}

	s3 := createTask(t, s, "", "stage 3", "root")
	if err := s.SetStage(ctx, s3.TaskID, "plan-99", "st-3", nil); err != nil {
		t.Fatalf("set stage 3: %v", err)
	}
	if err := s.Queue(ctx, s3.TaskID, "root"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := s.Dispatch(ctx, s3.TaskID, "root", "root"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := s.Accept(ctx, s3.TaskID, "root"); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := s.Complete(ctx, s3.TaskID, "root", nil); err != nil {
		t.Fatalf("complete: %v", err)
	}

	cancelled, err := s.CancelCascade(ctx, s1.TaskID)
	if err != nil {
		t.Fatalf("cancel cascade: %v", err)
	}

	// s1 and s2 should be cancelled; s3 was already complete, so not cancelled.
	if len(cancelled) != 2 {
		t.Fatalf("cancelled = %v, want 2 tasks", cancelled)
	}

	g1, _ := s.Get(ctx, s1.TaskID)
	g2, _ := s.Get(ctx, s2.TaskID)
	g3, _ := s.Get(ctx, s3.TaskID)

	if g1.State != StateCancelled || g2.State != StateCancelled {
		t.Fatalf("states: s1=%s, s2=%s; want both cancelled", g1.State, g2.State)
	}
	if g3.State != StateDone {
		t.Fatalf("state: s3=%s; want done", g3.State)
	}
}

// TestDeclineTrail pins the by+reason pairing enrichDeclineReason renders:
// a task refused twice must surface BOTH decliners with their stated reasons,
// not a bare "no capability matches".
func TestDeclineTrail(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := createTask(t, s, "", "trail", "root")
	must := func(err error) {
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(ctx, tk.TaskID, "root"))
	must(s.Dispatch(ctx, tk.TaskID, "root", "peer-a"))
	must(s.Decline(ctx, tk.TaskID, "root", "capacity full", "", "peer-a"))
	must(s.Dispatch(ctx, tk.TaskID, "root", "peer-b"))
	must(s.Decline(ctx, tk.TaskID, "root", "node draining", "drain", "peer-b"))

	trail, err := s.DeclineTrail(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("decline trail: %v", err)
	}
	if len(trail) != 2 {
		t.Fatalf("trail len = %d, want 2", len(trail))
	}
	if trail[0] != (DeclineRecord{By: "peer-a", Reason: "capacity full"}) ||
		trail[1] != (DeclineRecord{By: "peer-b", Reason: "node draining", Code: "drain"}) {
		t.Fatalf("trail = %+v", trail)
	}
	ids, err := s.DeclinedBy(ctx, tk.TaskID)
	if err != nil || len(ids) != 2 || ids[0] != "peer-a" || ids[1] != "peer-b" {
		t.Fatalf("DeclinedBy = %v, %v", ids, err)
	}
}

// TestRecoverRequeueSchedulableAndParked pins the restart fixes: a dispatched
// row coming back to queued must carry scheduled=1 or ListReady never sees it
// (the silent "went away but nobody did it" stall), while a row whose delivery
// is parked in task_outbox must stay dispatched — its custody is the parked
// bundle, not the queue.
func TestRecoverRequeueSchedulableAndParked(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// dispatched for LOCAL execution (the queue claim writes the self-target
	// delegate event), nothing parked: restart requeues it and arms the
	// scheduler.
	fly := createTask(t, s, "", "mid-flight", "root")
	if err := s.Queue(ctx, fly.TaskID, "root"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := s.ClaimLocal(ctx, fly.TaskID, "root"); err != nil {
		t.Fatalf("claim local: %v", err)
	}
	// Simulate the non-scheduled path: a direct submit carries scheduled=0.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET scheduled=0 WHERE task_id=?`, fly.TaskID); err != nil {
		t.Fatalf("unschedule: %v", err)
	}

	// dispatched to a PEER and sent (latest delegate target), accept not yet
	// arrived, nothing parked: the restart requeues it — forwardScheduled
	// resends and the peer dedups by task id; custody rows in the outbox are
	// the only ones that must not move.
	handed := createTask(t, s, "", "handed off", "root")
	if err := s.Queue(ctx, handed.TaskID, "root"); err != nil {
		t.Fatalf("queue handed: %v", err)
	}
	if err := s.ClaimLocal(ctx, handed.TaskID, "root"); err != nil {
		t.Fatalf("claim handed: %v", err)
	}
	if err := s.RetargetDelegation(ctx, handed.TaskID, "peer-x"); err != nil {
		t.Fatalf("retarget handed: %v", err)
	}

	// dispatched AND parked in the outbox: custody is the bundle, not the row.
	parked := createTask(t, s, "", "parked pin", "root")
	if err := s.Queue(ctx, parked.TaskID, "root"); err != nil {
		t.Fatalf("queue parked: %v", err)
	}
	if err := s.Dispatch(ctx, parked.TaskID, "root", "peer-dead"); err != nil {
		t.Fatalf("dispatch parked: %v", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO task_outbox (peer, task_id, payload_json, transport_type, ttl, created_at)
		 VALUES ('peer-dead', ?, '{}', 'pin', 0, 1)`, parked.TaskID); err != nil {
		t.Fatalf("park outbox row: %v", err)
	}

	if _, err := s.Recover(ctx, "root"); err != nil {
		t.Fatalf("recover: %v", err)
	}

	g1, err := s.Get(ctx, fly.TaskID)
	if err != nil {
		t.Fatalf("get fly: %v", err)
	}
	if g1.State != StateQueued || !g1.Scheduled {
		t.Fatalf("recovered dispatch = state:%s scheduled:%v, want queued+scheduled", g1.State, g1.Scheduled)
	}

	gh, err := s.Get(ctx, handed.TaskID)
	if err != nil {
		t.Fatalf("get handed: %v", err)
	}
	if gh.State != StateQueued {
		t.Fatalf("handed-off task state = %s, want %s (requeue re-forwards; the peer dedups the resend)", gh.State, StateQueued)
	}

	g2, err := s.Get(ctx, parked.TaskID)
	if err != nil {
		t.Fatalf("get parked: %v", err)
	}
	if g2.State != StateDispatched {
		t.Fatalf("parked task state = %s, want %s (outbox custody survives restart)", g2.State, StateDispatched)
	}
}

// TestRequeueStaleSubmitted covers the mid-life backstop for Recover's
// submitted branch: a row whose submitter died between create and dispatch
// (scheduled=0, no plan, no route event) would sit invisible to ListReady
// until the next restart. The sweep requeues only rows untouched past the
// cutoff — a submit still being routed was written seconds ago, and an
// unreleased plan stage must stay held back for its dependency graph.
func TestRequeueStaleSubmitted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	stale := createTask(t, s, "", "stranded ask", "owner")
	fresh := createTask(t, s, "", "live submit", "owner")
	remote := createTask(t, s, "", "inbound delegate", "owner")
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET remote=1 WHERE task_id=?`, remote.TaskID); err != nil {
		t.Fatalf("mark remote: %v", err)
	}
	stage := createTask(t, s, "", "held stage", "owner")
	if err := s.SetStage(ctx, stage.TaskID, "plan-stale-1", "st-1", []string{"st-0"}); err != nil {
		t.Fatalf("set stage: %v", err)
	}
	old := time.Now().Add(-10 * time.Minute).Unix()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET updated_at=? WHERE task_id IN (?,?,?)`, old, stale.TaskID, remote.TaskID, stage.TaskID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	cutoff := time.Now().Add(-2 * time.Minute).Unix()
	n, err := s.RequeueStaleSubmitted(ctx, cutoff)
	if err != nil {
		t.Fatalf("requeue stale: %v", err)
	}
	if n != 1 {
		t.Fatalf("requeued %d rows, want 1 (only the stale non-plan submit)", n)
	}
	g, err := s.Get(ctx, stale.TaskID)
	if err != nil {
		t.Fatalf("get stale: %v", err)
	}
	if g.State != StateQueued || !g.Scheduled {
		t.Fatalf("stale submit = state:%s scheduled:%v, want queued+scheduled", g.State, g.Scheduled)
	}
	g, err = s.Get(ctx, fresh.TaskID)
	if err != nil {
		t.Fatalf("get fresh: %v", err)
	}
	if g.State != StateSubmitted || g.Scheduled {
		t.Fatalf("fresh submit = state:%s scheduled:%v, want untouched submitted", g.State, g.Scheduled)
	}
	g, err = s.Get(ctx, remote.TaskID)
	if err != nil {
		t.Fatalf("get remote: %v", err)
	}
	if g.State != StateSubmitted {
		t.Fatalf("remote submit = state:%s, want untouched (it is cancelled, never requeued)", g.State)
	}
	if n, err := s.CancelStaleRemoteSubmits(ctx, cutoff); err != nil {
		t.Fatalf("cancel remote: %v", err)
	} else if n != 1 {
		t.Fatalf("cancelled %d remote submits, want 1", n)
	}
	g, err = s.Get(ctx, remote.TaskID)
	if err != nil {
		t.Fatalf("get remote post-cancel: %v", err)
	}
	if g.State != StateCancelled {
		t.Fatalf("remote submit post-sweep = state:%s, want cancelled", g.State)
	}
	g, err = s.Get(ctx, stage.TaskID)
	if err != nil {
		t.Fatalf("get stage: %v", err)
	}
	if g.State != StateSubmitted {
		t.Fatalf("plan stage = state:%s, want submitted (dependency graph holds it)", g.State)
	}
}

// TestRequeueStaleClaims covers the claim-orphan class: a queue scheduler
// claimed the row (dispatched, owner=self) and its process died before the
// run's first lease renewal — ExpireTasks never sees it (lease NULL) and
// ListReady never sees it (not queued). The sweep requeues only self-owned,
// lease-free, outbox-free rows past the cutoff; a live claim or a parked
// forward is left alone.
func TestRequeueStaleClaims(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	orphan := createTask(t, s, "", "dead claim", "owner")
	leased := createTask(t, s, "", "live claim", "owner")
	parked := createTask(t, s, "", "parked forward", "owner")
	inflight := createTask(t, s, "", "DTN in flight", "owner")
	for _, tk := range []Task{orphan, leased, parked, inflight} {
		if err := s.Queue(ctx, tk.TaskID, "owner"); err != nil {
			t.Fatalf("queue %s: %v", tk.Title, err)
		}
		if err := s.ClaimLocal(ctx, tk.TaskID, "self-node"); err != nil {
			t.Fatalf("claim %s: %v", tk.Title, err)
		}
	}
	// leased is still dispatched but its executor stamped a lease — the
	// monitor's own path will reap it if the renewal stops.
	if err := s.SetLease(ctx, leased.TaskID, int64(time.Hour/time.Millisecond)); err != nil {
		t.Fatalf("lease: %v", err)
	}
	// parked holds custody in task_outbox — the flush owes its delivery.
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO task_outbox (peer, task_id, payload_json, payload_blob, transport_type, ttl, created_at)
		 VALUES ('peer-x', ?, '', '', '', 0, ?)`, parked.TaskID, time.Now().Unix()); err != nil {
		t.Fatalf("outbox: %v", err)
	}
	// inflight is a DTN delegation whose outbox row was already dropped by a
	// successful hop: it carries no lease (DTN is lease-exempt) and its only
	// marker is the absolute deadline the store-and-forward paths stamp.
	if err := s.SetDeadlineIfUnset(ctx, inflight.TaskID, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	old := time.Now().Add(-10 * time.Minute).Unix()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET updated_at=? WHERE task_id IN (?,?,?,?)`, old, orphan.TaskID, leased.TaskID, parked.TaskID, inflight.TaskID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	cutoff := time.Now().Add(-2 * time.Minute).Unix()
	n, err := s.RequeueStaleClaims(ctx, "self-node", cutoff)
	if err != nil {
		t.Fatalf("requeue claims: %v", err)
	}
	if n != 1 {
		t.Fatalf("requeued %d rows, want 1 (only the lease-free claim)", n)
	}
	g, err := s.Get(ctx, orphan.TaskID)
	if err != nil {
		t.Fatalf("get orphan: %v", err)
	}
	if g.State != StateQueued || !g.Scheduled {
		t.Fatalf("orphan = state:%s scheduled:%v, want queued+scheduled", g.State, g.Scheduled)
	}
	g, err = s.Get(ctx, leased.TaskID)
	if err != nil {
		t.Fatalf("get leased: %v", err)
	}
	if g.State != StateDispatched {
		t.Fatalf("leased claim = %s, want dispatched (the lease owns its timeout)", g.State)
	}
	g, err = s.Get(ctx, parked.TaskID)
	if err != nil {
		t.Fatalf("get parked: %v", err)
	}
	if g.State != StateDispatched {
		t.Fatalf("parked forward = %s, want dispatched (outbox custody)", g.State)
	}
	g, err = s.Get(ctx, inflight.TaskID)
	if err != nil {
		t.Fatalf("get inflight: %v", err)
	}
	if g.State != StateDispatched {
		t.Fatalf("DTN in flight = %s, want dispatched (the absolute deadline is its bound)", g.State)
	}
}

// TestFailStaleLeaseless covers the dead-supervisor class: a running row
// whose process died before renewLease's first stamp has lease_expires_at
// NULL — invisible to ExpireTasks (which requires a lease to expire). The
// sweep fails only self-owned, lease-free, deadline-free stale rows.
func TestFailStaleLeaseless(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	dead := createTask(t, s, "", "dead run", "owner")
	alive := createTask(t, s, "", "renewed run", "owner")
	dtn := createTask(t, s, "", "dtn run", "owner")
	for _, tk := range []Task{dead, alive, dtn} {
		if err := s.Queue(ctx, tk.TaskID, "owner"); err != nil {
			t.Fatalf("queue %s: %v", tk.Title, err)
		}
		if err := s.ClaimLocal(ctx, tk.TaskID, "self-node"); err != nil {
			t.Fatalf("claim %s: %v", tk.Title, err)
		}
		if err := s.Accept(ctx, tk.TaskID, "self-node"); err != nil {
			t.Fatalf("accept %s: %v", tk.Title, err)
		}
	}
	if err := s.SetLease(ctx, alive.TaskID, int64(time.Hour/time.Millisecond)); err != nil {
		t.Fatalf("lease alive: %v", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET deadline_unix=? WHERE task_id=?`, time.Now().Add(time.Hour).Unix(), dtn.TaskID); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	old := time.Now().Add(-10 * time.Minute).Unix()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET updated_at=? WHERE task_id IN (?,?,?)`, old, dead.TaskID, alive.TaskID, dtn.TaskID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	cutoff := time.Now().Add(-2 * time.Minute).Unix()
	n, err := s.FailStaleLeaseless(ctx, "self-node", cutoff)
	if err != nil {
		t.Fatalf("fail leaseless: %v", err)
	}
	if n != 1 {
		t.Fatalf("failed %d rows, want 1 (only the dead lease-free run)", n)
	}
	g, err := s.Get(ctx, dead.TaskID)
	if err != nil {
		t.Fatalf("get dead: %v", err)
	}
	if g.State != StateFailed {
		t.Fatalf("dead run = %s, want failed", g.State)
	}
	g, err = s.Get(ctx, alive.TaskID)
	if err != nil {
		t.Fatalf("get alive: %v", err)
	}
	if g.State != StateRunning {
		t.Fatalf("renewed run = %s, want running", g.State)
	}
	g, err = s.Get(ctx, dtn.TaskID)
	if err != nil {
		t.Fatalf("get dtn: %v", err)
	}
	if g.State != StateRunning {
		t.Fatalf("dtn run = %s, want running (deadline-bound, not lease-bound)", g.State)
	}
}

// TestProjectStateCounts covers the one-query work summary behind the console's
// project cards: counts grouped per project and state, unnamed tasks excluded.
func TestProjectStateCounts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	mk := func(project, title, state string) {
		tk, err := s.Create(ctx, "", project, title, "node", []string{"node"})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		if state != "" {
			if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET state = ? WHERE task_id = ?`, state, tk.TaskID); err != nil {
				t.Fatalf("set state %s: %v", state, err)
			}
		}
	}
	mk("alpha", "a1", StateRunning)
	mk("alpha", "a2", StateRunning)
	mk("alpha", "a3", StateReview)
	mk("beta", "b1", StateDone)
	mk("", "unprojected", StateRunning) // must not appear under any project

	counts, err := s.ProjectStateCounts(ctx)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if got := counts["alpha"][StateRunning]; got != 2 {
		t.Fatalf("alpha running = %d, want 2", got)
	}
	if got := counts["alpha"][StateReview]; got != 1 {
		t.Fatalf("alpha review = %d, want 1", got)
	}
	if got := counts["beta"][StateDone]; got != 1 {
		t.Fatalf("beta done = %d, want 1", got)
	}
	if _, ok := counts[""]; ok {
		t.Fatalf("unnamed bucket present: %v", counts[""])
	}
}

// parkForFollowup flips a fresh task into the exact row the supervised
// follow-up park leaves behind: review under accept_work with the
// needs_followup flag inside the parked result.
func parkForFollowup(t *testing.T, s *TaskStore, tk Task, owner string) {
	t.Helper()
	if _, err := s.db.ExecContext(context.Background(), `
		UPDATE tasks SET state=?, owner_node=?, approval_disposition=?, result_json=?
		WHERE task_id=?`,
		StateReview, owner, string(ApprovalAcceptWork),
		`{"ok":false,"needs_followup":true,"stderr":"round budget exhausted"}`,
		tk.TaskID); err != nil {
		t.Fatalf("park for followup: %v", err)
	}
}

// TestNeedsFollowup pins the marker's semantics: only a review row whose
// parked result carries needs_followup may be continued — every other
// review is answered by approve or reject alone.
func TestNeedsFollowup(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := createTask(t, s, "", "followup", "node")
	parkForFollowup(t, s, tk, "node")

	cur, err := s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !cur.NeedsFollowup() {
		t.Fatal("parked followup row must report NeedsFollowup")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET result_json=? WHERE task_id=?`,
		`{"ok":true}`, tk.TaskID); err != nil {
		t.Fatalf("clear flag: %v", err)
	}
	cur, _ = s.Get(ctx, tk.TaskID)
	if cur.NeedsFollowup() {
		t.Fatal("review without the flag must not read as follow-up")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET state=?, result_json=? WHERE task_id=?`,
		StateDone, `{"needs_followup":true}`, tk.TaskID); err != nil {
		t.Fatalf("terminalize: %v", err)
	}
	cur, _ = s.Get(ctx, tk.TaskID)
	if cur.NeedsFollowup() {
		t.Fatal("non-review row must not read as follow-up even with the flag")
	}
}

// TestRequeueForContinue pins the CAS: review+accept_work+expected owner
// flips to queued with scheduled=1 (ListReady's exact selector — without it
// the continuation would be invisible), and every other combination is a
// conflict, so two callers can never double-claim the park.
func TestRequeueForContinue(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := createTask(t, s, "", "continue me", "node")
	parkForFollowup(t, s, tk, "node")

	if err := s.RequeueForContinue(ctx, tk.TaskID, "other-node"); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign owner must conflict, got %v", err)
	}
	if err := s.RequeueForContinue(ctx, tk.TaskID, "node"); err != nil {
		t.Fatalf("requeue: %v", err)
	}
	cur, err := s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if cur.State != StateQueued {
		t.Fatalf("state = %s, want queued", cur.State)
	}
	var scheduled int
	if err := s.db.QueryRowContext(ctx, `SELECT scheduled FROM tasks WHERE task_id=?`, tk.TaskID).Scan(&scheduled); err != nil {
		t.Fatalf("read scheduled: %v", err)
	}
	if scheduled != 1 {
		t.Fatal("requeue must stamp scheduled=1 for ListReady")
	}
	if err := s.RequeueForContinue(ctx, tk.TaskID, "node"); !errors.Is(err, ErrConflict) {
		t.Fatalf("second continue must conflict, got %v", err)
	}
	// A non-accept_work review (consent park) is not a follow-up continue.
	tk2 := createTask(t, s, "", "consent park", "node")
	if _, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET state=?, owner_node=?, approval_disposition=? WHERE task_id=?`,
		StateReview, "node", string(ApprovalResumeExecution), tk2.TaskID); err != nil {
		t.Fatalf("park consent: %v", err)
	}
	if err := s.RequeueForContinue(ctx, tk2.TaskID, "node"); !errors.Is(err, ErrConflict) {
		t.Fatalf("consent disposition must conflict, got %v", err)
	}
}

// TestContinueReviewedRequeues covers the core path end to end at the store
// level: the continue validates the park, lands the operator note as a
// steer-grade board message (so the continued round leads with it), and
// hands the row back to the schedulable queue.
func TestContinueReviewedRequeues(t *testing.T) {
	s := newTestStore(t)
	c := &Core{store: s, nodeID: "node", logger: testLogger(),
		loop: defense.NewLoopDetector(2), checkpointLoop: defense.NewLoopDetector(2)}
	ctx := context.Background()
	tk := createTask(t, s, "", "long work", "node")
	parkForFollowup(t, s, tk, "node")

	// A review without the follow-up flag refuses.
	plain := createTask(t, s, "", "plain review", "node")
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET state=?, owner_node=? WHERE task_id=?`,
		StateReview, "node", plain.TaskID); err != nil {
		t.Fatalf("park plain: %v", err)
	}
	if _, err := c.ContinueReviewed(ctx, plain.TaskID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("non-followup review must conflict, got %v", err)
	}

	final, err := c.ContinueReviewed(ctx, tk.TaskID, "finish the second half")
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if final.State != StateQueued {
		t.Fatalf("state = %s, want queued", final.State)
	}
	msgs, err := s.TaskMessages(ctx, "task:"+tk.TaskID)
	if err != nil {
		t.Fatalf("board read: %v", err)
	}
	var steer *TaskMessage
	for i := range msgs {
		if isUserSender(msgs[i].Sender) && msgs[i].Grade == MsgGradeSteer {
			steer = &msgs[i]
		}
	}
	if steer == nil || !strings.Contains(steer.Body, "finish the second half") {
		t.Fatalf("note must land as a steer message, got %+v", msgs)
	}

	// Foreign dispatch target: the continuation belongs to the executor.
	remote := createTask(t, s, "", "remote-run", "node")
	parkForFollowup(t, s, remote, "node")
	if err := s.RecordEvent(ctx, remote.TaskID, EvDelegate, map[string]any{"target": "peer-1"}); err != nil {
		t.Fatalf("record delegate: %v", err)
	}
	if _, err := c.ContinueReviewed(ctx, remote.TaskID, ""); err == nil {
		t.Fatal("remote-dispatched task must refuse local continue")
	}
}

// TestAttemptScopedVerdicts pins the split-brain fence on runner verdict
// writes: a run whose attempt was rotated out from under it (lease-expiry
// reroute -> requeue -> fresh run) must not land its Fail/Complete on the
// replacement attempt's row — the Get-then-CAS pair otherwise reads the
// fresh attempt's running state and kills it.
func TestAttemptScopedVerdicts(t *testing.T) {
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
	oldAttempt := tk.AttemptID

	// The stale attempt's verdict conflicts — nothing is written.
	if err := s.FailAttempt(ctx, tk.TaskID, "root", "attempt-that-never-was", "zombie"); !errors.Is(err, ErrConflict) {
		t.Fatalf("superseded FailAttempt = %v, want ErrConflict", err)
	}
	if err := s.CompleteAttempt(ctx, tk.TaskID, "root", "attempt-that-never-was", map[string]any{"ok": true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("superseded CompleteAttempt = %v, want ErrConflict", err)
	}
	cur, err := s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if cur.State != StateRunning || cur.AttemptID != oldAttempt {
		t.Fatalf("superseded write touched the row: state=%s attempt=%s", cur.State, cur.AttemptID)
	}

	// Rotation to a fresh attempt, then the OLD runner's verdict must still
	// conflict even though the row is back in a valid source state.
	must(s.Fail(ctx, tk.TaskID, "root", "boom"))
	if _, err := s.RequeueForRetry(ctx, tk.TaskID, "root", false); err != nil {
		t.Fatalf("requeue: %v", err)
	}
	must(s.Accept(ctx, tk.TaskID, "root"))
	cur, err = s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if cur.AttemptID == oldAttempt {
		t.Fatal("attempt did not rotate")
	}
	if err := s.FailAttempt(ctx, tk.TaskID, "root", oldAttempt, "zombie verdict"); !errors.Is(err, ErrConflict) {
		t.Fatalf("old-attempt FailAttempt on fresh run = %v, want ErrConflict", err)
	}
	if err := s.CompleteAttempt(ctx, tk.TaskID, "root", oldAttempt, map[string]any{"ok": true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("old-attempt CompleteAttempt on fresh run = %v, want ErrConflict", err)
	}
	cur, err = s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if cur.State != StateRunning {
		t.Fatalf("superseded verdict killed the fresh run: state=%s", cur.State)
	}

	// And the current attempt's verdict lands normally — the fence is
	// supersession, not a freeze.
	if err := s.CompleteAttempt(ctx, tk.TaskID, "root", cur.AttemptID, map[string]any{"ok": true}); err != nil {
		t.Fatalf("current attempt CompleteAttempt: %v", err)
	}
	cur, err = s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if cur.State != StateDone {
		t.Fatalf("current attempt verdict did not land: state=%s", cur.State)
	}
}

// TestForceFailAttemptScope covers the dispensation-scoped management fail:
// a verdict minted against one attempt must not land on a row that rotated
// (expiry settle racing a fresh dispensation, a superseded orphan sweep),
// while the matching attempt and the legacy empty-attempt form still settle.
func TestForceFailAttemptScope(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	tk := createTask(t, s, "", "t", "root")
	if err := s.Queue(ctx, tk.TaskID, "root"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := s.Dispatch(ctx, tk.TaskID, "root", "root"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := s.Accept(ctx, tk.TaskID, "root"); err != nil {
		t.Fatalf("accept: %v", err)
	}

	// A stale scan's verdict is ignored — the row keeps its live attempt.
	if err := s.ForceFailAttempt(ctx, tk.TaskID, "attempt-that-never-was", "stale scan"); err != nil {
		t.Fatalf("mismatched ForceFailAttempt = %v, want harmless nil", err)
	}
	cur, err := s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if cur.State != StateRunning || cur.AttemptID != tk.AttemptID {
		t.Fatalf("stale verdict touched row: state=%s attempt=%s", cur.State, cur.AttemptID)
	}

	// The matching attempt's verdict lands.
	if err := s.ForceFailAttempt(ctx, tk.TaskID, tk.AttemptID, "lease expired"); err != nil {
		t.Fatalf("matching ForceFailAttempt: %v", err)
	}
	cur, err = s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if cur.State != StateFailed {
		t.Fatalf("matching verdict did not land: state=%s", cur.State)
	}

	// Terminal rows stay terminal regardless of the attempt argument.
	if err := s.ForceFailAttempt(ctx, tk.TaskID, tk.AttemptID, "second verdict"); err != nil {
		t.Fatalf("terminal ForceFailAttempt = %v, want harmless nil", err)
	}
	cur, err = s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if cur.State != StateFailed || cur.AttemptID != tk.AttemptID {
		t.Fatalf("terminal row mutated: state=%s attempt=%s", cur.State, cur.AttemptID)
	}

	// The empty attempt form degenerates to plain ForceFail for legacy rows.
	tk2 := createTask(t, s, "", "t2", "root")
	if err := s.Queue(ctx, tk2.TaskID, "root"); err != nil {
		t.Fatalf("queue2: %v", err)
	}
	if err := s.ForceFailAttempt(ctx, tk2.TaskID, "", "legacy sweep"); err != nil {
		t.Fatalf("empty-attempt ForceFailAttempt: %v", err)
	}
	cur, err = s.Get(ctx, tk2.TaskID)
	if err != nil {
		t.Fatalf("get2: %v", err)
	}
	if cur.State != StateFailed {
		t.Fatalf("empty-attempt verdict did not land: state=%s", cur.State)
	}
}

// TestSettleExpiredSkipsRotatedAttempt: the scan's lease verdict convicts only
// the scanned dispensation. If the row rotated to a fresh attempt between the
// scan and the settle — an upstream adopt, a concurrent re-dispatch — the new
// custody is alive and must not be re-queued or failed on the stale evidence;
// its own scan row will judge it. The absolute deadline is the one exception:
// it is re-evaluated on the fresh row and binds regardless of rotation.
func TestSettleExpiredSkipsRotatedAttempt(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tk := createTask(t, s, "", "t-rot", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(ctx, tk.TaskID, "root"))
	must(s.Dispatch(ctx, tk.TaskID, "root", "root"))
	must(s.Accept(ctx, tk.TaskID, "root"))
	if _, err := s.db.Exec(`UPDATE tasks SET lease_expires_at=? WHERE task_id=?`,
		s.now()-1, tk.TaskID); err != nil {
		t.Fatalf("stamp lease: %v", err)
	}
	rows, err := s.ScanExpired(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("scan = %+v err=%v", rows, err)
	}
	if rows[0].AttemptID != tk.AttemptID {
		t.Fatalf("scan attempt = %q, want %q", rows[0].AttemptID, tk.AttemptID)
	}

	// Rotate: a newer dispensation adopts the row before the settle runs.
	newAttempt := tk.AttemptID[:len(tk.AttemptID)-1] + "z"
	must(s.AdoptAttempt(ctx, tk.TaskID, tk.AttemptID, newAttempt))

	outcome, err := s.SettleExpired(ctx, rows[0], "root")
	if err != nil || outcome != ExpirySkipped {
		t.Fatalf("settle = %s err=%v, want skipped", outcome, err)
	}
	got, err := s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != StateRunning || got.AttemptID != newAttempt {
		t.Fatalf("stale lease verdict killed fresh custody: state=%s attempt=%s", got.State, got.AttemptID)
	}
}

// TestSettleExpiredDeadlineStillBindsRotated covers the exception: even a
// rotated row dies when its own stamped deadline has passed — the lifetime
// bound belongs to the task, not to any one dispensation.
func TestSettleExpiredDeadlineStillBindsRotated(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tk := createTask(t, s, "", "t-rot-dl", "root")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(s.Queue(ctx, tk.TaskID, "root"))
	must(s.Dispatch(ctx, tk.TaskID, "root", "root"))
	must(s.Accept(ctx, tk.TaskID, "root"))
	// The fresh row carries a past deadline (stamped between scan and settle).
	if _, err := s.db.Exec(`UPDATE tasks SET deadline_unix=? WHERE task_id=?`,
		s.now()-1, tk.TaskID); err != nil {
		t.Fatalf("stamp deadline: %v", err)
	}
	newAttempt := tk.AttemptID[:len(tk.AttemptID)-1] + "z"
	must(s.AdoptAttempt(ctx, tk.TaskID, tk.AttemptID, newAttempt))

	// A lease-expiry scan row for the OLD attempt arrives late.
	outcome, err := s.SettleExpired(ctx, ExpiredRow{
		TaskID: tk.TaskID, State: StateRunning, Owner: "root",
		AttemptID: tk.AttemptID, Deadline: false,
	}, "root")
	if err != nil || outcome != ExpiryFailed {
		t.Fatalf("settle = %s err=%v, want failed — the deadline binds the rotated row", outcome, err)
	}
	got, err := s.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != StateFailed {
		t.Fatalf("deadline-expired rotated row = %s, want failed", got.State)
	}
}

// TestListSurvivesNullColumns covers the row an older schema leaves behind.
// The baseline tasks table declares parent_id, project, title, chain_json,
// created_at and updated_at without NOT NULL, so a row written before a column
// existed carries NULL there. Scanning one of those into a plain value fails,
// and scanTasks aborts the whole list with it — one such row took down
// /api/tasks for every task. A NULL means "not recorded", which is exactly
// what the zero value says.
func TestListSurvivesNullColumns(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO tasks
		(task_id, state, owner_node, attempt_id, state_version)
		VALUES ('legacy-null', ?, 'root', 'a1', 0)`, StateQueued); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	tasks, err := s.ListActive(ctx)
	if err != nil {
		t.Fatalf("ListActive over a NULL-column row: %v", err)
	}
	var found bool
	for _, tk := range tasks {
		if tk.TaskID != "legacy-null" {
			continue
		}
		found = true
		if tk.ParentID != "" || tk.Project != "" || tk.Title != "" ||
			len(tk.Chain) != 0 || tk.CreatedAt != 0 || tk.UpdatedAt != 0 {
			t.Fatalf("NULL columns hydrated as non-zero: %+v", tk)
		}
	}
	if !found {
		t.Fatal("legacy row missing from the list")
	}
	if _, err := s.ListByState(ctx, StateQueued); err != nil {
		t.Fatalf("ListByState over a NULL-column row: %v", err)
	}
	if _, err := s.Get(ctx, "legacy-null"); err != nil {
		t.Fatalf("Get over a NULL-column row: %v", err)
	}
}

// TestListSkipsUndecodableRow pins the other half of the listing contract the
// board depends on: a row the driver genuinely cannot convert is skipped and
// counted, not returned as an error. NULL is a "not recorded" value now
// (TestListSurvivesNullColumns), so this needs a value no conversion accepts —
// a word where an integer column is scanned.
func TestListSkipsUndecodableRow(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	good := createTask(t, s, "", "readable row", "node")
	broken := createTask(t, s, "", "unconvertible row", "node")
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET state_version = 'not-a-number' WHERE task_id = ?`,
		broken.TaskID); err != nil {
		t.Fatalf("break row: %v", err)
	}

	got, err := s.ListByState(ctx, StateSubmitted)
	if err != nil {
		t.Fatalf("list with a damaged row must not fail: %v", err)
	}
	if len(got) != 1 || got[0].TaskID != good.TaskID {
		t.Fatalf("list = %d rows (%v), want just %s", len(got), got, good.TaskID)
	}
	if s.SkippedRows() == 0 {
		t.Fatal("skipped row was not counted")
	}
}

// A retried task owes the thread a second report.
//
// The marker that makes the finalizer exactly-once used to be task-scoped, so
// the report filed for a failed attempt silenced every later one: a task that
// failed, was retried, and then succeeded kept the stale "[failed] …" as the
// thread's last word on it. Scoping the marker to the attempt it reported
// fixes that without re-reporting rows nobody retried.
func TestTerminalSessionTasksWithoutEventReportsEachAttempt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	tk := createTask(t, s, "", "retried after a failure", "root")
	must(s.SetSessionID(ctx, tk.TaskID, "sess-1"))
	must(s.Queue(ctx, tk.TaskID, "root"))
	must(s.Dispatch(ctx, tk.TaskID, "root", "win"))
	must(s.Accept(ctx, tk.TaskID, "win"))
	must(s.ForceFail(ctx, tk.TaskID, "bash shell missing"))

	failed, err := s.Get(ctx, tk.TaskID)
	must(err)
	// The marker the finalizer writes carries the attempt it reported.
	must(s.RecordEvent(ctx, tk.TaskID, "session_summary", map[string]string{
		"session_id": "sess-1", "state": "failed", "attempt_id": failed.AttemptID,
	}))

	got, err := s.TerminalSessionTasksWithoutEvent(ctx, "session_summary")
	must(err)
	if len(got) != 0 {
		t.Fatalf("candidates after the first report = %d, want 0", len(got))
	}

	// The retry rotates the attempt id and the task runs again.
	if _, err := s.RequeueForRetry(ctx, tk.TaskID, "win", false); err != nil {
		t.Fatalf("requeue for retry: %v", err)
	}
	must(s.Accept(ctx, tk.TaskID, "win"))
	must(s.Complete(ctx, tk.TaskID, "win", map[string]any{"ok": true}))

	got, err = s.TerminalSessionTasksWithoutEvent(ctx, "session_summary")
	must(err)
	if len(got) != 1 || got[0].TaskID != tk.TaskID {
		t.Fatalf("candidates after the retry = %+v, want the task once", got)
	}
	if got[0].State != StateDone {
		t.Fatalf("candidate state = %q, want done", got[0].State)
	}
}

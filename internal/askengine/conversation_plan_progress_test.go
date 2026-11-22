// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// The plan round writes its phase onto the session operation because it has no
// event stream of its own; the console reads it back through the conversation
// state it already polls once a second. Both halves of that contract are
// pinned here: the writer keeps the field current, and State surfaces it only
// while the round is actually running.
func TestPlanPhaseWriterFeedsTheConversationState(t *testing.T) {
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, _ *http.Request) {})
	st := sessions.NewStore(t.TempDir())
	sess, err := st.Create("phase fixture")
	if err != nil {
		t.Fatal(err)
	}
	service := e.Conversations(st)
	t.Cleanup(service.Close)
	ctx := context.Background()

	op := sessions.Operation{ID: "round-1", Status: "running"}
	if err := st.StartOperation(sess.ID, op); err != nil {
		t.Fatal(err)
	}
	w := &planPhaseWriter{st: st, sid: sess.ID, op: op, minGap: 0}
	w.report("dispatch", "")
	w.task("task-1")
	w.report("exec", "claude_code")

	state, err := service.State(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Implementation == nil ||
		state.Implementation.Round != "round-1" ||
		state.Implementation.Phase != "exec" ||
		state.Implementation.Detail != "claude_code" ||
		state.Implementation.TaskID != "task-1" {
		t.Fatalf("implementation not surfaced: %+v", state.Implementation)
	}

	// A settled round must not keep advertising itself: the writer clears the
	// field, and the poll then reports nothing.
	w.stop()
	if state, err = service.State(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if state.Implementation != nil {
		t.Fatalf("settled round still reported as running: %+v", state.Implementation)
	}
}

// An operation that is no longer running is not an implementation in progress,
// whatever its last written snapshot says. This is the case a crash leaves
// behind: the file still says "running" with a phase, the operation does not.
func TestImplementationIsHiddenOnceTheOperationSettles(t *testing.T) {
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, _ *http.Request) {})
	st := sessions.NewStore(t.TempDir())
	sess, err := st.Create("settled fixture")
	if err != nil {
		t.Fatal(err)
	}
	service := e.Conversations(st)
	t.Cleanup(service.Close)
	ctx := context.Background()

	op := sessions.Operation{ID: "round-1", Status: "running",
		Plan: &sessions.PlanProgress{Round: "round-1", Phase: "exec"}}
	if err := st.StartOperation(sess.ID, op); err != nil {
		t.Fatal(err)
	}
	op.Status = "error"
	if _, err := st.FinishRunningOperation(sess.ID, op); err != nil {
		t.Fatal(err)
	}
	state, err := service.State(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Implementation != nil {
		t.Fatalf("finished operation advertised as implementing: %+v", state.Implementation)
	}
}

// Once the round has settled, a late progress event must not drag the
// operation back to "running" or re-create the phase line.
func TestStoppedPlanWriterIgnoresLateEvents(t *testing.T) {
	st := sessions.NewStore(t.TempDir())
	sess, err := st.Create("late event fixture")
	if err != nil {
		t.Fatal(err)
	}
	op := sessions.Operation{ID: "round-1", Status: "running"}
	if err := st.StartOperation(sess.ID, op); err != nil {
		t.Fatal(err)
	}
	w := &planPhaseWriter{st: st, sid: sess.ID, op: op, minGap: 0}
	w.report("exec", "")
	w.stop()
	// The real closing sequence.
	done := op
	done.Status = "done"
	if _, err := st.FinishRunningOperation(sess.ID, done); err != nil {
		t.Fatal(err)
	}
	// A tool event from a goroutine that outlived the round.
	w.report("tool", "grep")
	w.task("task-late")

	saved, err := st.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Operation == nil || saved.Operation.Status != "done" || saved.Operation.Plan != nil {
		t.Fatalf("late event rewrote a settled operation: %+v", saved.Operation)
	}
}

// The throttle exists so a busy tool loop does not rewrite the session file on
// every event, but a change held back must still land: otherwise the console
// keeps reading a stale phase for as long as the next quiet stretch lasts.
func TestThrottledPhaseFlushesOnTheNextTick(t *testing.T) {
	st := sessions.NewStore(t.TempDir())
	sess, err := st.Create("throttle fixture")
	if err != nil {
		t.Fatal(err)
	}
	op := sessions.Operation{ID: "round-1", Status: "running"}
	if err := st.StartOperation(sess.ID, op); err != nil {
		t.Fatal(err)
	}
	w := &planPhaseWriter{st: st, sid: sess.ID, op: op, minGap: time.Hour}
	w.report("dispatch", "")
	// Inside the window: recorded, not written.
	w.report("judge", "verdict")
	if got := operationPlan(t, st, sess.ID); got == nil || got.Phase != "dispatch" {
		t.Fatalf("throttle did not hold the write back: %+v", got)
	}
	w.flushPending()
	if got := operationPlan(t, st, sess.ID); got == nil || got.Phase != "judge" || got.Detail != "verdict" {
		t.Fatalf("held-back phase never landed: %+v", got)
	}
}

func operationPlan(t *testing.T, st *sessions.Store, sid string) *sessions.PlanProgress {
	t.Helper()
	saved, err := st.Get(sid)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Operation == nil {
		return nil
	}
	return saved.Operation.Plan
}

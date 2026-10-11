// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// waitTaskState polls a store until the task reaches a state or the deadline
// passes; used where wire delivery and execution are asynchronous.
func waitTaskState(t *testing.T, c *Core, taskID string, states ...string) Task {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		tk, err := c.store.Get(context.Background(), taskID)
		if err == nil {
			for _, s := range states {
				if tk.State == s {
					return tk
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s never reached %v (err=%v)", taskID, states, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestTrustOpenOriginDeclinedE2E: a delegate from an open-tier peer is
// refused at admission — before any capacity reservation — and the local row
// terminalizes without ever executing.
func TestTrustOpenOriginDeclinedE2E(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCoreWithNative(t, "entry-open", "127.0.0.1:18801",
		ledger.NativeAbility{ID: "sys:info", Command: "echo", Args: []string{"entry"}})
	worker := newCoreWithNative(t, "worker-open", "127.0.0.1:18802",
		ledger.NativeAbility{ID: "sys:info", Command: "echo", Args: []string{"worker"}})
	startPair(t, ctx, entry, worker, "127.0.0.1:18801", "127.0.0.1:18802")

	// The operator pins the inbound peer to open: its pushes are refused.
	if err := ledger.SetPeerTrust(worker.db, "entry-open", "open"); err != nil {
		t.Fatalf("pin open: %v", err)
	}

	env, _ := bus.NewEnvelope(bus.MsgTaskDelegate, "entry-open", "m-open", bus.TaskDelegatePayload{
		TaskID: "open-push", Title: "t", Intent: "x", Requires: []string{"sys:info"},
		Chain: []string{"entry-open"},
	})
	if err := entry.sendTo("worker-open", env); err != nil {
		t.Fatalf("send: %v", err)
	}
	tk := waitTaskState(t, worker, "open-push", StateCancelled)
	_ = tk
	// The admission refusal also went back over the wire as a decline frame;
	// the entry has no local copy so it is ignored there — the audit trail on
	// the worker is the record that matters.
}

// TestTrustGuardedReviewParkE2E: a delegate from a guarded peer under
// accept.mode=review parks in StateReview; `panda approve` semantics resume
// it through the queue path and it runs to done.
func TestTrustGuardedReviewParkE2E(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCoreWithNative(t, "entry-rev", "127.0.0.1:18803",
		ledger.NativeAbility{ID: "sys:info", Command: "echo", Args: []string{"entry"}})
	worker := newCoreWithNative(t, "worker-rev", "127.0.0.1:18804",
		ledger.NativeAbility{ID: "sys:info", Command: "echo", Args: []string{"worker"}})
	startPair(t, ctx, entry, worker, "127.0.0.1:18803", "127.0.0.1:18804")

	if err := ledger.SetPeerTrust(worker.db, "entry-rev", "guarded"); err != nil {
		t.Fatalf("pin guarded: %v", err)
	}
	worker.SetTrustConfig(testTrustConfig("review", "off"))

	env, _ := bus.NewEnvelope(bus.MsgTaskDelegate, "entry-rev", "m-rev", bus.TaskDelegatePayload{
		TaskID: "guarded-push", Title: "t", Intent: "x", Requires: []string{"sys:info"},
		Chain: []string{"entry-rev"},
	})
	if err := entry.sendTo("worker-rev", env); err != nil {
		t.Fatalf("send: %v", err)
	}

	// The admission park lands before any execution.
	tk := waitTaskState(t, worker, "guarded-push", StateReview)
	if tk.ApprovalDisposition != ApprovalAdmissionReview {
		t.Fatalf("disposition = %q, want %q", tk.ApprovalDisposition, ApprovalAdmissionReview)
	}

	// Operator approves: ResumeApproved runs the existing approve→claim→run
	// path and must drive the parked row to completion like any reviewed task.
	final, res, err := worker.ResumeApproved(ctx, "guarded-push")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if final.State != StateDone || !res.OK {
		t.Fatalf("reviewed task finished as %s (ok=%v), want done", final.State, res.OK)
	}
}

// TestReviewHoldKeepsDelegatorLease: a parked admission-review row keeps the
// delegator's dispatched copy alive — the first beat lands as a noted
// progress event explaining the silence, later beats renew the lease, the
// loop refuses to double-arm, and it exits once the row leaves review.
// resumeReviewHolds is the entry point under test: it is what Recover calls
// after a restart, and it must rediscover parked rows on its own.
func TestReviewHoldKeepsDelegatorLease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCoreWithNative(t, "entry-hold", "127.0.0.1:18811",
		ledger.NativeAbility{ID: "sys:info", Command: "echo", Args: []string{"entry"}})
	worker := newCoreWithNative(t, "worker-hold", "127.0.0.1:18812",
		ledger.NativeAbility{ID: "sys:info", Command: "echo", Args: []string{"worker"}})
	startPair(t, ctx, entry, worker, "127.0.0.1:18811", "127.0.0.1:18812")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	// Executor copy: parked in review carrying the entry→worker chain —
	// the exact row parkDelegateReview leaves behind.
	const taskID = "hold-task"
	_, err := worker.store.CreateWithID(ctx, taskID, "", "", "t", worker.nodeID,
		[]string{entry.nodeID, worker.nodeID}, true)
	must(err)
	wt, err := worker.store.Get(ctx, taskID)
	must(err)
	must(worker.store.transitionToReview(ctx, taskID, StateSubmitted, StateReview,
		worker.nodeID, EvReview, map[string]any{"reason": "test"}, ApprovalResumeExecution))

	// Delegator copy: dispatched to the worker under the matching attempt,
	// so the beats pass the executor/attempt authorization checks.
	_, err = entry.store.CreateWithID(ctx, taskID, "", "", "t", entry.nodeID, nil, false)
	must(err)
	must(entry.store.AdoptAttempt(ctx, taskID, wt.AttemptID))
	must(entry.store.Queue(ctx, taskID, entry.nodeID))
	must(entry.store.Dispatch(ctx, taskID, entry.nodeID, worker.nodeID))

	// Shrink the worker lease so the beat interval (lease/3, floored at
	// minLeaseRenewInterval) fires inside the test window.
	worker.mu.Lock()
	worker.leaseTimeout = 600 * time.Millisecond
	worker.mu.Unlock()

	// The restart path discovers the parked row and arms the hold.
	worker.resumeReviewHolds(ctx)

	// First beat: the delegator learns why the task is quiet.
	deadline := time.Now().Add(5 * time.Second)
	for !taskEventsContain(entry, ctx, taskID, "held for operator admission review") {
		if time.Now().After(deadline) {
			t.Fatal("hold note never reached the delegator")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Noteless beats keep the lease moving: the deadline must advance past
	// its first-beat value within a couple of intervals.
	first, err := entry.store.Get(ctx, taskID)
	must(err)
	if first.LeaseExpires == 0 {
		t.Fatal("no lease recorded after first beat")
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		cur, err := entry.store.Get(ctx, taskID)
		must(err)
		if cur.LeaseExpires > first.LeaseExpires {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lease never renewed past the first beat")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Re-arming an already-held row is a no-op, not a second loop.
	worker.holdReviewUpstream(taskID)
	if _, ok := worker.reviewHolds.Load(taskID); !ok {
		t.Fatal("hold not tracked in reviewHolds")
	}

	// Approval moves the row out of review; the hold must unwind itself.
	must(worker.store.Approve(ctx, taskID))
	deadline = time.Now().Add(3 * time.Second)
	for {
		if _, ok := worker.reviewHolds.Load(taskID); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hold loop still running after the task left review")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestAdmissionReviewIgnoresWireResume: the delegator is exactly the party an
// admission park exists to check — its task_resume must not un-park the row.
// Only this node's own operator (store.Approve → ResumeApproved) may release
// it. The refusal echoes the held verdict so the delegator's mirror stays
// truthfully parked rather than timing out.
func TestAdmissionReviewIgnoresWireResume(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCoreWithNative(t, "entry-esc", "127.0.0.1:18821",
		ledger.NativeAbility{ID: "sys:info", Command: "echo", Args: []string{"entry"}})
	worker := newCoreWithNative(t, "worker-esc", "127.0.0.1:18822",
		ledger.NativeAbility{ID: "sys:info", Command: "echo", Args: []string{"worker"}})
	startPair(t, ctx, entry, worker, "127.0.0.1:18821", "127.0.0.1:18822")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	if err := ledger.SetPeerTrust(worker.db, "entry-esc", "guarded"); err != nil {
		t.Fatalf("pin guarded: %v", err)
	}
	worker.SetTrustConfig(testTrustConfig("review", "off"))

	env, _ := bus.NewEnvelope(bus.MsgTaskDelegate, "entry-esc", "m-esc", bus.TaskDelegatePayload{
		TaskID: "esc-task", Title: "t", Intent: "x", Requires: []string{"sys:info"},
		Chain: []string{"entry-esc"},
	})
	must(entry.sendTo("worker-esc", env))

	wt := waitTaskState(t, worker, "esc-task", StateReview)
	if wt.ApprovalDisposition != ApprovalAdmissionReview {
		t.Fatalf("disposition = %q, want %q", wt.ApprovalDisposition, ApprovalAdmissionReview)
	}

	// Delegator mirror: dispatched under the same attempt so the refusal
	// echo passes its authorization checks.
	_, err := entry.store.CreateWithID(ctx, "esc-task", "", "", "t", entry.nodeID, nil, false)
	must(err)
	must(entry.store.AdoptAttempt(ctx, "esc-task", wt.AttemptID))
	must(entry.store.Queue(ctx, "esc-task", entry.nodeID))
	must(entry.store.Dispatch(ctx, "esc-task", entry.nodeID, worker.nodeID))

	// The delegator tries to talk the executor out of its own review.
	renv, _ := bus.NewEnvelope(bus.MsgTaskResume, "entry-esc", "m-esc-r",
		bus.TaskResumePayload{TaskID: "esc-task", AttemptID: wt.AttemptID})
	must(entry.sendTo("worker-esc", renv))

	// The parked row must not move — and must never have executed.
	time.Sleep(700 * time.Millisecond)
	if got, _ := worker.store.Get(ctx, "esc-task"); got.State != StateReview {
		t.Fatalf("wire resume un-parked admission review: state=%s", got.State)
	}

	// The echoed verdict parks the delegator's mirror with the same
	// disposition — honest convergence, not silence.
	waitTaskState(t, entry, "esc-task", StateReview)
	if got, _ := entry.store.Get(ctx, "esc-task"); got.ApprovalDisposition != ApprovalAdmissionReview {
		t.Fatalf("mirror disposition = %q, want %q", got.ApprovalDisposition, ApprovalAdmissionReview)
	}

	// The operator's own approve is the only door — it runs to done.
	final, res, err := worker.ResumeApproved(ctx, "esc-task")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if final.State != StateDone || !res.OK {
		t.Fatalf("approved task finished as %s (ok=%v), want done", final.State, res.OK)
	}
}

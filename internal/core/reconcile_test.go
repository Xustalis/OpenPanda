// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

// TestReconcileLateDone is the "做完了但是说没做完" fix at store level: a row
// a timeout path closed (lease expiry → failed, deadline → expired) still
// accepts the executor's real done, with the audit marker proving the row
// did not succeed normally.
func TestReconcileLateDone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s := newTestStore(t)

	mk := func() Task {
		task, err := s.Create(ctx, "", "", "work", "self", []string{"self"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		return task
	}

	// Lease-expired failure: late done reconciles to done.
	t.Run("lease_failed_to_done", func(t *testing.T) {
		task := mk()
		if err := s.Queue(ctx, task.TaskID, "self"); err != nil {
			t.Fatalf("queue: %v", err)
		}
		if err := s.ForceFail(ctx, task.TaskID, "lease expired"); err != nil {
			t.Fatalf("force fail: %v", err)
		}
		if s.failureWasRejected(ctx, task.TaskID) {
			t.Fatal("a lease expiry must not read as a human reject")
		}
		res := bus.TaskResultPayload{TaskID: task.TaskID, State: StateDone, OK: true, Stdout: "actually finished"}
		if err := s.ReconcileDone(ctx, task.TaskID, "self", res, StateFailed); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		after, err := s.Get(ctx, task.TaskID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if after.State != StateDone {
			t.Fatalf("state = %s, want done", after.State)
		}
	})

	// Deadline-expired bundle: the done the link finally delivered reconciles.
	t.Run("expired_to_done", func(t *testing.T) {
		task := mk()
		if err := s.Queue(ctx, task.TaskID, "self"); err != nil {
			t.Fatalf("queue: %v", err)
		}
		if err := s.MarkExpired(ctx, task.TaskID, "deadline exceeded"); err != nil {
			t.Fatalf("expire: %v", err)
		}
		res := bus.TaskResultPayload{TaskID: task.TaskID, State: StateDone, OK: true, Stdout: "arrived late"}
		if err := s.ReconcileDone(ctx, task.TaskID, "self", res, StateExpired); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		after, _ := s.Get(ctx, task.TaskID)
		if after.State != StateDone {
			t.Fatalf("state = %s, want done", after.State)
		}
	})

	// Human reject is the one verdict a late done must never overturn.
	t.Run("rejected_never_resurrects", func(t *testing.T) {
		task := mk()
		if err := s.Queue(ctx, task.TaskID, "self"); err != nil {
			t.Fatalf("queue: %v", err)
		}
		if err := s.Dispatch(ctx, task.TaskID, "self", "exec"); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if err := s.applyReviewCAS(ctx, task.TaskID, StateDispatched, "self", task.AttemptID,
			EvResult, map[string]any{"q": "needs input"}, nil, ApprovalAcceptWork); err != nil {
			t.Fatalf("park for review: %v", err)
		}
		if err := s.Reject(ctx, task.TaskID, "user said no"); err != nil {
			t.Fatalf("reject: %v", err)
		}
		if !s.failureWasRejected(ctx, task.TaskID) {
			t.Fatal("a human reject must be detectable so a late done cannot resurrect it")
		}
	})

	// Cancelled is terminal by human hand too: the CAS must refuse.
	t.Run("cancelled_stays_closed", func(t *testing.T) {
		task := mk()
		if err := s.Cancel(ctx, task.TaskID); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		res := bus.TaskResultPayload{TaskID: task.TaskID, State: StateDone, OK: true}
		if err := s.ReconcileDone(ctx, task.TaskID, "self", res, StateCancelled); !errors.Is(err, ErrConflict) {
			t.Fatalf("reconcile on cancelled must conflict, got %v", err)
		}
	})
}

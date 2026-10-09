// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

// TestResumeParksWhenExecutorUnreachable: approving a review-parked task whose
// executor link just dropped must park the approval in resume_outbox instead
// of failing the task — a transient flap must not kill the user's explicit
// consent. The parked approval carries no lease (an expiry would fail the
// local copy while the consent still intends delivery) and is redelivered on
// the executor's next hello.
func TestResumeParksWhenExecutorUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	root := pinCore(t, "pin-root", "x")
	leaf := pinCore(t, "pin-leaf", "x")
	if err := root.Register(ctx); err != nil {
		t.Fatalf("register root: %v", err)
	}

	// A task dispatched to the leaf that came back to review awaiting the
	// user's consent (the executor's refusal parks both copies in review).
	tk, err := root.store.Create(ctx, "", "", "approval probe", "pin-root", []string{"pin-root"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := root.store.Queue(ctx, tk.TaskID, "pin-root"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := root.store.Dispatch(ctx, tk.TaskID, "pin-root", "pin-leaf"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := root.store.Accept(ctx, tk.TaskID, "pin-root"); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := root.store.PauseForAnswer(ctx, tk.TaskID, "pin-root",
		map[string]any{"question": "run it?"}); err != nil {
		t.Fatalf("pause: %v", err)
	}

	// Approve while the executor is unreachable: the resume parks.
	resumeCtx, resumeCancel := context.WithCancel(ctx)
	defer resumeCancel()
	done := make(chan error, 1)
	go func() {
		_, _, rerr := root.ResumeApproved(resumeCtx, tk.TaskID)
		done <- rerr
	}()
	deadline := time.Now().Add(5 * time.Second)
	var parked bool
	for time.Now().Before(deadline) {
		var n int
		if err := root.db.QueryRow(
			`SELECT COUNT(*) FROM resume_outbox WHERE task_id=?`, tk.TaskID).Scan(&n); err != nil {
			t.Fatalf("resume_outbox query: %v", err)
		}
		if n > 0 {
			parked = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !parked {
		t.Fatal("approval did not park in resume_outbox (old behavior: task failed on the send error)")
	}
	got := mustGetTask(t, root, ctx, tk.TaskID)
	if got.State != StateDispatched {
		t.Fatalf("state = %s, want dispatched (claimed, consent parked for delivery)", got.State)
	}
	if got.LeaseExpires != 0 {
		t.Fatalf("parked approval holds lease %d — an expiry would fail the copy while custody still delivers", got.LeaseExpires)
	}

	// The executor returns: its hello flushes the parked approval.
	wireTwo(t, ctx, leaf, root, "127.0.0.1:18411", "127.0.0.1:18412")
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := root.db.QueryRow(
			`SELECT COUNT(*) FROM resume_outbox WHERE task_id=?`, tk.TaskID).Scan(&n); err != nil {
			t.Fatalf("resume_outbox query: %v", err)
		}
		if n == 0 {
			// Delivered. The leaf has no local copy in this harness, so its
			// handleResume logs "unknown task" — the observable contract here
			// is that the approval left custody onto the wire instead of
			// dying with the first failed send.
			if mustGetTask(t, root, ctx, tk.TaskID).State == StateFailed {
				t.Fatal("task failed despite the approval being redelivered")
			}
			resumeCancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("resume call never returned after cancellation")
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("parked approval was never redelivered after the executor returned")
}

// TestHasPendingCustody: the redial loops tighten their cadence when an
// outbox holds undelivered rows — the check must see every custody table and
// clear the moment the row is delivered.
func TestHasPendingCustody(t *testing.T) {
	ctx := context.Background()
	root := pinCore(t, "pin-root", "x")
	if root.HasPendingCustody(ctx) {
		t.Fatal("fresh store must hold no custody")
	}
	tk, err := root.store.Create(ctx, "", "", "custody probe", "pin-root", []string{"pin-root"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	root.taskOutboxPersist(ctx, "pin-leaf", bus.TaskDelegatePayload{TaskID: tk.TaskID},
		"pin", time.Now().Add(time.Hour).Unix())
	if !root.HasPendingCustody(ctx) {
		t.Fatal("parked task must register as pending custody")
	}
	root.taskOutboxDrop(ctx, "pin-leaf", tk.TaskID)
	if root.HasPendingCustody(ctx) {
		t.Fatal("delivered custody must clear")
	}
	root.resumeOutboxPersist(ctx, "pin-leaf", bus.TaskResumePayload{TaskID: tk.TaskID},
		time.Now().Add(time.Hour).Unix())
	if !root.HasPendingCustody(ctx) {
		t.Fatal("parked approval must register as pending custody")
	}
	root.resumeOutboxDrop(ctx, "pin-leaf", tk.TaskID)
	if root.HasPendingCustody(ctx) {
		t.Fatal("all custody delivered must clear")
	}
}

// TestTaskOutboxTTLCoversParkedResume: a synchronous waiter bounded on
// custody must honor a parked approval's ttl — the same window the live wait
// would have used — not fall back to a bare lease guess.
func TestTaskOutboxTTLCoversParkedResume(t *testing.T) {
	ctx := context.Background()
	root := pinCore(t, "pin-root", "x")
	tk, err := root.store.Create(ctx, "", "", "ttl probe", "pin-root", []string{"pin-root"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if ttl := root.taskOutboxTTL(ctx, tk.TaskID); ttl != 0 {
		t.Fatalf("ttl with nothing parked = %d, want 0", ttl)
	}
	want := time.Now().Add(15 * time.Minute).Unix()
	root.resumeOutboxPersist(ctx, "pin-leaf", bus.TaskResumePayload{TaskID: tk.TaskID}, want)
	if ttl := root.taskOutboxTTL(ctx, tk.TaskID); ttl != want {
		t.Fatalf("ttl with parked resume = %d, want %d", ttl, want)
	}
	// The task_outbox side still wins when it is later.
	later := time.Now().Add(30 * time.Minute).Unix()
	root.taskOutboxPersist(ctx, "pin-leaf", bus.TaskDelegatePayload{TaskID: tk.TaskID}, "pin", later)
	if ttl := root.taskOutboxTTL(ctx, tk.TaskID); ttl != later {
		t.Fatalf("ttl with both parked = %d, want max %d", ttl, later)
	}
}

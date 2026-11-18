// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
)

func retriedSummaryFixture(t *testing.T, verdict string) summaryDeliveryFixture {
	t.Helper()
	f := newSummaryDeliveryFixture(t, true)
	ctx := context.Background()
	now := f.task.UpdatedAt
	if err := f.h.store.RecordEvent(ctx, f.task.TaskID, core.EvSupervise, map[string]any{
		"status": "failed", "reason": "old attempt rejected",
	}); err != nil {
		t.Fatal(err)
	}
	f.h.finalizeSessionSweep(ctx)
	f.assertTurns(t, 1)
	if _, err := f.h.store.RequeueForRetry(ctx, f.task.TaskID, "node", false, f.task.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err := f.h.store.Accept(ctx, f.task.TaskID, "node"); err != nil {
		t.Fatal(err)
	}
	if verdict != "" {
		if err := f.h.store.RecordEvent(ctx, f.task.TaskID, core.EvSupervise, map[string]any{
			"status": "done", "reason": verdict,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.h.store.Complete(ctx, f.task.TaskID, "node", map[string]any{"ok": true, "stdout": "new attempt completed"}); err != nil {
		t.Fatal(err)
	}
	// Event IDs retain causal ordering even when timestamps share a second.
	if _, err := f.db.Exec(`UPDATE task_events SET ts=CASE
		WHEN id < (SELECT MAX(id) FROM task_events WHERE task_id=? AND type=?) THEN ?
		WHEN type=? OR type=? THEN ? WHEN type=? THEN ? ELSE ? END
		WHERE task_id=?`, f.task.TaskID, core.EvRetry, now-500,
		core.EvRetry, core.EvDelegate, now-10, core.EvAccept, now-7, now, f.task.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE tasks SET updated_at=? WHERE task_id=?`, now, f.task.TaskID); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestFinalizerRetryUsesCurrentAttempt(t *testing.T) {
	for _, verdict := range []string{"", "current attempt accepted"} {
		t.Run(verdict, func(t *testing.T) {
			f := retriedSummaryFixture(t, verdict)
			f.h.finalizeSessionSweep(context.Background())
			f.assertTurns(t, 2)
			sess, err := f.h.sessions.Get(f.sessionID)
			if err != nil {
				t.Fatal(err)
			}
			turn := sess.Turns[1]
			if turn.DurationMS != 7000 {
				t.Errorf("retry duration = %dms, want 7000ms without the prior attempt", turn.DurationMS)
			}
			if turn.Verdict != verdict {
				t.Errorf("retry verdict = %q, want %q", turn.Verdict, verdict)
			}
			if sess.Turns[0].Verdict != "old attempt rejected" {
				t.Error("retry changed the already-delivered prior report")
			}
		})
	}
}

func TestFinalizerRetryWithoutExecutionHasNoOldTimingOrVerdict(t *testing.T) {
	f := newSummaryDeliveryFixture(t, true)
	ctx := context.Background()
	if err := f.h.store.RecordEvent(ctx, f.task.TaskID, core.EvSupervise, map[string]any{
		"reason": "prior attempt rejected",
	}); err != nil {
		t.Fatal(err)
	}
	f.h.finalizeSessionSweep(ctx)
	if _, err := f.h.store.RequeueForRetry(ctx, f.task.TaskID, "node", false, f.task.AttemptID); err != nil {
		t.Fatal(err)
	}
	// A reroute can fail before any accept/agent start in the new attempt.
	if err := f.h.store.Fail(ctx, f.task.TaskID, "node", "no executable route"); err != nil {
		t.Fatal(err)
	}
	f.h.finalizeSessionSweep(ctx)
	f.assertTurns(t, 2)
	sess, err := f.h.sessions.Get(f.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if turn := sess.Turns[1]; turn.DurationMS != 0 || turn.Verdict != "" {
		t.Fatalf("unstarted retry reused old execution data: %+v", turn)
	}
}

func TestFinalizerLegacyTimelineWithoutAttemptMarkers(t *testing.T) {
	f := newSummaryDeliveryFixture(t, false)
	ctx := context.Background()
	if _, err := f.db.Exec(`DELETE FROM task_events WHERE task_id=? AND type IN (?,?)`, f.task.TaskID, core.EvSubmit, core.EvRetry); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE task_events SET ts=? WHERE task_id=? AND type=?`, f.task.UpdatedAt-7, f.task.TaskID, core.EvAccept); err != nil {
		t.Fatal(err)
	}
	if err := f.h.store.RecordEvent(ctx, f.task.TaskID, core.EvSupervise, map[string]any{"reason": "legacy judgement"}); err != nil {
		t.Fatal(err)
	}
	if got := f.h.taskExecutionStart(ctx, f.task.TaskID); got != f.task.UpdatedAt-7 {
		t.Fatalf("legacy execution start = %d", got)
	}
	if got := f.h.taskVerdict(ctx, f.task.TaskID); got != "legacy judgement" {
		t.Fatalf("legacy verdict = %q", got)
	}
}

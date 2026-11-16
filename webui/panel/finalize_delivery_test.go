// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/testdb"
)

type summaryDeliveryFixture struct {
	h         *handler
	db        *sql.DB
	task      core.Task
	root      string
	sessionID string
}

func newSummaryDeliveryFixture(t *testing.T, failed bool) summaryDeliveryFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	db := testdb.OpenFile(t, filepath.Join(root, "tasks.db"))
	store := core.NewTaskStore(db, slog.New(slog.DiscardHandler))
	root = filepath.Join(root, "sessions")
	sessStore := sessions.NewStore(root)
	sess, err := sessStore.Create("summary delivery")
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.Create(ctx, "", "", "finished task", "node", []string{"node"})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error { return store.SetQueueMeta(ctx, task.TaskID, core.PriorityNormal, sess.ID, "", nil) },
		func() error { return store.Queue(ctx, task.TaskID, "node") },
		func() error { return store.Dispatch(ctx, task.TaskID, "node", "node") },
		func() error { return store.Accept(ctx, task.TaskID, "node") },
		func() error {
			if failed {
				return store.Fail(ctx, task.TaskID, "node", "first attempt failed")
			}
			return store.Complete(ctx, task.TaskID, "node", map[string]any{"ok": true, "stdout": "completed"})
		},
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	task, err = store.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	return summaryDeliveryFixture{
		h: &handler{store: store, sessions: sessStore}, db: db,
		task: task, root: root, sessionID: sess.ID,
	}
}

func (f summaryDeliveryFixture) assertTurns(t *testing.T, want int) {
	t.Helper()
	sess, err := sessions.NewStore(f.root).Get(f.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Turns) != want {
		t.Fatalf("summary turns = %d, want %d", len(sess.Turns), want)
	}
}

func TestFinalizerRetriesFailedTranscriptWrite(t *testing.T) {
	f := newSummaryDeliveryFixture(t, false)
	path := filepath.Join(f.root, f.sessionID+".json")
	backup := path + ".unavailable"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	f.h.finalizeSessionSweep(context.Background())
	var markers int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM task_events WHERE type = ?", evSessionSummary).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if markers != 0 {
		t.Fatal("failed transcript write committed a delivery marker")
	}
	if err := os.Rename(backup, path); err != nil {
		t.Fatal(err)
	}
	f.h.finalizeSessionSweep(context.Background())
	f.h.finalizeSessionSweep(context.Background())
	f.assertTurns(t, 1)
}

func TestFinalizerReplaysAfterMarkerFailure(t *testing.T) {
	f := newSummaryDeliveryFixture(t, false)
	if _, err := f.db.Exec(`CREATE TRIGGER fail_summary_marker BEFORE INSERT ON task_events
		WHEN NEW.type = 'session_summary'
		BEGIN SELECT RAISE(ABORT, 'injected marker failure'); END`); err != nil {
		t.Fatal(err)
	}
	f.h.finalizeSessionSweep(context.Background())
	f.assertTurns(t, 1)
	pending, err := f.h.store.TerminalSessionTasksWithoutEvent(context.Background(), evSessionSummary)
	if err != nil || len(pending) != 1 {
		t.Fatalf("marker failure must remain retryable: pending=%d err=%v", len(pending), err)
	}
	if _, err := f.db.Exec("DROP TRIGGER fail_summary_marker"); err != nil {
		t.Fatal(err)
	}
	// A new handler/store instance sees the persistent idempotency key, as
	// after a restart between transcript commit and event-marker commit.
	restarted := &handler{store: f.h.store, sessions: sessions.NewStore(f.root)}
	restarted.finalizeSessionSweep(context.Background())
	f.assertTurns(t, 1)
	pending, err = f.h.store.TerminalSessionTasksWithoutEvent(context.Background(), evSessionSummary)
	if err != nil || len(pending) != 0 {
		t.Fatalf("successful replay did not acknowledge delivery: pending=%d err=%v", len(pending), err)
	}
}

func TestFinalizerConcurrentDeliveriesAreIdempotent(t *testing.T) {
	f := newSummaryDeliveryFixture(t, false)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h := &handler{store: f.h.store, sessions: sessions.NewStore(f.root)}
			h.finalizeSessionSweep(context.Background())
		}()
	}
	wg.Wait()
	f.assertTurns(t, 1)
}

func TestFinalizerReportsRetriedAttemptSeparately(t *testing.T) {
	f := newSummaryDeliveryFixture(t, true)
	ctx := context.Background()
	f.h.finalizeSessionSweep(ctx)
	f.assertTurns(t, 1)
	if _, err := f.h.store.RequeueForRetry(ctx, f.task.TaskID, "node", false, f.task.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err := f.h.store.Accept(ctx, f.task.TaskID, "node"); err != nil {
		t.Fatal(err)
	}
	if err := f.h.store.Complete(ctx, f.task.TaskID, "node", map[string]any{"ok": true, "stdout": "recovered"}); err != nil {
		t.Fatal(err)
	}
	f.h.finalizeSessionSweep(ctx)
	f.h.finalizeSessionSweep(ctx)
	f.assertTurns(t, 2)
	sess, err := f.h.sessions.Get(f.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Turns[0].IdempotencyKey == sess.Turns[1].IdempotencyKey {
		t.Fatal("new attempt reused the failed attempt's summary key")
	}
}

func TestFinalizerPreservesDeliveryOutsideReplayDigest(t *testing.T) {
	f := newSummaryDeliveryFixture(t, false)
	report := "## Changes\nFixed request handling.\n\n## Verification\ngo test ./... passed.\n\n## Remaining\nManual deployment is still required."
	if _, err := f.db.Exec("UPDATE tasks SET result_json = ? WHERE task_id = ?", `{"stdout":`+strconv.Quote(report)+`}`, f.task.TaskID); err != nil {
		t.Fatal(err)
	}
	f.h.finalizeSessionSweep(context.Background())
	f.h.finalizeSessionSweep(context.Background())
	sess, err := sessions.NewStore(f.root).Get(f.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Turns) != 1 || sess.Turns[0].Delivery != report {
		t.Fatalf("delivery was lost or duplicated: %+v", sess.Turns)
	}
	if strings.Contains(sess.Turns[0].Text, "go test ./...") {
		t.Fatal("display payload leaked into replay digest")
	}
}

func TestTaskDeliveryBoundsRunesWithoutLosingResult(t *testing.T) {
	report := strings.Repeat("验证", maxDeliveryRunes)
	payload := `{"result":` + strconv.Quote(report) + `}`
	got, clipped := taskDelivery(payload)
	if !clipped || utf8.RuneCountInString(got) != maxDeliveryRunes || !utf8.ValidString(got) {
		t.Fatalf("invalid bounded delivery: runes=%d clipped=%v", utf8.RuneCountInString(got), clipped)
	}
	if extractResultText(payload) != report {
		t.Fatal("original task output was truncated")
	}
	if got, clipped := taskDelivery(`{"stdout":""}`); got != "" || clipped {
		t.Fatalf("empty delivery fabricated output: %q %v", got, clipped)
	}
}

// TestFinalizerMeasuresTheRunNotTheQueue: the report's duration is the work,
// not the wait. It used to be updated_at-created_at, which billed one task for
// the sibling it queued behind — a route check that failed in 0.18s printed
// "424s", and a task cancelled before it ever started printed "124s".
func TestFinalizerMeasuresTheRunNotTheQueue(t *testing.T) {
	f := newSummaryDeliveryFixture(t, false)
	ctx := context.Background()

	// Backdate the submission by 500s and pin the accept to 7s before the
	// terminal write: the row now looks like a task that queued for ~8 minutes
	// and ran for 7 seconds.
	if _, err := f.db.ExecContext(ctx, `UPDATE tasks SET created_at = updated_at - 500 WHERE task_id = ?`, f.task.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(ctx, `UPDATE task_events SET ts = ? WHERE task_id = ? AND type = ?`,
		f.task.UpdatedAt-7, f.task.TaskID, core.EvAccept); err != nil {
		t.Fatal(err)
	}

	f.h.finalizeSessionSweep(ctx)

	sess, err := sessions.NewStore(f.root).Get(f.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Turns) != 1 {
		t.Fatalf("turns = %d, want 1", len(sess.Turns))
	}
	if got := sess.Turns[0].DurationMS; got != 7000 {
		t.Fatalf("duration = %dms, want 7000 (the run), not %d (queue wait)", got, 500000)
	}
}

// A task that never left the queue has no execution window; inventing one
// prints a stopwatch for work that never happened.
func TestFinalizerLeavesTheDurationEmptyWhenNothingRan(t *testing.T) {
	f := newSummaryDeliveryFixture(t, false)
	ctx := context.Background()
	if _, err := f.db.ExecContext(ctx, `DELETE FROM task_events WHERE task_id = ? AND type IN (?, ?)`,
		f.task.TaskID, core.EvAccept, core.EvExecAgentStart); err != nil {
		t.Fatal(err)
	}
	if got := f.h.taskExecutionStart(ctx, f.task.TaskID); got != 0 {
		t.Fatalf("execution start = %d, want 0", got)
	}
	f.h.finalizeSessionSweep(ctx)
	sess, err := sessions.NewStore(f.root).Get(f.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Turns) != 1 || sess.Turns[0].DurationMS != 0 {
		t.Fatalf("turn = %+v, want no duration", sess.Turns)
	}
}

// TestFinalizerFilesPandasVerdictWithTheReport: the turn carries the judge's
// sentence both as the head of its replay digest and as Verdict for the
// console, so the bubble can print the house's assessment above the agent's
// own closing paragraph.
func TestFinalizerFilesPandasVerdictWithTheReport(t *testing.T) {
	f := newSummaryDeliveryFixture(t, false)
	ctx := context.Background()
	verdict := "已报告 agent 名称与工作目录，退出码 0，无文件改动。"
	if err := f.h.store.RecordEvent(ctx, f.task.TaskID, core.EvSupervise, map[string]any{
		"round": 1, "status": "done", "reason": verdict, "followup": "",
	}); err != nil {
		t.Fatal(err)
	}

	f.h.finalizeSessionSweep(ctx)

	sess, err := sessions.NewStore(f.root).Get(f.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Turns) != 1 {
		t.Fatalf("turns = %d, want 1", len(sess.Turns))
	}
	turn := sess.Turns[0]
	if turn.Verdict != verdict {
		t.Fatalf("verdict = %q, want the judge's sentence", turn.Verdict)
	}
	if !strings.Contains(turn.Text, verdict) || !strings.Contains(turn.Text, "\n> completed") {
		t.Fatalf("digest = %q, want the verdict then the quoted conclusion", turn.Text)
	}
}

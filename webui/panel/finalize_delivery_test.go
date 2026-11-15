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

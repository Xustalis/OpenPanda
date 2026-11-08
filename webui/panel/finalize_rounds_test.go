// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

func addRoundTask(t *testing.T, f summaryDeliveryFixture, roundID, title string) core.Task {
	t.Helper()
	task, err := f.h.store.Create(core.WithAskRound(context.Background(), roundID), "", "", title, "node", []string{"node"})
	if err != nil {
		t.Fatal(err)
	}
	if task.SessionID != f.sessionID {
		t.Fatal("task was not linked atomically to round session")
	}
	return task
}

func roundTurns(t *testing.T, f summaryDeliveryFixture) []sessions.Turn {
	t.Helper()
	s, err := f.h.sessions.Get(f.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var out []sessions.Turn
	for _, turn := range s.Turns {
		if turn.RoundID != "" {
			out = append(out, turn)
		}
	}
	return out
}

func TestRoundSummarySeparatesOverlappingDispatches(t *testing.T) {
	f := newSummaryDeliveryFixture(t, false)
	ctx := context.Background()
	for _, id := range []string{"round-a", "round-b"} {
		if err := f.h.store.BeginAskRound(ctx, id, f.sessionID, "zh-CN"); err != nil {
			t.Fatal(err)
		}
	}
	a := addRoundTask(t, f, "round-a", "first task")
	b := addRoundTask(t, f, "round-a", "second task")
	c := addRoundTask(t, f, "round-b", "unrelated task")
	if err := f.h.store.Cancel(ctx, a.TaskID); err != nil {
		t.Fatal(err)
	}
	if err := f.h.store.Cancel(ctx, b.TaskID); err != nil {
		t.Fatal(err)
	}
	f.h.finalizeAskRounds(ctx)
	if n := len(roundTurns(t, f)); n != 0 {
		t.Fatalf("summarized open dispatch: %d", n)
	}
	if err := f.h.store.CloseAskRound(ctx, "round-a", "closed"); err != nil {
		t.Fatal(err)
	}
	f.h.finalizeAskRounds(ctx)
	turns := roundTurns(t, f)
	if len(turns) != 1 || turns[0].RoundID != "round-a" || !strings.Contains(turns[0].Text, "2 个任务") || strings.Contains(turns[0].Text, c.TaskID) {
		t.Fatalf("wrong round: %+v", turns)
	}
	// New handler/store simulates restart, then concurrent sweeps stay idempotent.
	f.h = &handler{store: core.NewTaskStore(f.db, nil), sessions: sessions.NewStore(f.root)}
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() { defer wg.Done(); f.h.finalizeAskRounds(ctx) }()
	}
	wg.Wait()
	if len(roundTurns(t, f)) != 1 {
		t.Fatal("duplicate round summary")
	}
	if err := f.h.store.CloseAskRound(ctx, "round-b", "stopped"); err != nil {
		t.Fatal(err)
	}
	f.h.finalizeAskRounds(ctx)
	if len(roundTurns(t, f)) != 1 {
		t.Fatal("detaching request cancelled unfinished task")
	}
	if err := f.h.store.Cancel(ctx, c.TaskID); err != nil {
		t.Fatal(err)
	}
	f.h.finalizeAskRounds(ctx)
	turns = roundTurns(t, f)
	if len(turns) != 2 || !strings.Contains(turns[1].Text, "不代表已取消排队任务") {
		t.Fatalf("stopped round: %+v", turns)
	}
}

func TestRoundSummaryReportsReviewAndLaterResolution(t *testing.T) {
	f := newSummaryDeliveryFixture(t, false)
	ctx := context.Background()
	if err := f.h.store.BeginAskRound(ctx, "round-review", f.sessionID, "en"); err != nil {
		t.Fatal(err)
	}
	a := addRoundTask(t, f, "round-review", "needs human")
	// Simulate executor settlement; task lifecycle legality is covered by core tests.
	if _, err := f.db.Exec(`UPDATE tasks SET state=?,state_version=state_version+1 WHERE task_id=?`, core.StateReview, a.TaskID); err != nil {
		t.Fatal(err)
	}
	if err := f.h.store.CloseAskRound(ctx, "round-review", "closed"); err != nil {
		t.Fatal(err)
	}
	f.h.finalizeAskRounds(ctx)
	turns := roundTurns(t, f)
	if len(turns) != 1 || !strings.Contains(turns[0].Text, "1 need review") {
		t.Fatalf("review missing: %+v", turns)
	}
	if _, err := f.db.Exec(`UPDATE tasks SET state=?,attempt_id='retried-attempt',state_version=state_version+1 WHERE task_id=?`, core.StateDone, a.TaskID); err != nil {
		t.Fatal(err)
	}
	f.h.finalizeAskRounds(ctx)
	f.h.finalizeAskRounds(ctx)
	turns = roundTurns(t, f)
	if len(turns) != 2 || !strings.Contains(turns[1].Text, "1 completed") || turns[0].IdempotencyKey == turns[1].IdempotencyKey {
		t.Fatalf("resolution suppressed: %+v", turns)
	}
}

func TestRoundSummaryRecoversExpiredObserverAndMarkerFailure(t *testing.T) {
	f := newSummaryDeliveryFixture(t, false)
	ctx := context.Background()
	if err := f.h.store.BeginAskRound(ctx, "abandoned", f.sessionID, "en"); err != nil {
		t.Fatal(err)
	}
	a := addRoundTask(t, f, "abandoned", "orphaned request")
	if err := f.h.store.Cancel(ctx, a.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE ask_rounds SET lease_until=0 WHERE round_id='abandoned'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`CREATE TRIGGER fail_round_marker BEFORE UPDATE OF reported_version ON ask_rounds BEGIN SELECT RAISE(ABORT,'marker fault'); END`); err != nil {
		t.Fatal(err)
	}
	f.h.finalizeAskRounds(ctx)
	turns := roundTurns(t, f)
	if len(turns) != 1 || !strings.Contains(turns[0].Text, "interrupted") {
		t.Fatalf("orphan not recovered: %+v", turns)
	}
	if _, err := f.db.Exec(`DROP TRIGGER fail_round_marker`); err != nil {
		t.Fatal(err)
	}
	f.h = &handler{store: core.NewTaskStore(f.db, nil), sessions: sessions.NewStore(f.root)}
	f.h.finalizeAskRounds(ctx)
	if len(roundTurns(t, f)) != 1 {
		t.Fatal("replay duplicated persisted summary")
	}
	rounds, err := f.h.store.SettledAskRounds(ctx)
	if err != nil || len(rounds) != 0 {
		t.Fatalf("marker did not acknowledge: %+v %v", rounds, err)
	}
}

func TestRoundTaskCreationRejectsClosedRound(t *testing.T) {
	f := newSummaryDeliveryFixture(t, false)
	ctx := context.Background()
	if err := f.h.store.BeginAskRound(ctx, "sealed", f.sessionID, "en"); err != nil {
		t.Fatal(err)
	}
	if err := f.h.store.CloseAskRound(ctx, "sealed", "closed"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.store.Create(core.WithAskRound(ctx, "sealed"), "", "", "late task", "node", nil); err == nil {
		t.Fatal("late task escaped dispatch boundary")
	}
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE title='late task'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("task not rolled back: %d %v", n, err)
	}
}

func TestRoundSummaryRetriesTranscriptFailure(t *testing.T) {
	f := newSummaryDeliveryFixture(t, false)
	ctx := context.Background()
	if err := f.h.store.BeginAskRound(ctx, "transcript-fault", f.sessionID, "en"); err != nil {
		t.Fatal(err)
	}
	task := addRoundTask(t, f, "transcript-fault", "finished job")
	if err := f.h.store.Cancel(ctx, task.TaskID); err != nil {
		t.Fatal(err)
	}
	if err := f.h.store.CloseAskRound(ctx, "transcript-fault", "closed"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.root, f.sessionID+".json")
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	f.h.finalizeAskRounds(ctx)
	rounds, err := f.h.store.SettledAskRounds(ctx)
	if err != nil || len(rounds) != 1 {
		t.Fatalf("failed write acknowledged: %+v %v", rounds, err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	f.h.finalizeAskRounds(ctx)
	f.h.finalizeAskRounds(ctx)
	if len(roundTurns(t, f)) != 1 {
		t.Fatal("round not retried exactly once")
	}
}

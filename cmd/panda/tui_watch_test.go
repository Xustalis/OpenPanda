//go:build !lite

package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// collectPrinted flattens a command (possibly a batch) into the text its
// tea.Println members would print. Commands that return nil contribute nothing.
func collectPrinted(cmd tea.Cmd) string {
	if cmd == nil {
		return ""
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var sb strings.Builder
		for _, c := range batch {
			sb.WriteString(collectPrinted(c) + "\n")
		}
		return sb.String()
	}
	if msg == nil {
		return ""
	}
	return fmt.Sprintf("%v", msg)
}

// TestOnWatchCommitsNotes checks an out-of-band completion lands in scrollback as
// a transcript note — the TUI's stand-in for the classic watcher's printed line.
func TestOnWatchCommitsNotes(t *testing.T) {
	m := newTestTUI(t) // no store: the re-arm command is nil, so the batch is inspectable
	_, cmd := m.onWatch(watchMsg{notes: []string{"✓ build docs (done)", "✗ deploy (failed)"}})
	printed := collectPrinted(cmd)
	if !strings.Contains(printed, "build docs") || !strings.Contains(printed, "deploy") {
		t.Errorf("both completions should be committed: %q", printed)
	}
	// An empty poll is silent but must still re-arm nothing more than itself.
	if _, cmd := m.onWatch(watchMsg{}); collectPrinted(cmd) != "" {
		t.Errorf("an empty poll should print nothing: %q", collectPrinted(cmd))
	}
}

// TestPollCompletionsReportsTerminalTransitions drives the shared poll step over a
// real store: a task that fails after the baseline was taken is reported once, and
// a second poll with nothing new is silent.
func TestPollCompletionsReportsTerminalTransitions(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	st := core.NewTaskStore(db, nil)
	task, err := st.Create(ctx, "", "", "build docs", "node-a", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	r := &repl{store: st}
	r.resetWatchBaseline() // the queued task is already known: not news

	if events := r.pollCompletions(ctx); len(events) != 0 {
		t.Fatalf("an unchanged store should report nothing, got %v", events)
	}
	if err := st.ForceFail(ctx, task.TaskID, "boom"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	events := r.pollCompletions(ctx)
	if len(events) != 1 || !strings.Contains(events[0].note, "build docs") {
		t.Fatalf("the failure should be reported once: %v", events)
	}
	if events[0].review {
		t.Fatalf("a failed task is not a pending approval: %+v", events[0])
	}
	if events := r.pollCompletions(ctx); len(events) != 0 {
		t.Fatalf("a reported task must not repeat: %v", events)
	}
}

// TestOnWatchRaisesApprovalCardForReview is the out-of-band half of the "no
// silent approval" rule: a task that parks in review while no ask is running
// must be put in front of the user as the same foreground approval card an
// inline task raises — not merely mentioned in scrollback. The note is still
// committed (it names /approve and /reject), and a second review task in the
// same poll keeps its note rather than stealing the card.
func TestOnWatchRaisesApprovalCardForReview(t *testing.T) {
	m := newTestTUI(t)
	first := core.Task{TaskID: "task-review-1", Title: "delete the staging bucket",
		State: core.StateReview, Intent: "aws s3 rb", ApprovalDisposition: core.ApprovalResumeExecution}
	second := core.Task{TaskID: "task-review-2", Title: "drop the old table",
		State: core.StateReview, ApprovalDisposition: core.ApprovalResumeExecution}
	next, cmd := m.onWatch(watchMsg{events: []watchEvent{
		{note: "review 1 — /approve task-review-1", task: first, review: true},
		{note: "review 2 — /approve task-review-2", task: second, review: true},
	}})
	got := next.(tuiModel)
	if got.mode != modeApproving || got.pending == nil || got.pending.Approval == nil {
		t.Fatalf("a review task must raise the approval card: mode=%v pending=%+v", got.mode, got.pending)
	}
	if got.pending.Approval.TaskID != first.TaskID {
		t.Fatalf("card should hold the first review task, got %q", got.pending.Approval.TaskID)
	}
	if got.approvalSel != 1 {
		t.Fatalf("the card must start focused on deny, got %d", got.approvalSel)
	}
	if got.pendingWorkDir != "" {
		t.Fatalf("an out-of-band task resumes in its own tree, got %q", got.pendingWorkDir)
	}
	printed := collectPrinted(cmd)
	if !strings.Contains(printed, "task-review-1") || !strings.Contains(printed, "task-review-2") {
		t.Fatalf("both review notes should be committed: %q", printed)
	}
}

// TestOnWatchNeverStealsAnActiveTurn keeps the card from hijacking the screen:
// while an ask is in flight (or another card is already up) the review task is
// only reported, so the user's current interaction is never displaced — the
// task stays in review either way, reachable via /approve.
func TestOnWatchNeverStealsAnActiveTurn(t *testing.T) {
	review := core.Task{TaskID: "task-review-3", Title: "rm -rf build", State: core.StateReview,
		ApprovalDisposition: core.ApprovalResumeExecution}
	ev := watchEvent{note: "review 3", task: review, review: true}

	m := newTestTUI(t)
	m.mode = modeAsking
	next, _ := m.onWatch(watchMsg{events: []watchEvent{ev}})
	if got := next.(tuiModel); got.mode != modeAsking || got.pending != nil {
		t.Fatalf("an in-flight ask must not be displaced: mode=%v pending=%+v", got.mode, got.pending)
	}

	// needs_changed_input cannot be continued by consent, so no card is raised:
	// approving it would be meaningless, and the note says what to do instead.
	blocked := review
	blocked.ApprovalDisposition = core.ApprovalNeedsChangedInput
	m2 := newTestTUI(t)
	next2, _ := m2.onWatch(watchMsg{events: []watchEvent{{note: "blocked", task: blocked, review: true}}})
	if got := next2.(tuiModel); got.pending != nil {
		t.Fatalf("needs_changed_input must not raise a yes/no card: %+v", got.pending)
	}
}

// TestPollCompletionsFlagsStalls is the watcher's second job: not only
// transitions, but a task that stops moving entirely — queued with no
// consumer, active past its own lease, or parked in review long enough to
// remind again. A stuck task must surface in the foreground; silence is the
// failure mode under test.
func TestPollCompletionsFlagsStalls(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	st := core.NewTaskStore(db, nil)
	now := time.Now().Unix()
	stale := now - 11*60 // past watchStallQueued

	backdate := func(id string, updatedAt, lease int64) {
		t.Helper()
		if _, err := db.Exec(`UPDATE tasks SET updated_at=?, lease_expires_at=? WHERE task_id=?`, updatedAt, lease, id); err != nil {
			t.Fatalf("backdate %s: %v", id, err)
		}
	}

	// A queued task nobody is consuming.
	queued, err := st.Create(ctx, "", "", "stuck queued", "node-a", nil)
	if err != nil {
		t.Fatalf("create queued: %v", err)
	}
	if err := st.Queue(ctx, queued.TaskID, "node-a"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	backdate(queued.TaskID, stale, 0)

	// A running task already past its lease: the monitor should have failed
	// it; if it is still listed as running, nobody is expiring.
	running, err := st.Create(ctx, "", "", "stuck running", "node-a", nil)
	if err != nil {
		t.Fatalf("create running: %v", err)
	}
	if err := st.Queue(ctx, running.TaskID, "node-a"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := st.Dispatch(ctx, running.TaskID, "node-a", "node-a"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := st.Accept(ctx, running.TaskID, "node-a"); err != nil {
		t.Fatalf("accept: %v", err)
	}
	backdate(running.TaskID, now-30*60, now-60) // lease expired a minute ago

	// A plan-blocked stage waits on its graph by design — not a stall.
	stage, err := st.Create(ctx, "", "", "blocked stage", "node-a", nil)
	if err != nil {
		t.Fatalf("create stage: %v", err)
	}
	if err := st.SetStage(ctx, stage.TaskID, "plan-1", "b", []string{"a"}); err != nil {
		t.Fatalf("set stage: %v", err)
	}
	backdate(stage.TaskID, stale, 0)

	// A review parked so long it should remind again.
	review, err := st.Create(ctx, "", "", "parked review", "node-a", nil)
	if err != nil {
		t.Fatalf("create review: %v", err)
	}
	if err := st.Queue(ctx, review.TaskID, "node-a"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := st.Dispatch(ctx, review.TaskID, "node-a", "node-a"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := st.Accept(ctx, review.TaskID, "node-a"); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := st.Fail(ctx, review.TaskID, "node-a", "tier-2 refusal"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if err := st.ReviewWithDisposition(ctx, review.TaskID, "node-a", "needs authorization", core.ApprovalResumeExecution); err != nil {
		t.Fatalf("review: %v", err)
	}
	backdate(review.TaskID, now-40*60, 0)

	r := &repl{store: st}
	r.resetWatchBaseline()

	events := r.pollCompletions(ctx)
	byTask := make(map[string]watchEvent)
	for _, ev := range events {
		byTask[ev.task.TaskID] = ev
	}
	if ev, ok := byTask[queued.TaskID]; !ok || ev.review {
		t.Fatalf("queued stall should be announced as a plain note: %+v", byTask)
	}
	if ev, ok := byTask[running.TaskID]; !ok || ev.review {
		t.Fatalf("lease-overdue task should be announced: %+v", byTask)
	}
	if ev, ok := byTask[review.TaskID]; !ok || !ev.review {
		t.Fatalf("a long-parked review should re-remind as an approval event: %+v", byTask)
	}
	if _, ok := byTask[stage.TaskID]; ok {
		t.Fatalf("a dependency-blocked plan stage is not a stall: %+v", byTask)
	}

	// Stall notes fire once per (task, state): the very next poll is silent.
	if events := r.pollCompletions(ctx); len(events) != 0 {
		t.Fatalf("stall warnings must not repeat every poll: %v", events)
	}
}

// TestWatcherSilentDuringAsk confirms the in-flight turn owns the reporting: while
// an ask is running the poll neither speaks nor moves the baseline, so the
// completion is still absorbed by the turn that caused it.
func TestWatcherSilentDuringAsk(t *testing.T) {
	r := &repl{}
	if watchTasks(r) != nil {
		t.Error("no store means nothing to watch")
	}
	r.setAsking(true)
	if !r.askingNow() {
		t.Error("setAsking(true) should be visible to the watcher")
	}
	m := newTestTUI(t)
	m.r.setAsking(true)
	next, _ := m.commit(nil)
	if m.r.askingNow() {
		t.Error("committing the turn should release the watcher")
	}
	if next.(tuiModel).liveTask != nil {
		t.Error("commit should clear the live card")
	}
}

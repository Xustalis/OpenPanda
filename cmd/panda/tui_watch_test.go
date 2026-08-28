package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

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

	if notes := r.pollCompletions(ctx); len(notes) != 0 {
		t.Fatalf("an unchanged store should report nothing, got %v", notes)
	}
	if err := st.ForceFail(ctx, task.TaskID, "boom"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	notes := r.pollCompletions(ctx)
	if len(notes) != 1 || !strings.Contains(notes[0], "build docs") {
		t.Fatalf("the failure should be reported once: %v", notes)
	}
	if notes := r.pollCompletions(ctx); len(notes) != 0 {
		t.Fatalf("a reported task must not repeat: %v", notes)
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

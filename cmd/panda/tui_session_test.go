package main

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// newSessionTUI builds a TUI whose repl owns a throwaway session store, so the
// binding tests never touch the user's real CLI state directory.
func newSessionTUI(t *testing.T) (tuiModel, *sessions.Store) {
	t.Helper()
	st := sessions.NewStore(t.TempDir())
	r := &repl{loc: i18n.Locale("en"), cfg: &config.Config{}, interactive: true, sessionsSt: st}
	return newTUIModel(r), st
}

// TestHistoryBareModeReplaysConvo checks that with no session bound the TUI
// replays this run's in-memory conversation and asks for no worktree.
func TestHistoryBareModeReplaysConvo(t *testing.T) {
	m, _ := newSessionTUI(t)
	m.r.convo = append(m.r.convo, entry.Turn{Role: "user", Content: "hi"}, entry.Turn{Role: "assistant", Content: "hello"})

	history, workDir := m.history("what now")
	if len(history) != 2 || history[0].Content != "hi" {
		t.Fatalf("bare history = %+v", history)
	}
	if workDir != "" {
		t.Errorf("bare mode should not pin a worktree, got %q", workDir)
	}
	// The user's turn is only paired with its answer at commit time in bare mode,
	// so asking for context must not have grown the convo.
	if len(m.r.convo) != 2 {
		t.Errorf("bare mode should not record the turn early: %+v", m.r.convo)
	}
}

// TestHistoryBoundSessionReplaysThread verifies that a session bound by /resume
// governs the TUI's turns: the prompt is appended to the thread, the whole thread
// comes back as history, and the session's worktree becomes the working dir.
func TestHistoryBoundSessionReplaysThread(t *testing.T) {
	m, st := newSessionTUI(t)
	sess, err := st.Create("ppo")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := st.AppendTurn(sess.ID, sessions.Turn{Role: "user", Text: "earlier question"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := st.AppendTurn(sess.ID, sessions.Turn{Role: "assistant", Text: "earlier answer"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	m.r.activeSess = sess.ID

	history, _ := m.history("follow-up")
	if len(history) != 3 {
		t.Fatalf("expected the thread plus the new turn, got %d: %+v", len(history), history)
	}
	if history[0].Content != "earlier question" || history[2].Content != "follow-up" {
		t.Fatalf("thread replayed out of order: %+v", history)
	}
	// The prompt was persisted, not just replayed.
	got, err := st.Get(sess.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Turns) != 3 || got.Turns[2].Role != "user" {
		t.Fatalf("prompt not recorded in the thread: %+v", got.Turns)
	}
}

// TestHistoryStaleSessionFallsBack confirms a session id whose file is gone does
// not break the turn: the TUI drops silently back to bare mode.
func TestHistoryStaleSessionFallsBack(t *testing.T) {
	m, _ := newSessionTUI(t)
	m.r.activeSess = "deadbeefdeadbeef"
	m.r.convo = append(m.r.convo, entry.Turn{Role: "user", Content: "hi"})

	history, workDir := m.history("q")
	if m.r.activeSess != "" {
		t.Error("a stale session id should be cleared")
	}
	if len(history) != 1 || history[0].Content != "hi" {
		t.Fatalf("should fall back to bare convo, got %+v", history)
	}
	if workDir != "" {
		t.Errorf("workDir = %q, want empty", workDir)
	}
}

// TestCommitPersistsIntoSession checks the assistant side of a bound turn lands
// in the thread — an answer as prose, a delegated task as its id — rather than in
// the bare in-memory convo.
func TestCommitPersistsIntoSession(t *testing.T) {
	m, st := newSessionTUI(t)
	sess, err := st.Create("thread")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	m.r.activeSess = sess.ID
	m.pendingPrompt = "explain PPO"

	next, _ := m.commit(&askengine.Result{Kind: "answer", OK: true, Answer: "PPO is…"})
	m = next.(tuiModel)

	got, err := st.Get(sess.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Turns) != 1 || got.Turns[0].Role != "assistant" || got.Turns[0].Text != "PPO is…" {
		t.Fatalf("answer not persisted into the thread: %+v", got.Turns)
	}
	if len(m.r.convo) != 0 {
		t.Errorf("a bound turn must not also land in the bare convo: %+v", m.r.convo)
	}

	// A task turn stores its id as the turn's ref so the console can deep-link it.
	m.pendingPrompt = "build docs"
	next, _ = m.commit(&askengine.Result{Kind: "task", OK: true, TaskID: "t-1", Stdout: "done\n"})
	m = next.(tuiModel)
	got, err = st.Get(sess.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Turns) != 2 || got.Turns[1].Ref != "t-1" || got.Turns[1].Kind != "task" {
		t.Fatalf("task turn not linked: %+v", got.Turns)
	}
}

// TestContextLineReportsBinding checks the state row above the input tells the
// user which thread the next prompt joins — binding the session is only useful
// if it is visible.
func TestContextLineReportsBinding(t *testing.T) {
	m, st := newSessionTUI(t)
	if got := m.contextLine(); !strings.Contains(got, "new chat") {
		t.Errorf("fresh run should read as a new chat: %q", got)
	}
	m.r.convo = append(m.r.convo, entry.Turn{Role: "user", Content: "hi"}, entry.Turn{Role: "assistant", Content: "yo"})
	if got := m.contextLine(); !strings.Contains(got, "1 turns") {
		t.Errorf("bare chat should report its turn count: %q", got)
	}
	sess, err := st.Create("thread")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	m.r.activeSess = sess.ID
	if got := m.contextLine(); !strings.Contains(got, shortID(sess.ID)) {
		t.Errorf("bound session should be named: %q", got)
	}
	m.r.authorize = true
	if got := m.contextLine(); !strings.Contains(strings.ToLower(got), "on") {
		t.Errorf("standing authorization should be visible: %q", got)
	}
}

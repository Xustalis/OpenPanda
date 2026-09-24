//go:build !lite

package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	tea "github.com/charmbracelet/bubbletea"
)

// newIsolatedTUI builds a TUI whose convo persistence lands in a throwaway
// XDG_STATE_HOME, so loadConvo/saveConvo/clearConvo never touch real CLI state.
func newIsolatedTUI(t *testing.T) tuiModel {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	old := cliConfigPath
	cliConfigPath = filepath.Join(t.TempDir(), "missing-config.yaml")
	t.Cleanup(func() { cliConfigPath = old })
	r := &repl{loc: i18n.Locale("en"), cfg: &config.Config{}, interactive: true}
	m := newTUIModel(r)
	m.mode = modeIdle
	m.width, m.height = 100, 30
	return m
}

// pumpExec drains one command execution's event stream through Update until
// its terminal execDoneMsg, returning the final model.
func pumpExec(t *testing.T, m tuiModel, cmd tea.Cmd) tuiModel {
	t.Helper()
	for i := 0; i < 8192 && cmd != nil; i++ {
		msg := cmd()
		switch msg := msg.(type) {
		case execOutputMsg:
			next, c := m.Update(msg)
			m = next.(tuiModel)
			cmd = c
		case execDoneMsg:
			next, _ := m.Update(msg)
			m = next.(tuiModel)
			cmd = nil
		default:
			cmd = nil
		}
	}
	if cmd != nil {
		t.Fatal("exec pump did not reach its terminal event")
	}
	return m
}

// submitAndPump submits one line and, when it enters exec mode, pumps the
// command to completion — the full round trip a typed line makes.
func submitAndPump(t *testing.T, m tuiModel, text string) tuiModel {
	t.Helper()
	next, cmd := m.submit(text)
	m = next.(tuiModel)
	if m.mode == modeExec && cmd != nil {
		m = pumpExec(t, m, cmd)
	}
	return m
}

func lastBlock(m tuiModel) block {
	if m.chatHistory == nil || len(m.chatHistory.blocks) == 0 {
		return block{}
	}
	return m.chatHistory.blocks[len(m.chatHistory.blocks)-1]
}

// TestTUIClearIsVisualOnly pins the /clear contract: the transcript empties
// and the screen repaints, but the conversation — the context the next ask
// replays — survives. The wipe is /new's job.
func TestTUIClearIsVisualOnly(t *testing.T) {
	m := newIsolatedTUI(t)
	m.r.convo = append(m.r.convo,
		entry.Turn{Role: "user", Content: "hi"},
		entry.Turn{Role: "assistant", Content: "hello"})
	if m.chatHistory != nil {
		m.chatHistory.blocks = append(m.chatHistory.blocks, block{kind: blockUser, body: "hi"})
	}

	for _, alias := range []string{"/clear", "/cls", "/claer"} {
		m.chatHistory.blocks = append(m.chatHistory.blocks, block{kind: blockInfo, body: "x"})
		next, cmd := m.submit(alias)
		m = next.(tuiModel)
		if len(m.chatHistory.blocks) != 0 {
			t.Fatalf("%s should empty the transcript, got %d blocks", alias, len(m.chatHistory.blocks))
		}
		if len(m.r.convo) != 2 {
			t.Fatalf("%s must not wipe the conversation, convo=%v", alias, m.r.convo)
		}
		if cmd == nil {
			t.Fatalf("%s should return tea.ClearScreen", alias)
		}
	}
}

// TestTUINewWipesConvoAndFile covers /new: convo, the persisted file, and the
// transcript all reset — and the count note reports what was dropped.
func TestTUINewWipesConvoAndFile(t *testing.T) {
	m := newIsolatedTUI(t)
	saveConvo([]entry.Turn{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	})
	m.r.convo = loadConvo()
	if len(m.r.convo) != 2 {
		t.Fatalf("seed convo not persisted: %v", m.r.convo)
	}

	next, cmd := m.submit("/new")
	m = next.(tuiModel)
	if len(m.r.convo) != 0 {
		t.Fatalf("/new must clear the in-memory convo: %v", m.r.convo)
	}
	if got := loadConvo(); len(got) != 0 {
		t.Fatalf("/new must clear the persisted convo file: %v", got)
	}
	if len(m.chatHistory.blocks) == 0 {
		t.Fatal("/new should leave its cleared-note in the transcript")
	}
	if cmd == nil {
		t.Fatal("/new should batch the repaint with the note")
	}

	// With a bound session /new refuses, mirroring the classic guard.
	st := sessions.NewStore(t.TempDir())
	sess, err := st.Create("thread")
	if err != nil {
		t.Fatal(err)
	}
	m.r.sessionsSt = st
	m.r.activeSess = sess.ID
	m.r.convo = append(m.r.convo, entry.Turn{Role: "user", Content: "keep"})
	next, _ = m.submit("/new")
	m = next.(tuiModel)
	if len(m.r.convo) != 1 {
		t.Fatal("/new with a bound session must not clear the bare convo")
	}
	if got := lastBlock(m); !strings.Contains(got.body, "session") {
		t.Fatalf("/new with a bound session should explain the refusal, got %q", got.body)
	}
}

// TestTUIHistoryFallsBackToDisk is the reported bug: /history showed nothing
// because it only read the in-memory convo. With exchanges on disk and an
// empty memory the listing must still render them.
func TestTUIHistoryFallsBackToDisk(t *testing.T) {
	m := newIsolatedTUI(t)
	saveConvo([]entry.Turn{
		{Role: "user", Content: "earlier question"},
		{Role: "assistant", Content: "earlier answer"},
	})
	// r.convo is empty — as after a restart that never loaded the file.
	m = submitAndPump(t, m, "/history")
	if m.mode != modeIdle {
		t.Fatalf("/history should land back in idle, got %v", m.mode)
	}
	found := false
	for _, b := range m.chatHistory.blocks {
		if strings.Contains(b.body, "earlier question") {
			found = true
		}
	}
	if !found {
		t.Fatalf("/history must surface persisted turns, blocks=%v", m.chatHistory.blocks)
	}
}

// TestTUIHistoryBoundSessionListsThread verifies /history with a bound
// session prints the session's own thread rather than brushing the user off
// to the session commands.
func TestTUIHistoryBoundSessionListsThread(t *testing.T) {
	m := newIsolatedTUI(t)
	st := sessions.NewStore(t.TempDir())
	sess, err := st.Create("thread")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = st.AppendTurn(sess.ID, sessions.Turn{Role: "user", Text: "session question"})
	_, _ = st.AppendTurn(sess.ID, sessions.Turn{Role: "assistant", Text: "session answer"})
	m.r.sessionsSt = st
	m.r.activeSess = sess.ID

	m = submitAndPump(t, m, "/history")
	found := false
	for _, b := range m.chatHistory.blocks {
		if strings.Contains(b.body, "session question") {
			found = true
		}
	}
	if !found {
		t.Fatalf("/history must list the bound session's thread, blocks=%v", m.chatHistory.blocks)
	}
}

// TestTUIInvalidCommandAfterClear is the reported crash: /clear, a few
// interactions, then a mistyped command — the TUI must land back in idle with
// the unknown-command text as an ordinary transcript block.
func TestTUIInvalidCommandAfterClear(t *testing.T) {
	m := newIsolatedTUI(t)

	m = submitAndPump(t, m, "/clear")
	m = submitAndPump(t, m, "/help")
	m = submitAndPump(t, m, "/history")
	m = submitAndPump(t, m, "/frobnicate")

	if m.mode != modeIdle {
		t.Fatalf("invalid command must return to idle, got mode %v", m.mode)
	}
	var sawUnknown bool
	for _, b := range m.chatHistory.blocks {
		if strings.Contains(b.body, "unknown command") {
			sawUnknown = true
		}
	}
	if !sawUnknown {
		t.Fatalf("unknown command text should commit as a block, got %v", m.chatHistory.blocks)
	}
	// The frame must still render — a corrupted View would fail here.
	if v := m.View(); !strings.Contains(v, "unknown command") {
		t.Fatalf("View after the sequence should render the unknown-command block:\n%s", v)
	}

	// A few more degenerate inputs must all survive.
	for _, bad := range []string{"/", "///", "/clear junk", "/new x", "/resume", "!!"} {
		m = submitAndPump(t, m, bad)
		if m.mode != modeIdle && m.mode != modeList && m.mode != modeAsking {
			t.Fatalf("%q left the model in mode %v", bad, m.mode)
		}
	}
}

// TestTUIExecPanicSurfacesAsError proves a panicking handler cannot take the
// process down or wedge the pump: it surfaces as an error block in idle.
func TestTUIExecPanicSurfacesAsError(t *testing.T) {
	m := newIsolatedTUI(t)
	// /tasks with no store dereferences nil inside cmdTasks — the panic path
	// every exec'd command shares.
	m.r.store = nil
	m = submitAndPump(t, m, "/tasks")
	if m.mode != modeIdle {
		t.Fatalf("panicked exec must land back in idle, got %v", m.mode)
	}
	var sawErr bool
	for _, b := range m.chatHistory.blocks {
		if b.kind == blockError {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatalf("a panicked handler should surface an error block, blocks=%v", m.chatHistory.blocks)
	}
}

// TestTUIExecOutputSanitized feeds a completion carrying terminal control
// sequences — what the old /clear wrote straight to stdout — and checks the
// committed block holds only text.
func TestTUIExecOutputSanitized(t *testing.T) {
	m := newIsolatedTUI(t)
	m.mode = modeExec
	m.execGen = 1
	exec := newCommandExec(1)
	m.exec = exec
	next, _ := m.Update(execDoneMsg{
		exec:       exec,
		generation: 1,
		text:       "/fake",
		output:     "\x1b[2J\x1b[3J\x1b[Hplain output\x1b[0m",
	})
	m = next.(tuiModel)
	for _, b := range m.chatHistory.blocks {
		if strings.ContainsRune(b.body, '\x1b') {
			t.Fatalf("committed block still carries an escape: %q", b.body)
		}
	}
	if !strings.Contains(lastBlock(m).body, "plain output") {
		t.Fatalf("sanitized output lost its text: %q", lastBlock(m).body)
	}
}

// TestTUIResumeDoesNotLeakThreadIntoBareConvo covers the convo leak: binding
// a session must not copy its turns into the bare convo, or detaching would
// replay the thread as bare history and persist it into the bare file.
func TestTUIResumeDoesNotLeakThreadIntoBareConvo(t *testing.T) {
	m := newIsolatedTUI(t)
	m.r.convo = append(m.r.convo, entry.Turn{Role: "user", Content: "bare q"}, entry.Turn{Role: "assistant", Content: "bare a"})
	st := sessions.NewStore(t.TempDir())
	m.r.sessionsSt = st
	sess, err := st.Create("thread")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = st.AppendTurn(sess.ID, sessions.Turn{Role: "user", Text: "thread q"})
	_, _ = st.AppendTurn(sess.ID, sessions.Turn{Role: "assistant", Text: "thread a"})

	next, _ := m.submit("/resume " + sess.ID)
	m = next.(tuiModel)
	if m.r.activeSess != sess.ID {
		t.Fatalf("/resume should bind the session, activeSess=%q", m.r.activeSess)
	}
	if len(m.r.convo) != 2 || m.r.convo[0].Content != "bare q" {
		t.Fatalf("binding must not inject thread turns into the bare convo: %v", m.r.convo)
	}
	// The transcript shows the thread.
	var sawThread bool
	for _, b := range m.chatHistory.blocks {
		if b.body == "thread q" {
			sawThread = true
		}
	}
	if !sawThread {
		t.Fatal("attached session should replay its thread into the transcript")
	}

	next, _ = m.submit("/resume -")
	m = next.(tuiModel)
	if m.r.activeSess != "" {
		t.Fatal("/resume - should detach")
	}
	if len(m.r.convo) != 2 || m.r.convo[0].Content != "bare q" {
		t.Fatalf("detaching must restore the bare convo untouched: %v", m.r.convo)
	}
}

// TestTUIFailedTurnPersistsPair covers recordErrorTurn's bare-mode pairing:
// a failed ask still writes its user+assistant pair to the convo file, so
// /history and the next run can see the attempt instead of losing it.
func TestTUIFailedTurnPersistsPair(t *testing.T) {
	m := newIsolatedTUI(t)
	m.r.recordErrorTurn("what is this", errors.New("model exploded"))
	if len(m.r.convo) != 2 || m.r.convo[0].Role != "user" || m.r.convo[0].Content != "what is this" {
		t.Fatalf("failed turn should persist the user side: %v", m.r.convo)
	}
	if !strings.HasPrefix(m.r.convo[1].Content, "⚠") {
		t.Fatalf("failed turn should persist a marked assistant side: %v", m.r.convo)
	}
	got := loadConvo()
	if len(got) != 2 || got[0].Content != "what is this" {
		t.Fatalf("failed turn should reach the persisted file: %v", got)
	}
}

// TestTUIAuthorizeAndTasksClearGuards checks the two commands that mutated or
// needed a terminal through the exec path: /authorize toggles inline and
// /tasks clear refuses with a pointer to the shell.
func TestTUIAuthorizeAndTasksClearGuards(t *testing.T) {
	m := newIsolatedTUI(t)

	next, _ := m.submit("/authorize")
	m = next.(tuiModel)
	if !m.r.authorize {
		t.Fatal("/authorize should flip the standing-consent flag")
	}
	if m.mode != modeIdle {
		t.Fatalf("/authorize must stay inline (no exec), got %v", m.mode)
	}
	next, _ = m.submit("/authorize")
	m = next.(tuiModel)
	if m.r.authorize {
		t.Fatal("second /authorize should flip the flag back")
	}

	m = submitAndPump(t, m, "/tasks clear")
	if m.mode != modeIdle {
		t.Fatalf("/tasks clear must not enter exec, got %v", m.mode)
	}
	if got := lastBlock(m); !strings.Contains(got.body, "panda queue clear") {
		t.Fatalf("/tasks clear should point at the shell command, got %q", got.body)
	}
}

// TestTUIAskAndModeSlashGoToEngine covers the ask-shaped slash commands:
// /ask and /goal|/plan|/spec take the TUI's ask path (never the classic r.ask
// which grabs the raw terminal), and bare forms print their usage line.
func TestTUIAskAndModeSlashGoToEngine(t *testing.T) {
	m := newIsolatedTUI(t)

	next, _ := m.submit("/ask")
	m = next.(tuiModel)
	if got := lastBlock(m); got.kind != blockNote || !strings.Contains(got.body, "/ask") {
		t.Fatalf("bare /ask should print usage, got %+v", got)
	}
	next, _ = m.submit("/goal")
	m = next.(tuiModel)
	if got := lastBlock(m); got.kind != blockNote {
		t.Fatalf("bare /goal should print usage, got %+v", got)
	}

	// With no engine the turn lands as the no-model error — importantly it
	// took askTurn (modeAsking is never reached without an engine), not the
	// exec path that would run the classic interrupt-watched ask.
	next, _ = m.submit("/ask what is up")
	m = next.(tuiModel)
	if m.mode != modeIdle {
		t.Fatalf("/ask without an engine should note the failure inline, got %v", m.mode)
	}
	if got := lastBlock(m); got.kind != blockError {
		t.Fatalf("/ask without an engine should surface blockError, got %+v", got)
	}
	next, _ = m.submit("/goal\nship the fix")
	m = next.(tuiModel)
	if got := lastBlock(m); got.kind != blockError {
		t.Fatalf("multiline /goal should reach askTurn, got %+v", got)
	}
}

// TestTUIRepeatLast checks "!!": it replays the last user prompt through
// askTurn (here surfacing the no-model error) and reports when there is none.
func TestTUIRepeatLast(t *testing.T) {
	m := newIsolatedTUI(t)
	next, _ := m.submit("!!")
	m = next.(tuiModel)
	if got := lastBlock(m); got.kind != blockNote || !strings.Contains(got.body, "nothing") {
		t.Fatalf("!! with no history should note it, got %+v", got)
	}
	m.r.convo = append(m.r.convo,
		entry.Turn{Role: "user", Content: "do it again"},
		entry.Turn{Role: "assistant", Content: "done"})
	next, _ = m.submit("!!")
	m = next.(tuiModel)
	// The replayed prompt routed into askTurn: with no engine that surfaces
	// the no-model error rather than another "nothing to repeat" note.
	if got := lastBlock(m); got.kind != blockError {
		t.Fatalf("!! should re-submit the last prompt through askTurn, got %+v", got)
	}
}

// TestTUIQuitWithArgs ensures /quit takes effect even with trailing text —
// the classic dispatcher normalizes it to the same handler.
func TestTUIQuitWithArgs(t *testing.T) {
	m := newIsolatedTUI(t)
	next, cmd := m.submit("/quit now")
	if !next.(tuiModel).quitting || !isQuit(cmd) {
		t.Fatal("/quit now should quit")
	}
	next, cmd = m.submit("/exit")
	if !next.(tuiModel).quitting || !isQuit(cmd) {
		t.Fatal("/exit should quit")
	}
}

// TestConvoRoundTripThroughRestart simulates the "messages are not stored"
// complaint end to end: an exchange saved under one run's state dir loads
// back under the same dir, which is what a restarted process sees.
func TestConvoRoundTripThroughRestart(t *testing.T) {
	_ = newIsolatedTUI(t) // fixes XDG_STATE_HOME + cliConfigPath
	saveConvo([]entry.Turn{
		{Role: "user", Content: "remember me"},
		{Role: "assistant", Content: "noted"},
	})
	got := loadConvo()
	if len(got) != 2 || got[0].Content != "remember me" {
		t.Fatalf("persisted convo must reload, got %v", got)
	}
}

// TestTUIResumeNotFound verifies a bad session id reports rather than binds.
func TestTUIResumeNotFound(t *testing.T) {
	m := newIsolatedTUI(t)
	m.r.sessionsSt = sessions.NewStore(t.TempDir())
	next, _ := m.submit("/resume deadbeef")
	m = next.(tuiModel)
	if m.r.activeSess != "" {
		t.Fatal("a missing session id must not bind")
	}
	if got := lastBlock(m); got.kind != blockError {
		t.Fatalf("a missing session id should be an error block, got %+v", got)
	}
}

// TestTUISubmitEmptyAndWhitespace guards the degenerate submits.
func TestTUISubmitEmptyAndWhitespace(t *testing.T) {
	m := newIsolatedTUI(t)
	for _, in := range []string{"", "   ", "\n"} {
		next, cmd := m.submit(in)
		m = next.(tuiModel)
		if m.mode != modeIdle || cmd != nil {
			t.Fatalf("empty submit %q must be a no-op, mode=%v", in, m.mode)
		}
	}
	if len(m.chatHistory.blocks) != 0 {
		t.Fatalf("empty submits must not touch the transcript: %v", m.chatHistory.blocks)
	}
}

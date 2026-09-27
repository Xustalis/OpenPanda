//go:build !lite

package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/storage"
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
	r.cfg.Storage.SkillsPath = t.TempDir() // /skills hub must not seed builtins into cwd
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
// /tasks clear goes through the TUI's own confirm card — never a silent wipe,
// never a "go run this in a shell" dead end.
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

	// An empty board never raises the card — it reports "nothing to do".
	m = submitAndPump(t, m, "/tasks clear")
	if m.mode != modeIdle {
		t.Fatalf("/tasks clear on an empty board must not enter exec/confirm, got %v", m.mode)
	}
	if got := lastBlock(m); !strings.Contains(got.body, "empty") {
		t.Fatalf("/tasks clear on an empty board should say so, got %q", got.body)
	}

	// With a task on the board, /tasks clear raises the confirm card.
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := core.NewTaskStore(db, nil)
	if _, err := st.Create(context.Background(), "", "", "wipe me", "node-a", nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	m.r.store = st

	next, _ = m.submit("/tasks clear")
	m = next.(tuiModel)
	if m.mode != modeConfirm {
		t.Fatalf("/tasks clear should raise the confirm card, got %v", m.mode)
	}
	if v := m.View(); !strings.Contains(v, "1") || !strings.Contains(v, "[y]") {
		t.Fatalf("confirm card should name the task count and the keys:\n%s", v)
	}

	// 'n' declines: the task survives and the card notes the cancellation.
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if m.mode != modeIdle {
		t.Fatalf("declining should return to idle, got %v", m.mode)
	}
	if n, _ := st.ListByState(context.Background(), ""); len(n) != 1 {
		t.Fatal("declined clear must not delete the task")
	}

	// 'y' runs the wipe through the exec pump; the board ends empty.
	next, _ = m.submit("/tasks clear")
	m = next.(tuiModel)
	if m.mode != modeConfirm {
		t.Fatalf("/tasks clear should raise the confirm card again, got %v", m.mode)
	}
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if m.mode != modeExec || m.exec == nil {
		t.Fatalf("confirming should start the exec, got mode=%v", m.mode)
	}
	m = pumpExec(t, m, waitForExec(m.exec))
	if m.mode != modeIdle {
		t.Fatalf("the wipe should land back in idle, got %v", m.mode)
	}
	if n, _ := st.ListByState(context.Background(), ""); len(n) != 0 {
		t.Fatalf("confirmed clear should empty the board, %d tasks left", len(n))
	}

	// The --yes form answers ahead of time and skips the card entirely.
	if _, err := st.Create(context.Background(), "", "", "wipe me too", "node-a", nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	m = submitAndPump(t, m, "/tasks clear --yes")
	if m.mode != modeIdle {
		t.Fatalf("/tasks clear --yes should run and settle, got %v", m.mode)
	}
	if n, _ := st.ListByState(context.Background(), ""); len(n) != 0 {
		t.Fatalf("/tasks clear --yes should empty the board, %d tasks left", len(n))
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

// TestEverySlashCommandResponds sweeps the whole replCommands table (plus the
// TUI aliases) through submit: a registered command must never be a dead end —
// it either returns a cmd, changes mode (a panel/confirm opened), or quits.
// Exec-routed commands are additionally pumped and must leave real output in
// the transcript (a handler that produces nothing is indistinguishable from
// broken). "/web" is excluded: it binds a real loopback port.
func TestEverySlashCommandResponds(t *testing.T) {
	seen := map[string]bool{}
	var names []string
	for _, c := range replCommands {
		if !seen[c.name] {
			seen[c.name] = true
			names = append(names, c.name)
		}
	}
	names = append(names, "exit", "cls", "v", "ver", "bogus") // TUI aliases + unknown
	skipPump := map[string]bool{"web": true, "quit": true, "exit": true}

	for _, name := range names {
		m := newIsolatedTUI(t)
		next, cmd := m.submit("/" + name)
		m = next.(tuiModel)
		switch {
		case name == "quit" || name == "exit":
			if !m.quitting {
				t.Errorf("/%s must set quitting", name)
			}
			continue
		case cmd == nil && m.mode == modeIdle:
			t.Errorf("/%s is a dead command: no cmd, mode still idle", name)
			continue
		}
		if m.mode != modeExec || skipPump[name] {
			continue
		}
		before := len(m.chatHistory.blocks)
		m = pumpExec(t, m, cmd)
		if m.mode != modeIdle {
			t.Errorf("/%s left the model in mode %v", name, m.mode)
		}
		// The pump commits the user echo plus output/error. Only an echo means
		// the handler ran but produced nothing — a dead command in practice.
		var infoBlocks int
		for _, b := range m.chatHistory.blocks[before:] {
			if b.kind == blockInfo || b.kind == blockError || b.kind == blockNote {
				infoBlocks++
			}
		}
		if infoBlocks == 0 {
			t.Errorf("/%s produced no output in the transcript", name)
		}
	}
}

// TestTasksWatchRoutesToBoard pins the /tasks watch routing fix: the "watch"
// token must reach cmdTasks, so the exec streams frames until Esc cancels —
// not return a one-shot listing. And on exit the transcript keeps the last
// drawn frame, not every 2s snapshot concatenated.
func TestTasksWatchRoutesToBoard(t *testing.T) {
	m := newIsolatedTUI(t)
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := core.NewTaskStore(db, nil)
	if _, err := st.Create(context.Background(), "", "", "watchable task", "node-a", nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	m.r.store = st

	next, cmd := m.submit("/tasks watch")
	m = next.(tuiModel)
	if m.mode != modeExec || m.exec == nil || cmd == nil {
		t.Fatalf("/tasks watch should start a streaming exec, got mode=%v exec=%v", m.mode, m.exec)
	}
	// The board's first frame arrives as execOutputMsg chunks (one per Write) —
	// a one-shot listing (the swallowed-token bug) would send execDoneMsg and
	// stop. Drain chunks until the frame's task row is in the buffer.
	for i := 0; i < 64; i++ {
		msg := cmd()
		switch msg.(type) {
		case execOutputMsg:
			next, cmd = m.Update(msg)
			m = next.(tuiModel)
			if strings.Contains(m.exec.text(), "watchable task") {
				goto frameReady
			}
		case execDoneMsg:
			t.Fatalf("watch ended without a frame containing the task: %q", m.exec.text())
		default:
			t.Fatalf("unexpected watch event %T", msg)
		}
	}
	t.Fatalf("watch frame never contained the task: %q", m.exec.text())
frameReady:
	// Esc cancels the board; the pump drains to execDoneMsg.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(tuiModel)
	m = pumpExec(t, m, cmd)
	if m.mode != modeIdle {
		t.Fatalf("watch should settle back to idle, got %v", m.mode)
	}
	// The committed block is the board's last frame once — folding, not
	// concatenating — plus the exit note.
	var board *block
	for i := range m.chatHistory.blocks {
		b := &m.chatHistory.blocks[i]
		if b.kind == blockInfo && strings.Contains(b.body, "watchable task") {
			board = b
		}
	}
	if board == nil {
		t.Fatalf("transcript should keep the final board, blocks=%+v", m.chatHistory.blocks)
	}
	if n := strings.Count(board.body, "watchable task"); n != 1 {
		t.Fatalf("watch commit should hold one frame, task appears %d times:\n%s", n, board.body)
	}
	if !strings.Contains(board.body, i18n.T(i18n.English, "cli.watch.exited")) {
		t.Fatalf("watch commit should keep the exited line:\n%s", board.body)
	}
}

// TestFrameFolding covers the pure helpers that cut a repaint stream down to
// its last frame.
func TestFrameFolding(t *testing.T) {
	// Ordinary output is untouched.
	if got := latestFrame("plain\noutput\n"); got != "plain\noutput\n" {
		t.Fatalf("latestFrame should pass plain output through, got %q", got)
	}
	if got := commitFrame("plain\noutput\n"); got != "plain\noutput\n" {
		t.Fatalf("commitFrame should pass plain output through, got %q", got)
	}
	// A repaint stream: latestFrame is the current frame; commitFrame keeps the
	// final drawn frame plus the exit tail.
	stream := "\x1b[2J\x1b[Hframe-one\x1b[J" + "\x1b[Hframe-two\x1b[J" + "\x1b[0m\x1b[H\x1b[Jexited\n"
	if got := latestFrame(stream); got != "\x1b[Jexited\n" {
		t.Fatalf("latestFrame should expose only the last segment, got %q", got)
	}
	got := commitFrame(stream)
	if !strings.Contains(got, "frame-two") || strings.Contains(got, "frame-one") || !strings.Contains(got, "exited") {
		t.Fatalf("commitFrame should fold to the last frame plus the exit tail, got %q", got)
	}
	// A lone marker is not a repaint stream — pass it through; ansi.Strip
	// removes the escape itself downstream.
	two := "\x1b[2J\x1b[Honly-frame"
	if got := commitFrame(two); got != two {
		t.Fatalf("commitFrame should pass a single-marker stream through, got %q", got)
	}
}

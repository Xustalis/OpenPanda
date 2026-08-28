package main

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// newTestTUI builds a model over a minimal engine-less repl — enough to drive
// the keystroke/layout logic that does not touch the ask engine.
func newTestTUI(t *testing.T) tuiModel {
	t.Helper()
	r := &repl{loc: i18n.Locale("en"), cfg: &config.Config{}, interactive: true}
	return newTUIModel(r)
}

// step runs one Update and returns the concrete model, discarding the command —
// used for transitions whose command we do not need to inspect.
func step(m tuiModel, msg tea.Msg) tuiModel {
	next, _ := m.Update(msg)
	return next.(tuiModel)
}

// TestTUISizingAndInput drives the idle path: a window size sets the layout, and
// typed runes land in the input without leaving idle mode.
func TestTUISizingAndInput(t *testing.T) {
	m := newTestTUI(t)
	m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	if !m.ready || m.width != 100 {
		t.Fatalf("window size not recorded: ready=%v width=%d", m.ready, m.width)
	}
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hi")})
	if m.ta.Value() != "hi" {
		t.Fatalf("input not captured: %q", m.ta.Value())
	}
	if m.mode != modeIdle {
		t.Fatalf("typing should stay idle, got mode %v", m.mode)
	}
}

// TestTUIThoughtToggle confirms Ctrl+O flips the global thought-fold state in any
// mode, since it is a display toggle rather than a submit action.
func TestTUIThoughtToggle(t *testing.T) {
	m := newTestTUI(t)
	if m.expandThought {
		t.Fatal("thought should start folded")
	}
	m = step(m, tea.KeyMsg{Type: tea.KeyCtrlO})
	if !m.expandThought {
		t.Fatal("ctrl+o should expand the thought")
	}
	m = step(m, tea.KeyMsg{Type: tea.KeyCtrlO})
	if m.expandThought {
		t.Fatal("ctrl+o should fold the thought again")
	}
}

// TestTUIQuitCommands verifies the slash quit shortcuts and Ctrl+C both request
// tea.Quit from idle.
func TestTUIQuitCommands(t *testing.T) {
	for _, cmdText := range []string{"/quit", "/exit"} {
		m := newTestTUI(t)
		m.ta.SetValue(cmdText)
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if !next.(tuiModel).quitting || !isQuit(cmd) {
			t.Fatalf("%s should quit", cmdText)
		}
	}
	m := newTestTUI(t)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !isQuit(cmd) {
		t.Fatal("ctrl+c should quit from idle")
	}
}

// isQuit reports whether a command is tea.Quit by executing it and inspecting
// the message it produces (tea.Quit returns a tea.QuitMsg).
func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// newTestStream builds an askStream usable outside a real ask: a cancel that
// records the call and a dropped channel that drop() can close.
func newTestStream(cancelled *bool) *askStream {
	return &askStream{
		cancel:   func() { *cancelled = true },
		dropped:  make(chan struct{}),
		detached: false,
	}
}

// TestTUIInterruptReleasesTurn pins what Esc/Ctrl-C during a turn actually does:
// it cancels the ask and returns to the prompt, and it marks the ask detached so
// the result that arrives later is dropped rather than committed twice. It
// notably does NOT claim to stop the work — the core owns a delegated task's
// lifetime, so releasing the front end only stops the waiting.
func TestTUIInterruptReleasesTurn(t *testing.T) {
	m := newTestTUI(t)
	cancelled := false
	m.mode = modeAsking
	m.stream = newTestStream(&cancelled)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	got := next.(tuiModel)

	if !cancelled {
		t.Fatal("the ask's context should be cancelled")
	}
	if got.mode != modeIdle {
		t.Fatalf("mode: got %v want modeIdle", got.mode)
	}
	if got.stream != nil {
		t.Fatal("the released turn should no longer hold a stream")
	}
	if !m.stream.detached {
		t.Fatal("the released ask must be marked detached, or its late result is committed a second time")
	}
	// Having been released, a further interrupt is an ordinary quit from idle.
	_, cmd := got.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !isQuit(cmd) {
		t.Fatal("a second interrupt after release should quit")
	}
}

// TestTUIInterruptTwiceQuits covers the case nothing can be released: a
// ResumeApproved re-run has no stream and runs under the engine's own context,
// so the first interrupt can only say so — but a second one inside the window
// still quits. Without this a wedged turn would trap the user in the program.
func TestTUIInterruptTwiceQuits(t *testing.T) {
	m := newTestTUI(t)
	m.mode = modeAsking // a re-run is in flight; there is no stream to release
	m.stream = nil

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	got := next.(tuiModel)
	if got.mode != modeAsking {
		t.Fatalf("an unstoppable re-run must not return to the prompt: mode=%v", got.mode)
	}
	if got.quitting {
		t.Fatal("one interrupt should not quit while work is in flight")
	}

	next, cmd := got.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !next.(tuiModel).quitting || !isQuit(cmd) {
		t.Fatal("a second interrupt inside the window should quit")
	}
}

// TestTUIDetachedResultIsDropped guards against double-reporting: a released ask
// still finishes, but the watcher announces it, so onDone must not commit it.
func TestTUIDetachedResultIsDropped(t *testing.T) {
	m := newTestTUI(t)
	s := newTestStream(new(bool))
	s.detached = true

	_, cmd := m.Update(doneMsg{
		stream: s,
		out:    &askengine.Result{Kind: "answer", Answer: "late"},
	})
	if cmd != nil {
		t.Fatalf("a detached outcome must not be committed: cmd=%v", cmd)
	}
}

// TestTUIDroppedStreamReleasesPump is a leak test. A pump parked on an ask the
// user released has nothing left to receive; if it stayed parked, every
// interrupt would strand a goroutine for the life of the process.
func TestTUIDroppedStreamReleasesPump(t *testing.T) {
	s := newTestStream(new(bool))
	s.events = make(chan tea.Msg, 1)
	pump := waitForActivity(s)
	s.drop()

	got := make(chan tea.Msg, 1)
	go func() { got <- pump() }()

	select {
	case msg := <-got:
		if _, ok := msg.(droppedMsg); !ok {
			t.Fatalf("a pump parked on a dropped ask should be released, got %T", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pump stayed parked after the ask was dropped — goroutine leak")
	}
}

// TestTUIDroppedStreamUnblocksSend is the other half of the leak test: an engine
// goroutine mid-send when the user releases the ask must unblock rather than
// park forever on a channel nobody drains.
func TestTUIDroppedStreamUnblocksSend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &askStream{
		events:  make(chan tea.Msg), // unbuffered: a send only lands if someone reads
		cancel:  cancel,
		dropped: make(chan struct{}),
	}
	s.drop() // cancels ctx, which is what an in-flight send selects against

	done := make(chan struct{})
	go func() {
		s.send(ctx, deltaMsg("late"))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a send stayed blocked after the ask was dropped — goroutine leak")
	}
}

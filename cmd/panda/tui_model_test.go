package main

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/cliui"
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

// TestTUIWelcomeWaitsForTheTerminalSize pins the fix for a banner that used to be
// drawn from a guess: Init must not print it (no size is known yet), and the
// first WindowSizeMsg must.
func TestTUIWelcomeWaitsForTheTerminalSize(t *testing.T) {
	m := newTestTUI(t)
	if m.ready {
		t.Fatal("a fresh model must not claim to know its size")
	}
	// The first size report is what prints the banner, and only the first.
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 52, Height: 30})
	if cmd == nil {
		t.Fatal("first WindowSizeMsg should print the welcome frame")
	}
	m = step(m, tea.WindowSizeMsg{Width: 52, Height: 30})
	if _, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40}); cmd != nil {
		t.Fatal("a resize must not reprint the welcome frame")
	}
}

// TestTUIWelcomeFitsItsTerminal is the visible half of the same bug. Each banner
// line is clipped rather than wrapped, so the frame is the same six rows (two
// borders, four facts) at every width — a wrapping banner grew a row at a time as
// the terminal narrowed, turning the greeting into most of a small screen.
func TestTUIWelcomeFitsItsTerminal(t *testing.T) {
	const frameRows = 6
	for _, width := range []int{40, 52, 80, 200} {
		m := newTestTUI(t)
		m.r.cfg.Node.Name = "XenithdeMacBook-Pro.local"
		m.r.cfg.Storage.WorkPath = "/Users/xenith/Library/Application Support/openpanda"
		m.r.cfg.Model.Model = "deepseek-v4-flash"
		m = step(m, tea.WindowSizeMsg{Width: width, Height: 30})
		lines := strings.Split(m.welcome(), "\n")
		if len(lines) != frameRows {
			t.Errorf("width %d: banner is %d rows, want %d", width, len(lines), frameRows)
		}
		for _, line := range lines {
			if w := cliui.DisplayWidth(line); w > width {
				t.Errorf("width %d: banner line is %d columns: %q", width, w, line)
			}
		}
	}
}

// TestTUIHintLineShedsHintsWhenNarrow: the legend must never wrap or be cut
// mid-word, and submit/quit are the two hints it may not drop.
func TestTUIHintLineShedsHintsWhenNarrow(t *testing.T) {
	full := newTestTUI(t)
	full = step(full, tea.WindowSizeMsg{Width: 120, Height: 40})
	if n := strings.Count(full.hintLine(), "·"); n != 3 {
		t.Fatalf("a wide terminal should show all four hints, separators=%d", n)
	}

	narrow := newTestTUI(t)
	narrow = step(narrow, tea.WindowSizeMsg{Width: 46, Height: 30})
	line := narrow.hintLine()
	if w := cliui.DisplayWidth(line); w > narrow.textWidth() {
		t.Fatalf("hint line is %d columns, budget %d: %q", w, narrow.textWidth(), line)
	}
	if !strings.Contains(line, "enter") || !strings.Contains(line, "ctrl+c") {
		t.Fatalf("submit and quit must survive: %q", line)
	}
}

// TestAnswerTextMarksAndHangs covers the transcript's readability fix: prose is
// marked like the user's own turn and its continuation lines hang under that
// marker instead of resetting to column zero.
func TestAnswerTextMarksAndHangs(t *testing.T) {
	th := newTheme(i18n.Locale("en"))
	out := answerText(th, "alpha beta gamma delta epsilon zeta eta theta", 24)
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected the body to wrap at 24 columns: %q", out)
	}
	if !strings.HasPrefix(lines[0], th.glyph("⏺", "*")) {
		t.Errorf("first line lost its marker: %q", lines[0])
	}
	for _, l := range lines[1:] {
		if !strings.HasPrefix(l, "  ") {
			t.Errorf("continuation line is not hung under the marker: %q", l)
		}
	}
	// A committed block lands in the terminal's own scrollback, so the wrapper's
	// block padding must not travel with it into anything the user copies out.
	for _, l := range lines {
		if strings.HasSuffix(l, " ") {
			t.Errorf("wrapped line kept trailing padding: %q", l)
		}
	}
}

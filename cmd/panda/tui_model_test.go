package main

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/charmbracelet/lipgloss"
)

// newTestTUI builds a model over a minimal engine-less repl — enough to drive
// the keystroke/layout logic that does not touch the ask engine.
func newTestTUI(t *testing.T) tuiModel {
	t.Helper()
	r := &repl{loc: i18n.Locale("en"), cfg: &config.Config{}, interactive: true}
	m := newTUIModel(r)
	m.mode = modeIdle
	return m
}

// step runs one Update and returns the concrete model, discarding the command —
// used for transitions whose command we do not need to inspect.
func step(m tuiModel, msg tea.Msg) tuiModel {
	next, _ := m.Update(msg)
	return next.(tuiModel)
}

// TestTUIModifiedEnter covers the "newline, not submit" gestures: kitty CSI-u
// Shift+Enter, Alt+Enter (ESC CR), and the Ctrl+J fallback — all must grow the
// textarea instead of submitting.
func TestTUIModifiedEnter(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.KeyMsg
	}{
		{"kitty shift+enter", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[13;2u")}},
		{"kitty ctrl+enter", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[13;4u")}},
		{"alt+enter", tea.KeyMsg{Type: tea.KeyEnter, Alt: true}},
		{"ctrl+j", tea.KeyMsg{Type: tea.KeyCtrlJ}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestTUI(t)
			m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
			m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("first")})
			m = step(m, tc.msg)
			if m.ta.Value() != "first\n" {
				t.Fatalf("%s should insert a newline, got %q", tc.name, m.ta.Value())
			}
			if m.ta.LineCount() != 2 {
				t.Fatalf("expected 2 lines after %s, got %d", tc.name, m.ta.LineCount())
			}
			// Box grew with the content.
			if got := m.ta.Height(); got != 2 {
				t.Fatalf("expected textarea height 2, got %d", got)
			}
		})
	}

	// A bare Enter still submits: with no model configured the submission is
	// swallowed into the wizard, but the buffer must clear rather than gain a
	// newline.
	m := newTestTUI(t)
	m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	if strings.HasSuffix(m.ta.Value(), "\n") {
		t.Fatalf("plain Enter must not insert a newline, got %q", m.ta.Value())
	}
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
		events:   make(chan tea.Msg, 256),
		cancel:   func() { *cancelled = true },
		dropped:  make(chan struct{}),
		steer:    make(chan string, 32),
		detached: false,
	}
}

// TestTUIInterruptReleasesTurn pins what Esc/Ctrl-C during a turn actually does:
// it cancels the ask and returns to the prompt, and it marks the ask detached so
// the result that arrives later is dropped rather than committed twice. Core
// decides task semantics from that cancellation: a claimed approval resume is
// terminalized, while independently scheduled work remains core-owned.
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

// TestTUIInterruptTwiceQuits covers the defensive case where a mode transition
// reports busy without installing a stream. The first interrupt can only say so,
// but a second one inside the window still quits, so a wedged transition cannot
// trap the user in the program.
func TestTUIInterruptTwiceQuits(t *testing.T) {
	m := newTestTUI(t)
	m.mode = modeAsking // defensive busy state with no stream to release
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

// TestTUIDetachedResultIsDropped guards against double-reporting: after the
// stream is released, onDone must not commit a late outcome over a newer turn.
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
		s.send(ctx, deltaMsg{stream: s, text: "late"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a send stayed blocked after the ask was dropped — goroutine leak")
	}
}

func TestTUIRejectsEveryStaleStreamEvent(t *testing.T) {
	m := newTestTUI(t)
	m.mode = modeAsking
	m.stream = newTestStream(new(bool))
	m.thought = []string{"current thought"}
	m.liveAnswerChunks = []string{"current answer"}
	m.liveAnswerBytes = len("current answer")
	m.note = "current note"
	stale := newTestStream(new(bool))

	cases := []tea.Msg{
		deltaMsg{stream: stale, text: "late answer"},
		reasoningMsg{stream: stale, text: "late thought"},
		progressMsg{stream: stale, progress: askengine.Progress{Kind: askengine.ProgressTask, Name: "late task"}},
		doneMsg{stream: stale, out: &askengine.Result{Kind: "answer", Answer: "late done"}},
		resumedMsg{stream: stale, out: &askengine.Result{Kind: "task", TaskID: "late", OK: true}},
	}
	for _, msg := range cases {
		next, cmd := m.Update(msg)
		got := next.(tuiModel)
		if cmd != nil || got.stream != m.stream || got.mode != modeAsking {
			t.Fatalf("stale %T changed the active turn: mode=%v stream=%p cmd=%v", msg, got.mode, got.stream, cmd)
		}
		if got.liveAnswerText() != "current answer" || strings.Join(got.thought, "|") != "current thought" || got.note != "current note" || got.liveTask != nil {
			t.Fatalf("stale %T mutated live state: answer=%q thought=%q note=%q task=%+v", msg, got.liveAnswerText(), got.thought, got.note, got.liveTask)
		}
	}
}

func TestTUISteeringQueueFullKeepsDraft(t *testing.T) {
	for _, useMouse := range []bool{false, true} {
		name := "keyboard"
		if useMouse {
			name = "mouse"
		}
		t.Run(name, func(t *testing.T) {
			m := newTestTUI(t)
			m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
			m.mode = modeAsking
			m.stream = newTestStream(new(bool))
			for i := 0; i < cap(m.stream.steer); i++ {
				m.stream.steer <- "queued"
			}
			m.ta.SetValue("keep this draft")

			var msg tea.Msg = tea.KeyMsg{Type: tea.KeyEnter}
			if useMouse {
				buttons := m.askingButtonRects()
				msg = tea.MouseMsg{X: buttons[1].x + buttons[1].w/2, Y: buttons[1].y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
			}
			next, cmd := m.Update(msg)
			got := next.(tuiModel)
			if got.ta.Value() != "keep this draft" || got.mode != modeAsking || got.stream != m.stream {
				t.Fatalf("full queue lost draft or turn: draft=%q mode=%v stream=%p", got.ta.Value(), got.mode, got.stream)
			}
			if strings.Contains(got.pendingPrompt, "keep this draft") || cmd == nil {
				t.Fatalf("rejected steer was recorded or error feedback missing: prompt=%q cmd=%v", got.pendingPrompt, cmd)
			}
		})
	}
}

func TestTUIApprovalResumeKeepsCardAndDropsStaleCompletion(t *testing.T) {
	m := newTestTUI(t)
	live := newTaskProgress("approved task", time.Now(), i18n.English)
	m.liveTask = live
	m.mode = modeApproving
	m.pending = &askengine.Result{Approval: &askengine.ApprovalRequest{TaskID: "task-abc"}}
	m.turnWorkDir = "/tmp/session-worktree"

	next, _ := m.approvePending()
	got := next.(tuiModel)
	if got.stream == nil || got.liveTask != live {
		t.Fatal("approval must resume on a stream without replacing the live task card")
	}
	got = step(got, progressMsg{stream: got.stream, progress: askengine.Progress{Kind: askengine.ProgressRoute, Name: "routing"}})
	if got.liveTask != live || len(live.stages) != 1 {
		t.Fatalf("resume progress must extend the existing card: stages=%+v", live.stages)
	}

	stale := newTestStream(new(bool))
	after, cmd := got.Update(resumedMsg{stream: stale, out: &askengine.Result{Kind: "task", TaskID: "task-abc", OK: true}})
	unchanged := after.(tuiModel)
	if cmd != nil || unchanged.mode != modeAsking || unchanged.stream != got.stream || unchanged.liveTask != live {
		t.Fatal("a stale resume completion must not commit or clear the active task card")
	}

	cancelled := false
	got.stream.cancel = func() { cancelled = true }
	after, _ = got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	stopped := after.(tuiModel)
	if !cancelled || stopped.mode != modeIdle || stopped.stream != nil {
		t.Fatal("interrupting an approval resume must cancel its context and return to idle")
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

// TestTUIWelcomeFitsItsTerminal tests that the welcome banner adapts to the
// terminal size: narrow terminals (<76 columns) receive the compact header (5
// rows) while standard/wide terminals (>=76 columns) receive the full ASCII
// wordmark (11 rows), and no line ever exceeds the terminal width.
func TestTUIWelcomeFitsItsTerminal(t *testing.T) {
	for _, width := range []int{40, 52, 80, 200} {
		m := newTestTUI(t)
		m.r.cfg.Node.Name = "XenithdeMacBook-Pro.local"
		m.r.cfg.Storage.WorkPath = "/Users/xenith/Library/Application Support/openpanda"
		m.r.cfg.Model.Model = "deepseek-v4-flash"
		m = step(m, tea.WindowSizeMsg{Width: width, Height: 30})
		lines := strings.Split(m.welcome(), "\n")
		wantRows := 11
		if width < 76 {
			wantRows = 5
		}
		if len(lines) != wantRows {
			t.Errorf("width %d: banner is %d rows, want %d", width, len(lines), wantRows)
		}
		for _, line := range lines {
			if w := cliui.DisplayWidth(line); w > width {
				t.Errorf("width %d: banner line is %d columns: %q", width, w, line)
			}
		}
	}
}

// TestTUIHintLineShedsHintsWhenNarrow: the legend must never wrap or be cut
// mid-word, and submit/quit are the two hints it may not drop. The other three
// go in reverse order of usefulness — thought, newline, then the mouse toggle.
func TestTUIHintLineShedsHintsWhenNarrow(t *testing.T) {
	full := newTestTUI(t)
	full = step(full, tea.WindowSizeMsg{Width: 120, Height: 40})
	// The separator is a theme glyph — "·", or "|" when the terminal is not
	// UTF-8 — so count what this theme renders rather than the literal rune.
	// Counting "·" made the gate red on any shell whose locale env is unset
	// (a GUI-launched terminal, most CI images): the hints were all there, only
	// the separator had fallen back to ASCII.
	sep := full.th.glyph("·", "|")
	if n := strings.Count(full.hintLine(), sep); n != 4 {
		t.Fatalf("a wide terminal should show all five hints, separators=%d in %q", n, full.hintLine())
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
	if strings.Contains(line, "ctrl+t") {
		t.Fatalf("the mouse toggle should be shed before submit/quit: %q", line)
	}
}

// TestStatusRowIsOneFittingLine pins the footer that replaced the two rows around
// the input box: one line, never wider than the frame, and when the width runs out
// it is the state that survives rather than the key legend — a hint can be looked
// up in /help, which project the next task lands in cannot.
func TestStatusRowIsOneFittingLine(t *testing.T) {
	for _, width := range []int{40, 60, 120} {
		m := newTestTUI(t)
		m = step(m, tea.WindowSizeMsg{Width: width, Height: 30})
		m.projName = "panda"
		row := m.statusRow()
		if strings.Contains(row, "\n") {
			t.Errorf("width %d: the status row must be a single line: %q", width, row)
		}
		if w := cliui.DisplayWidth(row); w > m.textWidth() {
			t.Errorf("width %d: status row is %d columns, budget %d: %q", width, w, m.textWidth(), row)
		}
		if !strings.Contains(row, "panda") {
			t.Errorf("width %d: the active project must stay visible: %q", width, row)
		}
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

// TestTUIStreamingMultipleDeltasDoesNotPanic guards against the strings.Builder
// copied-by-value panic by simulating an in-flight stream that delivers multiple
// consecutive delta chunks across model updates.
func TestTUIStreamingMultipleDeltasDoesNotPanic(t *testing.T) {
	m := newTestTUI(t)
	m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	cancelled := false
	m.mode = modeAsking
	m.stream = newTestStream(&cancelled)

	chunks := []string{
		"我是 OpenPanda，",
		"你所有设备和 agent 的",
		"大总管。",
		"简单说，我有四件事",
		"可以为你处理。",
	}
	for _, c := range chunks {
		m = step(m, deltaMsg{stream: m.stream, text: c})
	}
	if got := m.liveAnswerText(); got != "我是 OpenPanda，你所有设备和 agent 的大总管。简单说，我有四件事可以为你处理。" {
		t.Fatalf("unexpected liveAnswer: %q", got)
	}
	v := m.View()
	if !strings.Contains(v, "大总管") {
		t.Fatalf("view should contain streamed answer: %q", v)
	}
}

// TestTUIRuntimeSteeringInputAndStop tests that while an ask is running (modeAsking),
// the user can see the input box, type a steering idea and inject it with Enter,
// and stop the running task immediately with Esc.
func TestTUIRuntimeSteeringInputAndStop(t *testing.T) {
	m := newTestTUI(t)
	m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	cancelled := false
	m.mode = modeAsking
	m.stream = newTestStream(&cancelled)
	m.pendingPrompt = "Initial question"

	// 1. View should include both the in-flight status and the input box
	v := m.View()
	if !strings.Contains(v, "Esc") || !strings.Contains(v, "Enter") {
		t.Fatalf("runtime view should show Esc stop and Enter steer hints: %q", v)
	}

	// 2. Type steering ideas into runtime input box
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("also add benchmarks")})
	if m.ta.Value() != "also add benchmarks" {
		t.Fatalf("runtime input not captured in textarea: %q", m.ta.Value())
	}
	if m.mode != modeAsking {
		t.Fatalf("typing should not cancel asking mode: got %v", m.mode)
	}

	// 3. Press Enter to steer in-flight task
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(tuiModel)
	if m.mode != modeAsking {
		t.Fatalf("steering with enter should stay in modeAsking: got %v", m.mode)
	}
	if m.ta.Value() != "" {
		t.Fatalf("textarea should reset after steering: %q", m.ta.Value())
	}
	if !strings.Contains(m.pendingPrompt, "also add benchmarks") {
		t.Fatalf("pendingPrompt should contain steering idea: %q", m.pendingPrompt)
	}
	if cmd == nil {
		t.Fatal("steering should emit printBlock cmd")
	}

	// 4. Press Esc to stop in-flight task
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(tuiModel)
	if !cancelled {
		t.Fatal("esc should cancel in-flight stream context")
	}
	if m.mode != modeIdle {
		t.Fatalf("esc should return to modeIdle: got %v", m.mode)
	}
}

// TestTUIMouseClickActions verifies mouse click handling across modeAsking and modeApproving.
func TestTUIMouseClickActions(t *testing.T) {
	// 1. modeAsking: clicking Stop button (X=10, Y=38) stops in-flight task
	{
		m := newTestTUI(t)
		m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
		cancelled := false
		m.mode = modeAsking
		m.stream = newTestStream(&cancelled)

		buttons := m.askingButtonRects()
		if len(buttons) != 3 || buttons[0].w == 0 {
			t.Fatalf("stop button has no rendered hitbox: %+v", buttons)
		}
		mouseMsg := tea.MouseMsg{
			X:      buttons[0].x + buttons[0].w/2,
			Y:      buttons[0].y,
			Action: tea.MouseActionPress,
			Button: tea.MouseButtonLeft,
		}
		next, _ := m.Update(mouseMsg)
		got := next.(tuiModel)
		if !cancelled {
			t.Fatal("clicking stop button should cancel in-flight stream")
		}
		if got.mode != modeIdle {
			t.Fatalf("mode: got %v want modeIdle after clicking stop", got.mode)
		}
	}

	// 2. modeAsking: clicking Inject button (X=30, Y=38) injects steer idea
	{
		m := newTestTUI(t)
		m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
		cancelled := false
		m.mode = modeAsking
		m.stream = newTestStream(&cancelled)
		m.pendingPrompt = "Initial question"
		m.ta.SetValue("refactor cleanly")

		buttons := m.askingButtonRects()
		if len(buttons) != 3 || buttons[1].w == 0 {
			t.Fatalf("steer button has no rendered hitbox: %+v", buttons)
		}
		mouseMsg := tea.MouseMsg{
			X:      buttons[1].x + buttons[1].w/2,
			Y:      buttons[1].y,
			Action: tea.MouseActionPress,
			Button: tea.MouseButtonLeft,
		}
		next, cmd := m.Update(mouseMsg)
		got := next.(tuiModel)
		if got.mode != modeAsking {
			t.Fatalf("clicking inject should keep modeAsking, got %v", got.mode)
		}
		if !strings.Contains(got.pendingPrompt, "refactor cleanly") {
			t.Fatalf("pendingPrompt should contain injected steer: %q", got.pendingPrompt)
		}
		if got.ta.Value() != "" {
			t.Fatalf("textarea should clear after steer click: %q", got.ta.Value())
		}
		if cmd == nil {
			t.Fatal("clicking inject should emit printBlock cmd")
		}
	}

	// 3. modeAsking: clicking Thought button (X=55, Y=38) toggles expandThought
	{
		m := newTestTUI(t)
		m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
		m.mode = modeAsking
		m.expandThought = false

		buttons := m.askingButtonRects()
		if len(buttons) != 3 || buttons[2].w == 0 {
			t.Fatalf("thought button has no rendered hitbox: %+v", buttons)
		}
		mouseMsg := tea.MouseMsg{
			X:      buttons[2].x + buttons[2].w/2,
			Y:      buttons[2].y,
			Action: tea.MouseActionPress,
			Button: tea.MouseButtonLeft,
		}
		next, _ := m.Update(mouseMsg)
		got := next.(tuiModel)
		if !got.expandThought {
			t.Fatal("clicking thought button should expand thought")
		}
		// Subsequent mouse release must NOT toggle thought back:
		releaseMsg := tea.MouseMsg{
			X:      buttons[2].x + buttons[2].w/2,
			Y:      buttons[2].y,
			Action: tea.MouseActionRelease,
			Button: tea.MouseButtonLeft,
		}
		next2, _ := got.Update(releaseMsg)
		got2 := next2.(tuiModel)
		if !got2.expandThought {
			t.Fatal("mouse release must not revert thought toggle")
		}
	}

	// 4. modeApproving: a click answers the card only when it lands on one of
	// the two option cells of the choice row — anywhere else it is ignored, so
	// a stray click on the transcript can never approve an irreversible task.
	{
		m := newTestTUI(t)
		m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
		m.mode = modeApproving
		m.pending = &askengine.Result{Approval: &askengine.ApprovalRequest{TaskID: "task-abc"}}

		layout := m.approvalLayout()
		if layout.yes.w == 0 || layout.no.w == 0 {
			t.Fatalf("approval card did not render option hitboxes: %+v", layout)
		}
		originY := 40 - lipgloss.Height(layout.rendered)
		yesMid := layout.yes.x + layout.yes.w/2
		noMid := layout.no.x + layout.no.w/2
		choiceY := originY + layout.yes.y

		// A click in the transcript area (anywhere off the choice row) is a
		// no-op — this is the regression the old left-half-of-screen rule
		// failed: X=10,Y=5 used to approve.
		got := step(m, tea.MouseMsg{X: 10, Y: 5, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
		if got.mode != modeApproving || got.pending == nil {
			t.Fatalf("click off the choice row must not answer the card: mode=%v pending=%+v", got.mode, got.pending)
		}
		// A click on the card body (the head line) is also a no-op, even at an
		// X that falls inside the yes option's column span.
		headY := originY + 1
		got = step(got, tea.MouseMsg{X: yesMid, Y: headY, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
		if got.mode != modeApproving || got.pending == nil {
			t.Fatalf("click on the card body must not answer the card: mode=%v pending=%+v", got.mode, got.pending)
		}
		// A click on the choice row but between/beyond the option cells is a
		// no-op too.
		gapX := layout.yes.x + layout.yes.w + 1
		got = step(got, tea.MouseMsg{X: gapX, Y: choiceY, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
		if got.mode != modeApproving || got.pending == nil {
			t.Fatalf("click between the option cells must not answer the card: mode=%v pending=%+v", got.mode, got.pending)
		}

		// Clicking the [n] cell denies: back to idle, card dismissed.
		got = step(got, tea.MouseMsg{X: noMid, Y: choiceY, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
		if got.mode != modeIdle {
			t.Fatalf("clicking the [n] cell should deny (modeIdle), got %v", got.mode)
		}
		if got.pending != nil {
			t.Fatalf("pending should be cleared on deny, got %+v", got.pending)
		}

		// Clicking the [y] cell approves: the turn resumes asking.
		got = step(m, tea.MouseMsg{X: yesMid, Y: choiceY, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
		if got.mode != modeAsking {
			t.Fatalf("clicking the [y] cell should approve (modeAsking), got %v", got.mode)
		}
		if got.stream == nil {
			t.Fatal("approval resume must install a cancellable stream")
		}
	}

	// 5. modeIdle: clicking a slash menu item submits that command
	{
		m := newTestTUI(t)
		m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
		m.mode = modeIdle
		m.ta.SetValue("/he")
		m.menu.sync(m.ta.Value(), nil)
		if !m.menu.active || len(m.menu.items) == 0 {
			t.Fatal("menu should be active for /he")
		}
		menuLines := strings.Split(m.menu.render(m.th, m.textWidth(), m.menuRows()), "\n")
		targetY := 40 - 1 - len(menuLines)
		clickMsg := tea.MouseMsg{
			X:      5,
			Y:      targetY,
			Action: tea.MouseActionPress,
			Button: tea.MouseButtonLeft,
		}
		next, cmd := m.Update(clickMsg)
		got := next.(tuiModel)
		if got.menu.active {
			t.Fatal("clicking menu item should dismiss the menu")
		}
		if cmd == nil {
			t.Fatal("clicking menu item should submit command")
		}
	}

	// 6. Chinese locale (zh-CN) asking button hit testing
	{
		r := &repl{loc: i18n.Locale("zh-CN"), cfg: &config.Config{}, interactive: true}
		m := newTUIModel(r)
		m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
		m.mode = modeAsking
		buttons := m.askingButtonRects()
		if len(buttons) != 3 {
			t.Fatalf("expected three asking button hitboxes, got %+v", buttons)
		}
		for i, rect := range buttons {
			if rect.w == 0 || m.askingButtonHit(rect.x+rect.w/2, rect.y) != i {
				t.Fatalf("button %d does not match rendered rectangle %+v", i, rect)
			}
		}
		for i := 0; i < len(buttons)-1; i++ {
			gapX := buttons[i].x + buttons[i].w
			if hit := m.askingButtonHit(gapX, buttons[i].y); hit != -1 {
				t.Fatalf("gap after button %d hit %d", i, hit)
			}
		}
	}
}

// TestTUIVerySmallWindowSize verifies that the model never panics or crashes when
// rendered into degenerate, tiny, or negative terminal windows (0x0, 1x1, 5x2, -10x-5).
func TestTUIVerySmallWindowSize(t *testing.T) {
	sizes := []tea.WindowSizeMsg{
		{Width: 1, Height: 1},
		{Width: 5, Height: 2},
		{Width: 10, Height: 5},
	}
	for _, sz := range sizes {
		m := newTestTUI(t)
		m = step(m, sz)
		for _, mode := range []tuiMode{modeIdle, modeAsking} {
			m.mode = mode
			if mode == modeAsking {
				m.liveAnswerChunks = []string{"a deliberately long answer that must fit the tiny terminal"}
				m.liveAnswerBytes = len(m.liveAnswerChunks[0])
				m.stream = newTestStream(new(bool))
			}
			view := m.View()
			if view == "" {
				t.Fatalf("empty view for size %+v mode %v", sz, mode)
			}
			for row, line := range strings.Split(view, "\n") {
				if w := lipgloss.Width(line); w > sz.Width {
					t.Fatalf("size %+v mode %v row %d is %d columns: %q", sz, mode, row, w, line)
				}
			}
		}
	}
}

// TestTUIOutOfBoundsMouseEvents verifies that strange or negative mouse click coordinates
// never cause an index panic or crash.
func TestTUIOutOfBoundsMouseEvents(t *testing.T) {
	m := newTestTUI(t)
	m = step(m, tea.WindowSizeMsg{Width: 80, Height: 24})

	weirdClicks := []tea.MouseMsg{
		{X: -10, Y: -10, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft},
		{X: 999, Y: 999, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft},
		{X: 0, Y: 0, Action: tea.MouseActionRelease, Button: tea.MouseButtonNone},
		{X: 50, Y: 50, Action: tea.MouseActionMotion, Button: tea.MouseButtonRight},
	}

	for _, click := range weirdClicks {
		m = step(m, click)
		m.mode = modeAsking
		m = step(m, click)
		m.mode = modeApproving
		m = step(m, click)
	}
}

// TestTUIMultipleSteeringDrains verifies that multiple steering ideas injected during
// execution are queued and drained safely without blocking or dropping.
func TestTUIMultipleSteeringDrains(t *testing.T) {
	s := &askStream{
		events:  make(chan tea.Msg, 256),
		cancel:  func() {},
		dropped: make(chan struct{}),
		steer:   make(chan string, 32),
	}

	ideas := []string{
		"第一点要求：必须添加单元测试",
		"第二点要求：优化内存开销",
		"第三点要求：提供完整的中文文档",
	}

	for _, id := range ideas {
		s.injectSteer(id)
	}

	var drained []string
	for {
		select {
		case id := <-s.steer:
			drained = append(drained, id)
		default:
			goto done
		}
	}
done:
	if len(drained) != len(ideas) {
		t.Fatalf("expected %d drained ideas, got %d", len(ideas), len(drained))
	}
	for i, id := range ideas {
		if drained[i] != id {
			t.Errorf("drained[%d] = %q, want %q", i, drained[i], id)
		}
	}
}

// TestTUIStartupIsNotBottomPadded pins the layout fix: the welcome frame is
// committed where the cursor already is — the top of a fresh terminal — and
// the transcript grows downward from it. The frame used to be preceded by
// blank lines computed to push it to the bottom edge, which stranded the
// greeting under a screenful of dead space on anything taller than the frame
// and put the only interactive row where the eye is not.
func TestTUIStartupIsNotBottomPadded(t *testing.T) {
	m := newTestTUI(t)
	prints := m.startupPrints()
	if len(prints) == 0 {
		t.Fatal("startup must print the welcome frame")
	}
	if strings.HasPrefix(prints[0], "\n") {
		t.Fatal("the welcome frame must print at the cursor, not be padded down to the bottom edge")
	}
}

// TestTUIStartupReplaysRestoredConversation pins the visibility fix for a
// resumed bare chat: runTUI restores r.convo silently, and the replay has to
// re-commit those turns to scrollback — otherwise the previous context exists
// nowhere the user can read, because the wheel belongs to the terminal and
// nothing was printed above the banner this run. Three banked pairs must come
// back as three ❯/⏺ block pairs behind the banner, and the first
// WindowSizeMsg must hand Bubble Tea exactly that many Println commands.
func TestTUIStartupReplaysRestoredConversation(t *testing.T) {
	m := newTestTUI(t)
	m.r.convo = []entry.Turn{
		{Role: "user", Content: "what port does the daemon use?"},
		{Role: "assistant", Content: "8787 by default."},
		{Role: "user", Content: "and the web console?"},
		{Role: "assistant", Content: "Same port, /web path."},
		{Role: "user", Content: "thanks"},
		{Role: "assistant", Content: "Any time."},
	}
	prints := m.startupPrints()
	if len(prints) != 1+6 {
		t.Fatalf("expected banner + 6 replayed blocks, got %d prints", len(prints))
	}
	if prints[0] != m.welcome() {
		t.Fatal("the banner must come first, unpadded")
	}
	joined := strings.Join(prints, "\n")
	for _, want := range []string{
		"what port does the daemon use?",
		"8787 by default.",
		"Same port, /web path.",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("replay lost a restored turn: %q not in transcript", want)
		}
	}

	next, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if !next.(tuiModel).ready {
		t.Fatal("first WindowSizeMsg must mark the model ready")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("first WindowSizeMsg must batch the startup prints, got %T", cmd())
	}
	if len(batch) != 1+6 {
		t.Fatalf("expected %d Println commands, got %d", 1+6, len(batch))
	}
}

// TestTUIStartupReplayFoldsOldTurns bounds the replay: its job is orientation,
// not a verbatim wall, so a long banked conversation shows only the most
// recent ten pairs, behind a note counting what was folded away.
func TestTUIStartupReplayFoldsOldTurns(t *testing.T) {
	m := newTestTUI(t)
	for i := 0; i < 12; i++ {
		// Zero-padded labels so no kept turn ("question 10", "question 11")
		// contains a folded turn's text as a substring.
		label := strconv.Itoa(i)
		if len(label) < 2 {
			label = "0" + label
		}
		m.r.convo = append(m.r.convo,
			entry.Turn{Role: "user", Content: "question " + label},
			entry.Turn{Role: "assistant", Content: "answer " + label},
		)
	}
	prints := m.startupPrints()
	// Banner + fold note + 10 pairs.
	if len(prints) != 1+1+20 {
		t.Fatalf("expected banner + fold note + 20 blocks, got %d prints", len(prints))
	}
	note := prints[1]
	if strings.Contains(note, "question") || strings.Contains(note, "answer") {
		t.Fatalf("prints[1] must be the fold note, got %q", note)
	}
	joined := strings.Join(prints, "\n")
	for _, gone := range []string{"question 00", "answer 00", "question 01"} {
		if strings.Contains(joined, gone) {
			t.Fatalf("folded turn leaked into the replay: %q", gone)
		}
	}
	for _, kept := range []string{"question 02", "answer 11"} {
		if !strings.Contains(joined, kept) {
			t.Fatalf("kept turn missing from the replay: %q", kept)
		}
	}
}

// TestShedHintsDropsByPriorityInEveryLegend pins the fix for a positional drop
// list. The old loop dropped fixed indices sized for the five-entry chat legend,
// so on the four-entry slash-menu legend its third drop was skipped by a bounds
// guard and the two legends silently ran different shedding policies. Shedding
// now follows each entry's priority, which describes a legend of any length.
func TestShedHintsDropsByPriorityInEveryLegend(t *testing.T) {
	chat := newTestTUI(t)
	menu := newTestTUI(t)
	menu.menu.active = true
	menu.menu.items = []menuItem{{name: "/model"}}

	cases := []struct {
		name string
		m    tuiModel
		keep []string // entries that must survive every budget
		gone string   // the most expendable entry, shed first
	}{
		{
			name: "chat legend",
			m:    chat,
			keep: []string{i18n.T(chat.loc, "tui.hint.submit"), i18n.T(chat.loc, "tui.hint.quit")},
			gone: i18n.T(chat.loc, "tui.hint.thought"),
		},
		{
			name: "slash-menu legend",
			m:    menu,
			keep: []string{i18n.T(menu.loc, "tui.hint.menuRun"), i18n.T(menu.loc, "tui.hint.menuCancel")},
			gone: i18n.T(menu.loc, "tui.hint.menuComplete"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			all := tc.m.hintKeys()
			if len(all) < 4 {
				t.Fatalf("setup wrong: legend has only %d entries", len(all))
			}
			// A zero budget forces the loop to shed everything it is allowed to,
			// which is exactly the set of protected entries.
			got := hintTexts(shedHints(all, "  ·  ", 0))
			if len(got) != len(tc.keep) {
				t.Fatalf("only the protected entries should survive, got %v", got)
			}
			for _, k := range tc.keep {
				if !slices.Contains(got, k) {
					t.Errorf("protected entry %q was shed: %v", k, got)
				}
			}
			if slices.Contains(got, tc.gone) {
				t.Errorf("most expendable entry %q survived a zero budget: %v", tc.gone, got)
			}
		})
	}
}

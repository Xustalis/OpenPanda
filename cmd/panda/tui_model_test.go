package main

import (
	"context"
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
// mid-word, and submit/quit are the two hints it may not drop.
func TestTUIHintLineShedsHintsWhenNarrow(t *testing.T) {
	full := newTestTUI(t)
	full = step(full, tea.WindowSizeMsg{Width: 120, Height: 40})
	// The separator is a theme glyph — "·", or "|" when the terminal is not
	// UTF-8 — so count what this theme renders rather than the literal rune.
	// Counting "·" made the gate red on any shell whose locale env is unset
	// (a GUI-launched terminal, most CI images): the hints were all there, only
	// the separator had fallen back to ASCII.
	sep := full.th.glyph("·", "|")
	if n := strings.Count(full.hintLine(), sep); n != 3 {
		t.Fatalf("a wide terminal should show all four hints, separators=%d in %q", n, full.hintLine())
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
		m = step(m, deltaMsg(c))
	}
	if m.liveAnswer != "我是 OpenPanda，你所有设备和 agent 的大总管。简单说，我有四件事可以为你处理。" {
		t.Fatalf("unexpected liveAnswer: %q", m.liveAnswer)
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

		mouseMsg := tea.MouseMsg{
			X:      10,
			Y:      38,
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

		mouseMsg := tea.MouseMsg{
			X:      30,
			Y:      38,
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

		mouseMsg := tea.MouseMsg{
			X:      55,
			Y:      38,
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
			X:      55,
			Y:      38,
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

		// Locate the choice row the way approvalHit does: re-render the framed
		// card and find the [y]/[n] line from the bottom.
		lines := strings.Split(m.approvalCard(), "\n")
		choiceRow := -1
		for i := len(lines) - 1; i >= 0; i-- {
			if strings.Contains(lines[i], "[y]") && strings.Contains(lines[i], "[n]") {
				choiceRow = i
				break
			}
		}
		if choiceRow < 0 {
			t.Fatal("approval card did not render a choice row")
		}
		choiceY := 40 - len(lines) + choiceRow

		// Option cell midpoints, from the same pieces choice() renders: a
		// border+padding origin of 2, then per option a 2-col focus prefix,
		// the [k] badge, a space and the label, with a 3-space gap between.
		optW := func(key string) int {
			return 2 + 3 + 1 + cliui.DisplayWidth(i18n.T(m.loc, "tui.approval."+key))
		}
		yesMid := 2 + optW("yes")/2
		noMid := 2 + optW("yes") + 3 + optW("no")/2

		// A click in the transcript area (anywhere off the choice row) is a
		// no-op — this is the regression the old left-half-of-screen rule
		// failed: X=10,Y=5 used to approve.
		got := step(m, tea.MouseMsg{X: 10, Y: 5, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
		if got.mode != modeApproving || got.pending == nil {
			t.Fatalf("click off the choice row must not answer the card: mode=%v pending=%+v", got.mode, got.pending)
		}
		// A click on the card body (the head line) is also a no-op, even at an
		// X that falls inside the yes option's column span.
		headY := 40 - len(lines) + 1
		got = step(got, tea.MouseMsg{X: yesMid, Y: headY, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
		if got.mode != modeApproving || got.pending == nil {
			t.Fatalf("click on the card body must not answer the card: mode=%v pending=%+v", got.mode, got.pending)
		}
		// A click on the choice row but between/beyond the option cells is a
		// no-op too.
		gapX := 2 + optW("yes") + 1
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
		// Hit stop button at X=8, Y=39
		if hit := m.askingButtonHit(8, 39); hit != 0 {
			t.Fatalf("expected Stop button (0), got %d", hit)
		}
		// Hit steer button at X=26, Y=39
		if hit := m.askingButtonHit(26, 39); hit != 1 {
			t.Fatalf("expected Steer button (1), got %d", hit)
		}
		// Hit thought button at X=48, Y=39
		if hit := m.askingButtonHit(48, 39); hit != 2 {
			t.Fatalf("expected Thought button (2), got %d", hit)
		}
	}
}

// TestTUIVerySmallWindowSize verifies that the model never panics or crashes when
// rendered into degenerate, tiny, or negative terminal windows (0x0, 1x1, 5x2, -10x-5).
func TestTUIVerySmallWindowSize(t *testing.T) {
	sizes := []tea.WindowSizeMsg{
		{Width: 0, Height: 0},
		{Width: 1, Height: 1},
		{Width: 5, Height: 2},
		{Width: 10, Height: 5},
		{Width: -10, Height: -5},
	}
	for _, sz := range sizes {
		m := newTestTUI(t)
		m = step(m, sz)
		// View in idle
		v := m.View()
		if v == "" {
			t.Fatalf("empty view for size %+v", sz)
		}

		// View in asking mode with liveAnswer and task
		m.mode = modeAsking
		m.liveAnswer = "short answer"
		m.liveTask = newTaskProgress("building task", time.Now())
		vAsking := m.View()
		if vAsking == "" {
			t.Fatalf("empty asking view for size %+v", sz)
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

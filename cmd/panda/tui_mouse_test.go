package main

// Regression cover for the mouse-ownership fix (docs/confirmed-issues-fix-report.md
// §3.1): the terminal keeps the mouse by default so drag-select and the
// terminal's own copy shortcut work, while the transcript stays scrollable
// through alternate scroll (wheel → Up/Down), PageUp/PageDown and ctrl+t.

import (
	"fmt"
	"io"
	"os"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// newMouseTestTUI pins the environment (a developer's own PANDA_MOUSE must not
// change what "the default" means) and returns the usual idle model.
func newMouseTestTUI(t *testing.T) tuiModel {
	t.Helper()
	t.Setenv(MouseEnvVar, "")
	m := newTestTUI(t)
	if m.mouse.captured() {
		t.Fatalf("default mouse mode = %v, want select (the terminal keeps the mouse)", m.mouse)
	}
	return m
}

// sameMsgType reports whether two commands emit the same message type. Bubble
// Tea's mouse-protocol messages are unexported, so comparing type names against
// the library's own commands is the only handle a test has — and it keeps
// working if those types are renamed, since both sides go through the same
// formatter.
func sameMsgType(a, b tea.Cmd) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return fmt.Sprintf("%T", a()) == fmt.Sprintf("%T", b())
}

// TestResolveMouseModePrecedence pins env > config > default, and that an
// unrecognised token falls through to the next source instead of silently
// choosing a side.
func TestResolveMouseModePrecedence(t *testing.T) {
	cases := []struct {
		name string
		env  string
		cfg  string
		want mouseMode
	}{
		{"no setting means select", "", "", mouseSelect},
		{"config scroll", "", "scroll", mouseScroll},
		{"config select", "", "select", mouseSelect},
		{"unknown config value falls through", "", "mouse", mouseSelect},
		{"env beats config", "scroll", "select", mouseScroll},
		{"env select beats config scroll", "select", "scroll", mouseSelect},
		{"unknown env falls through to config", "wat", "scroll", mouseScroll},
		{"case and padding are tolerated", "  SCROLL ", "", mouseScroll},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(MouseEnvVar, tc.env)
			cfg := &config.Config{}
			cfg.UI.Mouse = tc.cfg
			if got := resolveMouseMode(cfg); got != tc.want {
				t.Fatalf("resolveMouseMode(env=%q, config=%q) = %v, want %v", tc.env, tc.cfg, got, tc.want)
			}
		})
	}
}

// TestResolveMouseModeWithoutConfig covers the engine-less path: a nil config is
// legal here (it is what the REPL hands over when nothing was loaded), and it
// must still land on select rather than panic.
func TestResolveMouseModeWithoutConfig(t *testing.T) {
	t.Setenv(MouseEnvVar, "")
	if got := resolveMouseMode(nil); got != mouseSelect {
		t.Fatalf("nil config resolved to %v, want select", got)
	}
}

// TestMouseConfigFileDrivesStartupMode closes the loop from the config file to
// the startup decision: ui.mouse has to survive YAML decoding and reach the
// resolver, otherwise the documented setting would be dead weight.
func TestMouseConfigFileDrivesStartupMode(t *testing.T) {
	t.Setenv(MouseEnvVar, "")
	var cfg config.Config
	if err := yaml.Unmarshal([]byte("ui:\n  mouse: scroll\n"), &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if got := resolveMouseMode(&cfg); got != mouseScroll {
		t.Fatalf("ui.mouse: scroll resolved to %v, want scroll", got)
	}
}

// TestMouseToggleFlipsModeAndProtocol drives ctrl+t (and its F2 twin) both ways:
// the mode field flips, the command is a real one, and each mode maps to the
// Bubble Tea command that actually changes the terminal protocol.
func TestMouseToggleFlipsModeAndProtocol(t *testing.T) {
	m := newMouseTestTUI(t)
	m.mouse = mouseSelect

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	got := next.(tuiModel)
	if !got.mouse.captured() {
		t.Fatal("ctrl+t from select should capture the mouse")
	}
	if cmd == nil {
		t.Fatal("switching modes must return a command; the mode field alone changes nothing on the wire")
	}

	next, _ = got.Update(tea.KeyMsg{Type: tea.KeyF2})
	got = next.(tuiModel)
	if got.mouse.captured() {
		t.Fatal("F2 should hand the mouse back to the terminal")
	}

	if !sameMsgType(mouseProgramCmd(mouseScroll), tea.EnableMouseCellMotion) {
		t.Fatal("scroll mode must ask the terminal for cell-motion reporting")
	}
	if !sameMsgType(mouseProgramCmd(mouseSelect), tea.DisableMouse) {
		t.Fatal("select mode must release the mouse so terminal selection returns")
	}
}

// TestMouseToggleAnnouncesTheNewMode checks that flipping the mode leaves a note
// in the transcript. Without it the change is invisible: a drag that suddenly
// fails to select anything looks like a bug rather than a mode.
func TestMouseToggleAnnouncesTheNewMode(t *testing.T) {
	m := newMouseTestTUI(t)
	m.mouse = mouseSelect
	before := len(m.chatHistory.blocks)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	got := next.(tuiModel)
	if len(got.chatHistory.blocks) != before+1 {
		t.Fatalf("mode switch should add one transcript note, got %d blocks (was %d)",
			len(got.chatHistory.blocks), before)
	}
	note := got.chatHistory.blocks[len(got.chatHistory.blocks)-1]
	if note.kind != blockNote {
		t.Fatalf("mode note kind = %v, want blockNote", note.kind)
	}
	if want := i18n.T(i18n.English, "tui.mouse.scrollOn"); note.body != want {
		t.Fatalf("note = %q, want %q", note.body, want)
	}

	next, _ = got.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	got = next.(tuiModel)
	note = got.chatHistory.blocks[len(got.chatHistory.blocks)-1]
	if want := i18n.T(i18n.English, "tui.mouse.selectOn"); note.body != want {
		t.Fatalf("note = %q, want %q", note.body, want)
	}
}

// TestArrowScrollsTranscriptInSelectMode is the core of the fix: with the
// terminal owning the mouse, a wheel notch arrives as an arrow key (alternate
// scroll), so those keys have to move the transcript. Down at the bottom clamps
// instead of going negative — offset 0 is the anchored live bottom.
func TestArrowScrollsTranscriptInSelectMode(t *testing.T) {
	m := newMouseTestTUI(t)
	m.mouse = mouseSelect
	m.width, m.height = 80, 24

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(tuiModel)
	if m.scrollOffset != transcriptScrollStep {
		t.Fatalf("arrow up should scroll back %d lines, got %d", transcriptScrollStep, m.scrollOffset)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(tuiModel)
	if m.scrollOffset != 0 {
		t.Fatalf("arrow down should return to the bottom, got %d", m.scrollOffset)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(tuiModel)
	if m.scrollOffset != 0 {
		t.Fatalf("arrow down below the bottom must clamp at 0, got %d", m.scrollOffset)
	}
}

// TestArrowKeepsMultiLineEditingWithTheTerminalOwningTheMouse guards the cost of
// the mapping above: a multi-line draft still needs Up/Down to move the caret,
// so the transcript must not steal them.
func TestArrowKeepsMultiLineEditingWithTheTerminalOwningTheMouse(t *testing.T) {
	m := newMouseTestTUI(t)
	m.mouse = mouseSelect
	m.width, m.height = 80, 24
	m.ta.SetValue("first line\nsecond line")
	if m.ta.LineCount() < 2 {
		t.Fatalf("draft should be multi-line, LineCount = %d", m.ta.LineCount())
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(tuiModel)
	if m.scrollOffset != 0 {
		t.Fatalf("a multi-line draft keeps the arrows; transcript moved to %d", m.scrollOffset)
	}
}

// TestArrowKeepsSlashMenuNavigation is the other exception: while the popup is
// open the arrows pick rows, so a wheel notch should move the highlight rather
// than the transcript (in scroll mode the same gesture does exactly that).
func TestArrowKeepsSlashMenuNavigation(t *testing.T) {
	m := newMouseTestTUI(t)
	m.mouse = mouseSelect
	m.width, m.height = 80, 24
	m.ta.SetValue("/")
	m.menu.sync(m.ta.Value(), m.argResolve())
	if !m.menu.active || len(m.menu.items) < 2 {
		t.Fatalf("slash menu should be open with rows, active=%v rows=%d", m.menu.active, len(m.menu.items))
	}
	start := m.menu.sel

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(tuiModel)
	if m.menu.sel == start {
		t.Fatal("the open menu owns the arrow keys and should have moved its highlight")
	}
	if m.scrollOffset != 0 {
		t.Fatalf("the open menu must not scroll the transcript, offset = %d", m.scrollOffset)
	}
}

// TestArrowScrollsWhileAsking covers the mode where long answers appear: reading
// back mid-turn is the main reason to scroll at all, and it must not require the
// app to own the mouse.
func TestArrowScrollsWhileAsking(t *testing.T) {
	m := newMouseTestTUI(t)
	m.mouse = mouseSelect
	m.width, m.height = 80, 24
	m.mode = modeAsking

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(tuiModel)
	if m.scrollOffset != transcriptScrollStep {
		t.Fatalf("asking should scroll on arrow up, offset = %d", m.scrollOffset)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = next.(tuiModel)
	if m.scrollOffset != 0 {
		t.Fatalf("PgDown should return to the bottom, offset = %d", m.scrollOffset)
	}
}

// TestArrowDoesNotScrollWhenTheAppOwnsTheMouse keeps the other half honest: in
// scroll mode the arrows stay editing keys, because the wheel reaches onMouse
// directly and nothing needs to be faked from keystrokes.
func TestArrowDoesNotScrollWhenTheAppOwnsTheMouse(t *testing.T) {
	m := newMouseTestTUI(t)
	m.width, m.height = 80, 24
	m.mouse = mouseScroll
	m.ta.SetValue("user input draft")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(tuiModel)
	if m.scrollOffset != 0 {
		t.Fatalf("scroll mode must leave the arrows to the editor, offset = %d", m.scrollOffset)
	}

	// ...and the wheel still works there, so the two modes really are equivalent
	// in what they can scroll.
	next, _ = m.Update(tea.MouseMsg{Type: tea.MouseWheelUp})
	m = next.(tuiModel)
	if m.scrollOffset != transcriptScrollStep {
		t.Fatalf("wheel up should still scroll in scroll mode, offset = %d", m.scrollOffset)
	}
}

// TestMouseHintNamesTheNextAction keeps the legend honest: the hint has to say
// what ctrl+t does from where the user stands, not what the modes are called.
func TestMouseHintNamesTheNextAction(t *testing.T) {
	m := newMouseTestTUI(t)
	m.mouse = mouseSelect
	if got, want := m.mouseHint(), i18n.T(i18n.English, "tui.hint.mouseSelect"); got != want {
		t.Fatalf("select-mode hint = %q, want %q", got, want)
	}
	m.mouse = mouseScroll
	if got, want := m.mouseHint(), i18n.T(i18n.English, "tui.hint.mouseScroll"); got != want {
		t.Fatalf("scroll-mode hint = %q, want %q", got, want)
	}

	m.mouse = mouseSelect
	hints := hintTexts(m.hintKeys())
	// The legend sheds its most expendable entries on narrow terminals — the
	// thought fold first, then the newline, then the mouse toggle (see the shed
	// priorities in hintKeys) — because submit and quit are the two a user cannot
	// afford to lose. The mouse hint therefore has to sit after those two, so a
	// cramped terminal drops it last instead of hiding ctrl+t while there is
	// still room to show it.
	idx := -1
	for i, hint := range hints {
		if hint == m.mouseHint() {
			idx = i
		}
	}
	if idx != 3 {
		t.Fatalf("mouse hint at index %d of %d, want 3 (behind the thought and newline hints): %v",
			idx, len(hints), hints)
	}
}

// captureStdout runs cmd with os.Stdout redirected and returns what it wrote.
// The alternate-scroll flag is a raw escape sequence rather than a Bubble Tea
// message, so the bytes are the only handle a test has on it.
func captureStdout(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	if cmd == nil {
		t.Fatal("nil command: nothing would reach the terminal")
	}
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	_ = cmd()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}

// TestAltScrollFollowsTheMode covers the half of the toggle Bubble Tea does not:
// it drives the mouse protocols itself but never emits DECSET 1007, so the flag
// is ours to move. Switching back to select without re-arming it would hand the
// mouse over and leave the wheel dead on every terminal that does not default
// 1007 on — reintroducing, through the toggle, the exact "scrolling is gone"
// complaint this design exists to prevent.
func TestAltScrollFollowsTheMode(t *testing.T) {
	if got := captureStdout(t, altScrollCmd(mouseSelect)); got != altScrollOn {
		t.Errorf("select mode must ask for alternate scroll: got %q, want %q", got, altScrollOn)
	}
	if got := captureStdout(t, captureMode(mouseScroll)); got != altScrollOff {
		t.Errorf("scroll mode must release alternate scroll: got %q, want %q", got, altScrollOff)
	}
}

// captureMode keeps the table above readable: the argument is a mode, not a
// command, and both rows are then asserted the same way.
func captureMode(m mouseMode) tea.Cmd { return altScrollCmd(m) }

// TestWheelScrollsWhileExecRuns: a long task is exactly when a user reads back,
// and the mode gate in onMouse used to swallow the wheel there. PageUp/PageDown
// kept working during exec, and so did Up/Down in select mode, so a dead wheel
// was a hole rather than a policy — the two modes disagreed about whether exec
// could be scrolled at all.
func TestWheelScrollsWhileExecRuns(t *testing.T) {
	m := newMouseTestTUI(t)
	m.width, m.height = 80, 24
	m.mouse = mouseScroll
	m.mode = modeExec

	next, _ := m.Update(tea.MouseMsg{Type: tea.MouseWheelUp})
	m = next.(tuiModel)
	if m.scrollOffset != transcriptScrollStep {
		t.Fatalf("wheel up should read back during exec, offset = %d", m.scrollOffset)
	}

	next, _ = m.Update(tea.MouseMsg{Type: tea.MouseWheelDown})
	m = next.(tuiModel)
	if m.scrollOffset != 0 {
		t.Fatalf("wheel down should return to the live bottom, offset = %d", m.scrollOffset)
	}
}

// TestClicksStayIgnoredWhileExecRuns guards the other side of that change: exec
// has no buttons on screen, so the click gate has to stay shut or a stray click
// could interfere with a running task.
func TestClicksStayIgnoredWhileExecRuns(t *testing.T) {
	m := newMouseTestTUI(t)
	m.width, m.height = 80, 24
	m.mouse = mouseScroll
	m.mode = modeExec

	next, cmd := m.Update(tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      12,
		Y:      8,
	})
	m = next.(tuiModel)
	if m.mode != modeExec || m.quitting {
		t.Fatalf("click during exec changed state: mode=%v quitting=%v", m.mode, m.quitting)
	}
	if cmd != nil {
		t.Fatalf("click during exec produced a command: %T", cmd())
	}
}

// TestOverscrollClampsToContent is the regression for the dead-scroll bug: the
// offset used to be clamped only inside View's local copy, so holding the wheel
// past the top left a phantom offset behind and the following scroll-downs did
// nothing visible until it unwound (measured: 62 inert notches after 100
// wheel-ups). The model now clamps against the ceiling View publishes, so the
// very first scroll-down after an overshoot moves the transcript again.
func TestOverscrollClampsToContent(t *testing.T) {
	m := newMouseTestTUI(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m = next.(tuiModel)
	m.chatHistory = &chatHistory{}
	for i := 0; i < 60; i++ {
		m.chatHistory.blocks = append(m.chatHistory.blocks,
			block{kind: blockUser, body: fmt.Sprintf("line %d", i)})
	}

	bottom := m.View() // renders once, publishing the real scroll ceiling
	if m.scrollLimit == nil {
		t.Fatal("View must publish a scroll ceiling")
	}
	limit := *m.scrollLimit
	if limit <= 0 {
		t.Fatalf("setup wrong: content should overflow the viewport, ceiling = %d", limit)
	}

	for i := 0; i < 400; i++ { // far past the top, the way trackpad momentum does
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
		m = next.(tuiModel)
	}
	if m.scrollOffset != limit {
		t.Fatalf("overscroll must clamp to the content ceiling: offset = %d, ceiling = %d", m.scrollOffset, limit)
	}

	top := m.View()
	if top == bottom {
		t.Fatal("setup wrong: the top and bottom of the transcript render identically")
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(tuiModel)
	if m.View() == top {
		t.Fatalf("the first scroll-down after an overshoot must move the transcript (offset = %d)", m.scrollOffset)
	}
}

// TestScrollCeilingShrinksWithTheContent guards the other half of the clamp: a
// ceiling published for a taller frame must not survive into a frame that fits
// on screen, or the stale limit would admit an offset this content cannot
// honour.
func TestScrollCeilingShrinksWithTheContent(t *testing.T) {
	m := newMouseTestTUI(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m = next.(tuiModel)
	m.chatHistory = &chatHistory{}
	for i := 0; i < 60; i++ {
		m.chatHistory.blocks = append(m.chatHistory.blocks,
			block{kind: blockUser, body: fmt.Sprintf("line %d", i)})
	}
	_ = m.View()
	if *m.scrollLimit <= 0 {
		t.Fatalf("setup wrong: content should overflow, ceiling = %d", *m.scrollLimit)
	}

	// The transcript shrinks until it fits: the ceiling has to follow it to 0.
	m.chatHistory = &chatHistory{blocks: []block{{kind: blockUser, body: "one line"}}}
	_ = m.View()
	if got := *m.scrollLimit; got != 0 {
		t.Fatalf("a transcript that fits must publish ceiling 0, got %d", got)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(tuiModel)
	if m.scrollOffset != 0 {
		t.Fatalf("nothing overflows, so scrolling must stay at 0, got %d", m.scrollOffset)
	}
}

package main

import (
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// TestMenuTrigger checks the popup opens only on a bare "/token": a slash with
// no arguments yet, and never on plain text or a command that already has args.
func TestMenuTrigger(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"/", true},
		{"/ta", true},
		{"/tasks", true},
		{"/tasks ", false}, // a space means arguments follow — close the popup
		{"/logs 42", false},
		{"hello", false},
		{"", false},
		{"!ls", false},
	}
	for _, c := range cases {
		if got := menuTrigger(c.in); got != c.want {
			t.Errorf("menuTrigger(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestMenuSyncFiltersAndRanks confirms the filter narrows the list and ranks
// prefix matches ahead of substring matches, with the table order as tie-break.
func TestMenuSyncFiltersAndRanks(t *testing.T) {
	mn := newSlashMenu(i18n.Locale("en"))
	mn.sync("/task")
	if !mn.active {
		t.Fatal("menu should be active for /task")
	}
	if len(mn.items) == 0 {
		t.Fatal("expected matches for /task")
	}
	// Both /tasks and /task exist; a prefix match must lead the list.
	if mn.items[0].name != "/tasks" && mn.items[0].name != "/task" {
		t.Fatalf("prefix match should lead, got %q", mn.items[0].name)
	}
	for _, it := range mn.items {
		if it.name == "/help" {
			t.Fatal("/help should not match filter \"task\"")
		}
	}

	// An empty filter (a bare slash) lists every command.
	mn.sync("/")
	if len(mn.items) != len(replCommands) {
		t.Fatalf("bare slash should list all %d commands, got %d", len(replCommands), len(mn.items))
	}

	// A space closes the popup.
	mn.sync("/tasks ")
	if mn.active || len(mn.items) != 0 {
		t.Fatalf("space should close the menu, active=%v items=%d", mn.active, len(mn.items))
	}
}

// TestMenuMoveClamps verifies selection stepping stays within the filtered list
// (no wraparound past either end).
func TestMenuMoveClamps(t *testing.T) {
	mn := newSlashMenu(i18n.Locale("en"))
	mn.sync("/")
	mn.move(-1) // already at top
	if mn.sel != 0 {
		t.Fatalf("move(-1) at top should stay 0, got %d", mn.sel)
	}
	mn.move(len(mn.items) + 5) // past the bottom
	if mn.sel != len(mn.items)-1 {
		t.Fatalf("move past bottom should clamp to %d, got %d", len(mn.items)-1, mn.sel)
	}
	if mn.selected() != mn.items[mn.sel].name {
		t.Fatalf("selected() = %q, want %q", mn.selected(), mn.items[mn.sel].name)
	}
}

// TestMenuOpensWhileTyping drives the idle key path: typing "/ta" opens the
// popup and filters it, and Esc dismisses it without clearing the line.
func TestMenuOpensWhileTyping(t *testing.T) {
	m := newTestTUI(t)
	m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	for _, r := range "/ta" {
		m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if !m.menu.active {
		t.Fatalf("menu should open while typing a slash token, value=%q", m.ta.Value())
	}
	if len(m.menu.items) == 0 {
		t.Fatal("menu should have filtered matches for /ta")
	}
	m = step(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.menu.active {
		t.Fatal("esc should dismiss the menu")
	}
	if m.ta.Value() != "/ta" {
		t.Fatalf("esc should keep the typed line, got %q", m.ta.Value())
	}
}

// TestSubmitSlashBlanksTheFrameBeforeExec is the regression test for the two
// input boxes a slash command used to leave behind. tea.Exec hands the terminal
// over by stopping the renderer, and stopping erases only the row the cursor sits
// on — so every row the last frame drew above it stays in scrollback, and the
// command's output printed under a stranded copy of the input box with a fresh
// one repainting below. The frame therefore has to be empty before the terminal
// is released, and the prompt has to come back when the command finishes.
func TestSubmitSlashBlanksTheFrameBeforeExec(t *testing.T) {
	m := newTestTUI(t)
	m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	next, cmd := m.submit("/help")
	nm := next.(tuiModel)
	if nm.mode != modeExec {
		t.Fatalf("a slash command runs in the foreground, not in mode %v", nm.mode)
	}
	if nm.quitting {
		t.Fatal("/help should not quit")
	}
	if cmd == nil {
		t.Fatal("slash command should return an exec command")
	}
	if v := nm.View(); v != "" {
		t.Fatalf("the frame must be empty before the terminal is released, got %q", v)
	}
	// While the command owns the terminal the keys are its own, not the model's.
	if after, _ := nm.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); after.(tuiModel).quitting {
		t.Fatal("ctrl+c during a foreground command must not quit the program")
	}
	back := step(nm, execDoneMsg{})
	if back.mode != modeIdle {
		t.Fatalf("the prompt should return when the command finishes, mode=%v", back.mode)
	}
	if !strings.Contains(back.View(), i18n.T(back.loc, "tui.input.placeholder")) {
		t.Fatalf("the input box should be back on screen: %q", back.View())
	}
}

// TestIsBareCommand covers the slash/shell vs. prompt routing decision.
func TestIsBareCommand(t *testing.T) {
	for _, in := range []string{"/help", "/tasks 1", "!ls", "!"} {
		if !isBareCommand(in) {
			t.Errorf("%q should be a bare command", in)
		}
	}
	for _, in := range []string{"hello", "explain PPO", ""} {
		if isBareCommand(in) {
			t.Errorf("%q should not be a bare command", in)
		}
	}
}

// TestMenuAnchorsUnderTheBox pins where the popup is drawn and what the footer
// says while it is open. Above the box, the list moved the input down a row per
// match as the filter widened, so the control being typed into drifted while the
// user typed; below it, the box holds its place. The legend has to follow the
// keys: with the list open enter runs the highlighted command, so advertising
// "ctrl+j newline" there would describe bindings that are not in force.
func TestMenuAnchorsUnderTheBox(t *testing.T) {
	m := newTestTUI(t)
	m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	for _, r := range "/ta" {
		m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	v := m.View()
	box, list := strings.Index(v, m.th.glyph("╭", "+")), strings.Index(v, "/tasks")
	if box < 0 || list < 0 {
		t.Fatalf("expected the box and the filtered list on screen: %q", v)
	}
	if box > list {
		t.Fatalf("the list must be drawn under the box, not above it: %q", v)
	}
	run := i18n.T(m.loc, "tui.hint.menuRun")
	if !strings.Contains(v, run) {
		t.Errorf("the footer should carry the menu's legend (%q): %q", run, v)
	}
	if nl := i18n.T(m.loc, "tui.hint.newline"); strings.Contains(v, nl) {
		t.Errorf("the idle legend (%q) should stand down while the menu is open: %q", nl, v)
	}
	if list > strings.LastIndex(v, run) {
		t.Errorf("the legend belongs under the list: %q", v)
	}
	// Dismissing the popup restores the editing legend.
	m = step(m, tea.KeyMsg{Type: tea.KeyEsc})
	if v := m.View(); !strings.Contains(v, i18n.T(m.loc, "tui.hint.newline")) {
		t.Errorf("the idle legend should return once the menu closes: %q", v)
	}
}

// TestMenuRowsFitTheWindow checks the list is capped by the terminal's spare
// height. An inline renderer repaints by counting rows back up from the cursor,
// so a frame taller than the window scrolls its own top away and every later
// repaint lands in the wrong place — the cap is what keeps the popup from
// growing the frame past the screen.
func TestMenuRowsFitTheWindow(t *testing.T) {
	m := newTestTUI(t)
	cases := []struct{ height, want int }{
		{0, 8},  // size not reported yet
		{40, 8}, // roomy: the scannability cap wins
		{12, 6}, // 12 - (blank + 3 box rows + footer + 1)
		{6, 3},  // tiny window: the floor keeps the list useful
	}
	for _, c := range cases {
		m.height = c.height
		if got := m.menuRows(); got != c.want {
			t.Errorf("menuRows() at height %d = %d, want %d", c.height, got, c.want)
		}
	}

	// A list longer than its window says how many are out of view, so the user
	// knows whether to keep typing or keep arrowing.
	mn := newSlashMenu(i18n.Locale("en"))
	mn.sync("/")
	out := mn.render(theme{loc: i18n.Locale("en")}, 100, 3)
	rows := strings.Split(out, "\n")
	if len(rows) != 4 {
		t.Fatalf("3 rows plus the overflow line, got %d: %q", len(rows), out)
	}
	want := i18n.Tf(i18n.Locale("en"), "tui.menu.more", "n", strconv.Itoa(len(mn.items)-3))
	if !strings.Contains(rows[3], want) {
		t.Errorf("overflow line should read %q: %q", want, rows[3])
	}
}

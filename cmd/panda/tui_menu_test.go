package main

import (
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/askengine"
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
	mn.sync("/task", nil)
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
	mn.sync("/", nil)
	if len(mn.items) != len(replCommands) {
		t.Fatalf("bare slash should list all %d commands, got %d", len(replCommands), len(mn.items))
	}

	// A space closes the popup.
	mn.sync("/tasks ", nil)
	if mn.active || len(mn.items) != 0 {
		t.Fatalf("space should close the menu, active=%v items=%d", mn.active, len(mn.items))
	}
}

// TestMenuMoveClamps verifies selection stepping stays within the filtered list
// (no wraparound past either end).
func TestMenuMoveClamps(t *testing.T) {
	mn := newSlashMenu(i18n.Locale("en"))
	mn.sync("/", nil)
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
	mn.sync("/", nil)
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

// TestMenuArgumentMode covers the popup's second life: past the first space it
// lists the argument candidates the resolver offers (locale codes for /lang),
// filters by the token being typed, and fill() rewrites that token in place.
// With no resolver it stays closed — the old behaviour for every command.
func TestMenuArgumentMode(t *testing.T) {
	mn := newSlashMenu(i18n.Locale("en"))
	resolve := func(cmd string, args []string) []string {
		if cmd == "lang" && len(args) == 1 {
			return localeCodeList()
		}
		return nil
	}
	mn.sync("/lang ", resolve)
	if !mn.active || !mn.argMode {
		t.Fatal("a space after /lang should open the argument list")
	}
	if len(mn.items) != len(i18n.Locales) {
		t.Fatalf("expected %d locale candidates, got %d", len(i18n.Locales), len(mn.items))
	}
	// A partial token filters the list case-insensitively.
	mn.sync("/lang z", resolve)
	if len(mn.items) != 1 || mn.items[0].name != "zh-CN" {
		t.Fatalf("filter \"z\" should leave zh-CN, got %v", mn.items)
	}
	// fill() rewrites only the token under the cursor, keeping the command.
	if got := mn.fill(); got != "/lang zh-CN " {
		t.Fatalf("fill() = %q, want %q", got, "/lang zh-CN ")
	}
	// The locale rows carry their endonym as the help column.
	mn.sync("/lang ", resolve)
	found := false
	for _, it := range mn.items {
		if it.name == "zh-CN" && it.desc == i18n.LocaleNames[i18n.ChineseSimp] {
			found = true
		}
	}
	if !found {
		t.Fatal("zh-CN should be glossed with its endonym")
	}
	// No resolver: the popup stays closed at the argument position.
	mn.sync("/lang ", nil)
	if mn.active {
		t.Fatal("nil resolver should leave the argument popup closed")
	}
}

// TestArgMenuTabFillsAndEnterSubmits drives the key path end to end: typing
// "/lang", Tab completes the command, the locale list opens, an arrow moves
// the selection, Tab fills the candidate into the line, and Enter submits the
// filled line as the command.
func TestArgMenuTabFillsAndEnterSubmits(t *testing.T) {
	m := newTestTUI(t)
	m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	for _, r := range "/lang" {
		m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m = step(m, tea.KeyMsg{Type: tea.KeyTab}) // complete the command name
	if m.ta.Value() != "/lang " {
		t.Fatalf("tab should complete to %q, got %q", "/lang ", m.ta.Value())
	}
	if !m.menu.active || !m.menu.argMode {
		t.Fatalf("the locale list should be open after the command completes, active=%v arg=%v", m.menu.active, m.menu.argMode)
	}
	if len(m.menu.items) != len(localeCodeList()) {
		t.Fatalf("expected the locale candidates, got %d", len(m.menu.items))
	}
	// Arrow to the second candidate, then Tab it into the line.
	m = step(m, tea.KeyMsg{Type: tea.KeyDown})
	want := "/lang " + m.menu.items[m.menu.sel].name + " "
	m = step(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.ta.Value() != want {
		t.Fatalf("tab should fill %q, got %q", want, m.ta.Value())
	}
	// Enter submits the filled line: the model hands the terminal to the
	// classic dispatch, so the mode is exec and the input is reset.
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeExec {
		t.Fatalf("enter should run the filled command, mode=%v", m.mode)
	}
	if m.ta.Value() != "" || m.menu.active {
		t.Fatalf("submit should clear the input and close the menu, value=%q active=%v", m.ta.Value(), m.menu.active)
	}
}

// TestApplyLocaleAfterLangCommand is the /lang regression: the handler changes
// the repl's locale, and the front end has to follow — theme labels, menu
// help and the input placeholder were captured at startup and used to stay in
// the old language while only the handler's printed output switched.
func TestApplyLocaleAfterLangCommand(t *testing.T) {
	m := newTestTUI(t)
	m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	if m.loc != i18n.English || m.r.loc != i18n.English {
		t.Fatal("test starts in English")
	}
	m.r.loc = i18n.ChineseSimp // what cmdLang does while the command runs
	back := step(m, execDoneMsg{})
	if back.loc != i18n.ChineseSimp {
		t.Fatalf("the model should adopt the repl's locale, got %q", back.loc)
	}
	if back.th.loc != i18n.ChineseSimp {
		t.Fatalf("the theme's label locale should follow, got %q", back.th.loc)
	}
	if back.ta.Placeholder != i18n.T(i18n.ChineseSimp, "tui.input.placeholder") {
		t.Fatalf("the placeholder should be re-resolved, got %q", back.ta.Placeholder)
	}
	if len(back.menu.all) == 0 || back.menu.all[0].desc != i18n.T(i18n.ChineseSimp, replCommands[0].help) {
		t.Fatal("the slash menu's help lines should be re-resolved in the new locale")
	}
}

// TestApprovalArrowSelection covers the approval card's arrow picker: the
// focus starts on deny (the [y/N] safe default), arrows toggle it, Enter
// answers the focused choice, and the y/n hotkeys keep working.
func TestApprovalArrowSelection(t *testing.T) {
	m := newTestTUI(t)
	m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.pending = &askengine.Result{
		Kind:          "task",
		NeedsApproval: true,
		Approval:      &askengine.ApprovalRequest{TaskID: "t1", Title: "rm -rf build"},
	}
	m.mode = modeApproving
	m.approvalSel = 1
	if m.approvalSel != 1 {
		t.Fatalf("focus should start on deny, got %d", m.approvalSel)
	}
	m = step(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.approvalSel != 0 {
		t.Fatalf("an arrow should move focus to approve, got %d", m.approvalSel)
	}
	// Enter on approve resumes the task: the mode returns to asking.
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeAsking {
		t.Fatalf("enter on approve should resume the task, mode=%v", m.mode)
	}
}

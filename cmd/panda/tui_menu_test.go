package main

import (
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

// TestSubmitSlashRoutesToExec confirms a slash command submits into the
// foreground exec path (a command is returned) and does not enter asking mode.
func TestSubmitSlashRoutesToExec(t *testing.T) {
	m := newTestTUI(t)
	next, cmd := m.submit("/help")
	nm := next.(tuiModel)
	if nm.mode != modeIdle {
		t.Fatalf("a slash command should not enter asking mode, got %v", nm.mode)
	}
	if nm.quitting {
		t.Fatal("/help should not quit")
	}
	if cmd == nil {
		t.Fatal("slash command should return an exec command")
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

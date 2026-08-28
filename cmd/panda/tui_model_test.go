package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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

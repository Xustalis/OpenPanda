package main

// Launch glue for the full-screen interactive front end. runTUI builds the
// Bubble Tea program over an already-constructed repl (engine, stores, locale)
// and runs it inline — committed turns flow into scrollback, so the terminal
// keeps the conversation after exit. shouldUseTUI decides when `panda` /
// `panda repl` open this front end instead of the classic line loop.

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

// shouldUseTUI reports whether the rich TUI should drive this session. It needs
// an interactive TTY and a configured engine (the TUI is a chat front end; with
// no model endpoint the classic command loop is the right fallback). The
// PANDA_CLASSIC_REPL escape hatch forces the old line editor — a safety valve
// while the TUI matures and for scripted/e2e use that expects the plain loop.
func shouldUseTUI(r *repl) bool {
	if os.Getenv("PANDA_CLASSIC_REPL") != "" {
		return false
	}
	return r.interactive && stdoutIsTTY()
}

// runTUI runs the Bubble Tea program to completion in the alternate screen
// buffer. By default the terminal retains mouse ownership (mouseSelect) so
// drag-select, double-click, and terminal copy work out of the box, with wheel
// scrolling handled via alternate scroll (DECSET 1007). Mouse cell motion is
// enabled only when mouseScroll is active (via config, PANDA_MOUSE, or ctrl+t).
func runTUI(r *repl) {
	if c := loadConvo(); len(c) > 0 {
		r.convo = c
	}
	model := newTUIModel(r)

	opts := []tea.ProgramOption{
		tea.WithAltScreen(),
	}
	if model.mouse.captured() {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	p := tea.NewProgram(model, opts...)
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "panda: "+err.Error())
	}
	// Leave the flag as we found it; it is inert outside the alt screen, but a
	// terminal we hand back to the shell should not keep our leftovers.
	fmt.Fprint(os.Stdout, altScrollOff)
}

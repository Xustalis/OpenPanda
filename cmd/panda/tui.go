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

// runTUI runs the Bubble Tea program to completion in the alternate screen buffer.
func runTUI(r *repl) {
	if c := loadConvo(); len(c) > 0 {
		r.convo = c
	}
	p := tea.NewProgram(newTUIModel(r), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "panda: "+err.Error())
	}
}

// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

package main

// Launch glue for the full-screen interactive front end. runTUI builds the
// Bubble Tea program over an already-constructed repl (engine, stores, locale)
// and runs it in the alternate screen: View repaints the transcript from
// chatHistory each frame, so the conversation is on screen for the life of the
// program. Quitting restores the shell's previous screen and prints where the
// conversation was saved. shouldUseTUI decides when `panda` / `panda repl` open
// this front end instead of the classic line loop.

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// shouldUseTUI reports whether the rich TUI should drive this session. It needs
// an interactive TTY; an install with no configured model still enters the TUI,
// where the onboarding/model wizard handles setup. The PANDA_CLASSIC_REPL escape
// hatch forces the old line editor — a safety valve for scripted/e2e use that
// expects the plain loop.
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
func runTUI(r *repl) error {
	// The thread this sitting resumes (or the legacy bare conversation, when no
	// store could be bound) was already prepared by runRepl — see
	// prepareConversation — so the transcript can be seeded below.
	// Bubble Tea owns the terminal now: any classic path that still reached the
	// raw line editor (a y/N confirm, the interrupt watcher, the history list)
	// would steal keys out from under the input loop and write straight over
	// the painted frame. Nil the session so those paths decline instead.
	r.term = nil
	model := newTUIModel(r)

	opts := []tea.ProgramOption{
		tea.WithAltScreen(),
	}
	if model.mouse.captured() {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	p := tea.NewProgram(model, opts...)
	// Ask the terminal to disambiguate Enter from modified Enter (kitty
	// keyboard protocol, flag 1): Terminals that support it send Shift+Enter as
	// CSI 13;2u, which Update maps to a newline. Terminals that do not ignore
	// the push, and Alt+Enter / Ctrl+J still reach us the classic ways.
	if stdoutIsTTY() {
		fmt.Fprint(os.Stdout, "\x1b[>1u")
	}
	// Leave the terminal as we found it — even if Run panics: pop the keyboard
	// flags and release alternate scroll, so the shell does not inherit our
	// leftovers.
	defer func() {
		if stdoutIsTTY() {
			fmt.Fprint(os.Stdout, "\x1b[<u")
		}
		fmt.Fprint(os.Stdout, altScrollOff)
	}()
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "panda: "+err.Error())
		// A panic or an abnormal kill is not a clean quit. Exiting 0 here made
		// a crashed TUI look successful to a shell, a supervisor, or a script
		// that had just run `panda`.
		if sid := r.sessID(); sid != "" {
			fmt.Println(pal().Muted(i18n.Tf(r.locale(), "tui.exit.session", "id", sid)))
		}
		return err
	}
	// Restoring the main screen makes the conversation look lost, so say where
	// it went: a bound session persists in the store (resume by id), a bare
	// chat lives in the convo file and reloads on the next launch.
	if sid := r.sessID(); sid != "" {
		fmt.Println(pal().Muted(i18n.Tf(r.locale(), "tui.exit.session", "id", sid)))
	} else if r.convoLen() > 0 {
		fmt.Println(pal().Muted(i18n.T(r.locale(), "tui.exit.bare")))
	}
	return nil
}

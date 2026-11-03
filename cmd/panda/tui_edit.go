// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

package main

// /edit inside the full-screen front end. The line-oriented REPL can hand the
// terminal to an editor and simply block; Bubble Tea owns the terminal, so the
// handover has to go through ExecProcess, which restores the alternate screen
// and repaints the transcript when the editor exits.

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// editDoneMsg reports that the editor returned. err is its exit error, if any.
type editDoneMsg struct{ err error }

// editFile opens /edit's target in the user's editor at the requested line.
func (m tuiModel) editFile(arg string) (tea.Model, tea.Cmd) {
	if m.r == nil {
		return m, m.printBlock(block{kind: blockError, body: i18n.T(m.loc, "repl.edit.failed")})
	}
	path, line, err := editTarget(m.r, arg)
	if err != nil {
		body := err.Error()
		if body == "usage" {
			body = i18n.T(m.loc, "repl.edit.usage")
		}
		return m, m.printBlock(block{kind: blockNote, body: body})
	}
	cmd, err := editorCommand(path, line)
	if err != nil {
		return m, m.printBlock(block{kind: blockError, body: err.Error()})
	}
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg { return editDoneMsg{err: err} })
}

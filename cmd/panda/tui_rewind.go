// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

package main

// /rewind: put the thread and its worktree back to a finished turn.
//
// This is the terminal half of internal/sessions/checkpoint.go. The list is
// newest-first because the reason anyone reaches for a rewind is almost always
// "undo the last turn", and the confirmation is a card rather than a second
// Enter: a rewind deletes work, and a key that deletes work must not be the
// same key that navigates a list.

import (
	"context"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// openRewindList shows the turns this thread can return to.
func (m tuiModel) openRewindList() (tea.Model, tea.Cmd) {
	r := m.r
	if r == nil || r.sessionsSt == nil {
		return m, m.printBlock(block{kind: blockNote, body: i18n.T(m.loc, "tui.rewind.noSession")})
	}
	id := r.sessID()
	if id == "" {
		return m, m.printBlock(block{kind: blockNote, body: i18n.T(m.loc, "tui.rewind.noSession")})
	}
	sess, err := r.sessionsSt.Get(id)
	if err != nil {
		return m, m.printBlock(block{kind: blockError, body: err.Error()})
	}
	cps, err := r.sessionsSt.Checkpoints(context.Background(), id)
	if err != nil {
		return m, m.printBlock(block{kind: blockError, body: err.Error()})
	}
	if len(cps) == 0 {
		return m, m.printBlock(block{kind: blockNote, body: i18n.T(m.loc, "tui.rewind.none")})
	}

	items := make([]SelectionItem, 0, len(cps))
	for i := len(cps) - 1; i >= 0; i-- { // newest first
		cp := cps[i]
		item := SelectionItem{
			ID:      strconv.Itoa(cp.Turn),
			Index:   len(items) + 1,
			Turns:   cp.Turn,
			Title:   rewindQuestion(sess, cp.Turn),
			Snippet: i18n.Tf(m.loc, "tui.rewind.item", "n", strconv.Itoa(cp.Turn)),
			Value:   cp.Turn,
		}
		if cp.Turn > 0 && cp.Turn <= len(sess.Turns) {
			if ts := sess.Turns[cp.Turn-1].TS; ts > 0 {
				item.Time = time.Unix(ts, 0).Format("2006-01-02 15:04")
			}
		}
		items = append(items, item)
	}

	sl := NewSelectionList(i18n.T(m.loc, "tui.rewind.title"), items)
	sl.EmptyText = i18n.T(m.loc, "tui.rewind.none")
	m.mode = modeList
	m.listKind = listRewind
	m.selectionList = sl
	return m, nil
}

// confirmRewind asks before deleting work, and carries the target turn.
func (m tuiModel) confirmRewind(turn int) (tea.Model, tea.Cmd) {
	m.mode = modeConfirm
	m.confirmText = i18n.Tf(m.loc, "tui.rewind.confirm", "n", strconv.Itoa(turn))
	m.confirmRun = func(nm *tuiModel) (tea.Model, tea.Cmd) {
		return nm.doRewind(turn)
	}
	return m, nil
}

// doRewind performs the rewind and repaints the transcript from what the store
// now holds — the blocks after the target turn are gone from both places, and
// the screen has to agree with the file.
func (m *tuiModel) doRewind(turn int) (tea.Model, tea.Cmd) {
	r := m.r
	if r == nil || r.sessionsSt == nil {
		return *m, nil
	}
	id := r.sessID()
	if id == "" {
		return *m, m.printBlock(block{kind: blockNote, body: i18n.T(m.loc, "tui.rewind.noSession")})
	}
	ctx := context.Background()
	res, err := r.sessionsSt.Rewind(ctx, id, turn)
	if err != nil {
		return *m, m.printBlock(block{kind: blockError, body: err.Error()})
	}
	// The turns past the rewind no longer exist, so neither do their snapshots.
	// Leaving them would offer targets for a history that is gone.
	r.sessionsSt.DropCheckpointsAfter(ctx, id, res.Turn)

	sess, err := r.sessionsSt.Get(id)
	if err != nil {
		return *m, m.printBlock(block{kind: blockError, body: err.Error()})
	}
	m.reloadTranscript(sess)
	m.mode = modeIdle
	m.liveAnswerChunks = nil
	m.liveAnswerBytes = 0
	m.thought = nil
	m.liveTask = nil
	m.pending = nil

	var note string
	switch {
	case !res.Files:
		note = i18n.Tf(m.loc, "tui.rewind.doneChatOnly", "n", strconv.Itoa(res.Turn))
	case len(res.Removed) > 0:
		note = i18n.Tf(m.loc, "tui.rewind.doneFiles", "n", strconv.Itoa(res.Turn),
			"removed", strconv.Itoa(len(res.Removed)))
	default:
		note = i18n.Tf(m.loc, "tui.rewind.done", "n", strconv.Itoa(res.Turn))
	}
	return *m, m.printBlock(block{kind: blockNote, body: note})
}

// reloadTranscript rebuilds the committed transcript from a session, the same
// way attaching one does — the rewind path needs the blocks replaced, not
// appended to.
func (m *tuiModel) reloadTranscript(sess *sessions.Session) {
	if m.chatHistory == nil || sess == nil {
		return
	}
	m.chatHistory.clear()
	for _, t := range sess.Turns {
		switch t.Role {
		case "user":
			m.chatHistory.blocks = append(m.chatHistory.blocks, block{kind: blockUser, body: t.Text})
		case "assistant":
			if strings.TrimSpace(t.Text) == "" {
				continue
			}
			kind := blockAnswer
			switch t.Kind {
			case "error":
				kind = blockError
			case "task":
				kind = blockTask
			}
			m.chatHistory.blocks = append(m.chatHistory.blocks, block{kind: kind, body: t.Text})
		}
	}
	m.scrollOffset = 0
}

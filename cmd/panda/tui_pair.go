// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

package main

// The TUI's half of "somebody is knocking".
//
// A pairing request used to be visible only to a reader sitting in the web
// console's pairing panel. On a headless box — the case this whole feature was
// asked for — nobody is, and the request expires in four minutes while the
// other human watches "waiting for the other device".
//
// So the request becomes ambient: one line above the composer for as long as
// it is answerable, one block in the transcript the first time it appears (so
// scrolling back still finds it), and `/pair` to answer it. No modal, no focus
// steal: a terminal has no "steal focus politely", and a card that swallows
// keystrokes mid-sentence is worse than the request it announces.

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// pendingPairs reads the answerable inbound requests. Errors are swallowed
// into "nothing waiting": this runs on the watcher's two-second beat, and a
// transient read failure must not clear a banner the operator is reading or
// print a line every two seconds. The store error surfaces where it matters —
// when the request is actually answered.
func pendingPairs(r *repl) []core.PairPendingRow {
	if r == nil || r.db == nil {
		return nil
	}
	rows, err := core.PairPending(r.db)
	if err != nil {
		return nil
	}
	return rows
}

// adoptPairs folds one poll's worth of requests into the model and returns the
// commands that fold produced (at most one: the first-time transcript note).
//
// The banner tracks the newest request; the note fires once per id, not once
// per poll. Without that guard the watcher would print the same notice every
// two seconds for four minutes.
func (m *tuiModel) adoptPairs(rows []core.PairPendingRow) []tea.Cmd {
	if len(rows) == 0 {
		m.pairReq = nil
		m.pairNoted = ""
		return nil
	}
	first := rows[0]
	m.pairReq = &first
	if m.pairNoted == first.ID {
		return nil
	}
	m.pairNoted = first.ID
	name := pairDisplayName(first.PeerName, first.PeerAddr)
	body := i18n.Tf(m.loc, "tui.pair.notice", "name", name, "code", first.SAS) +
		"\n" + i18n.T(m.loc, "tui.pair.noticeHow")
	return []tea.Cmd{m.printBlock(block{kind: blockNote, body: body})}
}

// pairBanner is the composer's one-line notice. It sits above the input box
// rather than in the status row because the status row is already the "what
// will this prompt do" line and it sheds content on narrow terminals — a
// pairing request must not be the thing that gets shed.
func (m tuiModel) pairBanner() string {
	if m.pairReq == nil {
		return ""
	}
	name := pairDisplayName(m.pairReq.PeerName, m.pairReq.PeerAddr)
	line := i18n.Tf(m.loc, "tui.pair.banner", "name", name, "code", m.pairReq.SAS)
	if left := pairCountdown(m.pairReq.ExpiresAt, time.Now()); left != "" {
		line += " · " + left
	}
	line += " · " + i18n.T(m.loc, "tui.pair.bannerCmd")
	return m.th.warn.Render(m.th.glyph("●", "*") + " " + line)
}

// pairCommand implements `/pair` for the TUI: bare opens the decision card for
// the newest request, `confirm <id>` / `reject <id>` answer without a card.
//
// It answers through the same core call the CLI and the console use, so all
// three front ends flip the one row the daemon's waitPairConfirm goroutine is
// watching — there is no second channel and no way for two of them to disagree.
func (m tuiModel) pairCommand(arg string) (tea.Model, tea.Cmd) {
	if m.r == nil || m.r.db == nil {
		return m, m.printBlock(block{kind: blockError, body: i18n.T(m.loc, "repl.pair.noStore")})
	}
	fields := strings.Fields(arg)
	if len(fields) > 0 {
		switch strings.ToLower(fields[0]) {
		case "confirm", "yes", "y":
			return m.pairDecide(strings.Join(fields[1:], " "), true)
		case "reject", "no", "n":
			return m.pairDecide(strings.Join(fields[1:], " "), false)
		}
	}

	rows := pendingPairs(m.r)
	if len(rows) == 0 {
		return m, m.printBlock(block{kind: blockNote, body: i18n.T(m.loc, "tui.pair.none")})
	}
	req := rows[0]
	m.pairReq = &req
	name := pairDisplayName(req.PeerName, req.PeerAddr)
	line := i18n.Tf(m.loc, "tui.pair.card", "name", name, "code", req.SAS)
	if left := pairCountdown(req.ExpiresAt, time.Now()); left != "" {
		line += " · " + left
	}
	m.mode = modeConfirm
	m.confirmText = line + "\n" + i18n.T(m.loc, "tui.pair.cardHint")
	// The card's [y] runs this. Enter and Esc both decline — onConfirmKey's
	// rule, and the safe direction for a decision that hands over a secret.
	m.confirmRun = func(mm *tuiModel) (tea.Model, tea.Cmd) {
		return mm.pairDecide(req.ID, true)
	}
	return m, nil
}

// pairDecide flips one request and reports the outcome. The card is already
// gone by the time this runs (onConfirmKey leaves the mode before calling the
// callback), so this owns the whole visible result.
func (m tuiModel) pairDecide(id string, confirm bool) (tea.Model, tea.Cmd) {
	if m.r == nil || m.r.db == nil {
		return m, m.printBlock(block{kind: blockError, body: i18n.T(m.loc, "repl.pair.noStore")})
	}
	id = strings.TrimSpace(id)
	if id == "" {
		// Bare `/pair` never lands here (it opens the card); an explicit verb
		// with no id has to say what it wants instead of guessing.
		return m, m.printBlock(block{kind: blockError, body: i18n.T(m.loc, "repl.pair.needID")})
	}
	state := core.PairStateRejected
	if confirm {
		state = core.PairStateConfirmed
	}
	if err := core.AnswerPairSession(m.r.db, id, state); err != nil {
		return m, m.printBlock(block{kind: blockError, body: err.Error()})
	}
	m.pairReq = nil
	m.pairNoted = ""
	body := i18n.T(m.loc, "tui.pair.rejected")
	if confirm {
		body = i18n.T(m.loc, "tui.pair.confirmed")
	}
	return m, m.printBlock(block{kind: blockNote, body: body})
}

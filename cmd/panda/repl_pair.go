// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// `/pair` — the responder's side of the pairing ceremony, from inside the
// chat shell.
//
// The ceremony is symmetrical only in appearance: the machine that asks runs
// `panda pair <target>` and blocks on a yes/no at the other end, while the
// machine being asked has exactly one job — show the same six digits and say
// yes or no within four minutes. `panda pair` (the CLI verb) does that, but a
// reader living in the TUI or the REPL should not have to leave the shell to
// press it, and more to the point the front end that *noticed* the request is
// the front end that can answer it.
//
// This file holds the parts both front ends share: the listing, the answer,
// and the countdown. tui_pair.go adds the live banner and the yes/no card on
// top; the classic REPL prints the listing straight to the terminal.

import (
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// pairDisplayName is who the request is from, as the operator should read it:
// the advertised name when one came with the hello, the address otherwise.
// The pairing hello always carries an address, and a request identified only
// by "unknown" is useless when two machines are involved.
func pairDisplayName(peerName, peerAddr string) string {
	if strings.TrimSpace(peerName) != "" {
		return peerName
	}
	return peerAddr
}

// pairCountdown renders the request's remaining life as m:ss, empty once it
// is spent — the row has already expired by then, and "0:00" would look like
// a session still worth answering.
func pairCountdown(expires time.Time, now time.Time) string {
	left := int(expires.Sub(now).Round(time.Second).Seconds())
	if left <= 0 {
		return ""
	}
	return strconv.Itoa(left/60) + ":" + pad2(left%60)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// pairShortID is what the operator types back: the row id is a 32-hex session
// id, and the leading eight characters are unique in practice (the CLI's
// AnswerPairSession matches on prefix for the same reason).
func pairShortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// pairList prints the requests still waiting on this node, newest first.
func (r *repl) pairList() {
	if r.db == nil {
		r.outln(i18n.T(r.loc, "repl.pair.noStore"))
		return
	}
	rows, err := core.PairPending(r.db)
	if err != nil {
		r.outln(err.Error())
		return
	}
	if len(rows) == 0 {
		r.outln(i18n.T(r.loc, "repl.pair.none"))
		return
	}
	now := time.Now()
	r.outln(i18n.T(r.loc, "repl.pair.head"))
	for _, s := range rows {
		r.outln("  " + i18n.Tf(r.loc, "repl.pair.row",
			"id", pairShortID(s.ID),
			"name", pairDisplayName(s.PeerName, s.PeerAddr),
			"code", s.SAS,
			"left", pairCountdown(s.ExpiresAt, now)))
	}
	r.outln(i18n.T(r.loc, "repl.pair.howto"))
}

// pairAnswer flips one request. `idPrefix` may be the short id the listing
// printed; AnswerPairSession resolves the prefix and refuses an ambiguous or
// already-resolved one.
func (r *repl) pairAnswer(idPrefix string, confirm bool) {
	if r.db == nil {
		r.outln(i18n.T(r.loc, "repl.pair.noStore"))
		return
	}
	idPrefix = strings.TrimSpace(idPrefix)
	if idPrefix == "" {
		r.outln(i18n.T(r.loc, "repl.pair.needID"))
		return
	}
	state := core.PairStateRejected
	if confirm {
		state = core.PairStateConfirmed
	}
	if err := core.AnswerPairSession(r.db, idPrefix, state); err != nil {
		r.outln(err.Error())
		return
	}
	if confirm {
		r.outln(i18n.T(r.loc, "repl.pair.confirmed"))
		return
	}
	r.outln(i18n.T(r.loc, "repl.pair.rejected"))
}

// cmdPair is the `/pair` handler: bare lists, `confirm <id>` and
// `reject <id>` answer.
//
// There is no "answer the bare id" shorthand on purpose. Confirming hands the
// mesh secret to the other machine, and a mistyped token should print usage,
// not silently admit a device — the two verbs are the whole vocabulary.
func (r *repl) cmdPair(arg string) {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		r.pairList()
		return
	}
	switch strings.ToLower(fields[0]) {
	case "confirm", "yes", "y":
		r.pairAnswer(strings.Join(fields[1:], " "), true)
	case "reject", "no", "n":
		r.pairAnswer(strings.Join(fields[1:], " "), false)
	default:
		r.outln(i18n.T(r.loc, "repl.pair.usage"))
	}
}

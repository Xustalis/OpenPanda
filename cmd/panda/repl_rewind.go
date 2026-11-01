// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// /rewind in the line-oriented REPL. The full-screen front end has a picker and
// a confirmation card (tui_rewind.go); here the transcript is the terminal's own
// scrollback, so the list is printed and the turn number is typed back.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// cmdRewind implements "/rewind [turn]".
func (r *repl) cmdRewind(arg string) {
	out := r.commandOutput()
	id := r.sessID()
	if id == "" || r.sessionsSt == nil {
		fmt.Fprintln(out, i18n.T(r.loc, "tui.rewind.noSession"))
		return
	}
	ctx := context.Background()

	if n, err := strconv.Atoi(strings.TrimSpace(arg)); err == nil && n > 0 {
		res, err := r.sessionsSt.Rewind(ctx, id, n)
		if err != nil {
			fmt.Fprintf(out, "%s: %v\n", i18n.T(r.loc, "tui.rewind.failed"), err)
			return
		}
		r.sessionsSt.DropCheckpointsAfter(ctx, id, res.Turn)
		switch {
		case !res.Files:
			fmt.Fprintln(out, i18n.Tf(r.loc, "tui.rewind.doneChatOnly", "n", strconv.Itoa(res.Turn)))
		case len(res.Removed) > 0:
			fmt.Fprintln(out, i18n.Tf(r.loc, "tui.rewind.doneFiles", "n", strconv.Itoa(res.Turn),
				"removed", strconv.Itoa(len(res.Removed))))
		default:
			fmt.Fprintln(out, i18n.Tf(r.loc, "tui.rewind.done", "n", strconv.Itoa(res.Turn)))
		}
		return
	}

	cps, err := r.sessionsSt.Checkpoints(ctx, id)
	if err != nil {
		fmt.Fprintf(out, "%s: %v\n", i18n.T(r.loc, "tui.rewind.failed"), err)
		return
	}
	if len(cps) == 0 {
		fmt.Fprintln(out, i18n.T(r.loc, "tui.rewind.none"))
		return
	}
	sess, _ := r.sessionsSt.Get(id)
	fmt.Fprintln(out, i18n.T(r.loc, "tui.rewind.title"))
	for i := len(cps) - 1; i >= 0; i-- {
		turn := cps[i].Turn
		question := ""
		if sess != nil {
			question = rewindQuestion(sess, turn)
		}
		fmt.Fprintf(out, "  %s  %s\n", i18n.Tf(r.loc, "tui.rewind.item", "n", strconv.Itoa(turn)), question)
	}
	fmt.Fprintf(out, "\n%s\n", i18n.Tf(r.loc, "tui.rewind.hint", "n", strconv.Itoa(cps[len(cps)-1].Turn)))
}

// rewindQuestion names a checkpoint by the prompt that started it: "the last
// thing I asked before this" is how a person finds the turn they want back,
// and a bare message count is not. Shared with the full-screen picker, which is
// why it lives in the untagged file.
func rewindQuestion(sess *sessions.Session, turn int) string {
	if sess == nil || turn <= 0 || turn > len(sess.Turns) {
		return ""
	}
	for i := turn - 1; i >= 0; i-- {
		t := sess.Turns[i]
		if t.Role != "user" {
			continue
		}
		text := strings.Join(strings.Fields(t.Text), " ")
		if runes := []rune(text); len(runes) > 48 {
			text = string(runes[:48]) + "…"
		}
		return text
	}
	return ""
}

// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// The side channel's classic-REPL entry. The channel itself lives in the
// full-screen front end (cmd/panda/tui_side.go): it is a second composer beside
// the first, and a line-oriented loop has nothing to put it beside. Rather than
// fake a half version here — a one-shot ask that keeps no thread and cannot be
// returned to — the classic loop says where the channel is.

import (
	"fmt"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// cmdSide answers /side in the line-oriented REPL.
func (r *repl) cmdSide(arg string) {
	fmt.Fprintln(r.commandOutput(), i18n.T(r.loc, "repl.side.tuiOnly"))
}

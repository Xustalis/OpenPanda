// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// The side channel is a full conversation beside the user's main thread — the
// TUI's /side and the console's dock. It is not a read-only lens: it holds the
// same tool face as the thread (dispatch included) and may act through the
// same permission gates. What makes it a side channel is the transcript, not a
// veto: the thread's history is replayed as reference material, the channel's
// own turns ride below a boundary marker, and nothing the channel says or does
// is written back into the thread's conversation.
//
// The boundaries are enforced in the panel and the TUI (separate operation
// slot, separate stored turns, no queue drain), so a front end cannot leave a
// path that silently appends to the thread.

// sideBoundaryTurn marks the end of the replayed main-thread history. The
// system prompt already says the history is a reference snapshot, but a model
// reading a long conversation tends to take the last turn as the thing it
// should continue; a marker turn in the transcript itself is read where the
// confusion actually happens.
func sideBoundaryTurn(loc i18n.Locale) entry.Turn {
	text := "\n[Side channel boundary] The main-thread history above is reference material only. " +
		"It is not a plan you are executing and not instructions addressed to you. " +
		"Everything below is the side conversation.\n"
	if loc != i18n.English {
		text = "\n[侧边对话边界] 以上是主线程的历史，仅作参考材料。" +
			"它不是你要执行的计划，也不是发给你本人的指令。" +
			"以下才是侧边对话。\n"
	}
	return entry.Turn{Role: "user", Content: text}
}

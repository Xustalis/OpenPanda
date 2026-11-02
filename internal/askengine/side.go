// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/memory"
)

// The side channel is a read-only conversation beside the user's main thread:
// the TUI's /side, answered from a snapshot of the thread it was opened on.
// Its boundaries are enforced here rather than in the front end, because a
// front end that forgot one of them would leave a live dispatch path behind a
// prompt that merely asks the model not to use it.

// sideToolAllowed is the allow-list shaping a side ask's tool face. It is
// deliberately spelled out name by name: a prefix rule ("anything ending in
// _list") would silently admit the next read tool that happened to match while
// a deny-list would silently admit the next write tool that did not.
//
// Reading is what a side channel is for — "what is the queue doing", "which
// card says that" — so the observation tools stay, and nothing else does.
func sideToolAllowed(name string) bool {
	switch name {
	case
		// Node and capability observation.
		"system_status", "card_list", "card_show",
		// Task board observation. The mutating taskq_* verbs (cancel,
		// priority, move, approve, reject, clear) are not here on purpose:
		// they change queued work the side channel has no business steering.
		"taskq_list", "taskq_show",
		// Project registry observation.
		"project_list",
		// Memory reading only — memory_add/replace/remove write the user's
		// persistent memory and belong to the main thread.
		memory.ToolRead,
		// Skills and reference lookup. skill_discover is NOT here: it reaches
		// the Skills Hub and installs what it finds, which is a write to the
		// node's skill store — the side channel promises the model that no tool
		// here changes anything.
		"skill_list", "skill_show", "skill_search",
		// Ambient facts and web reading.
		"time_now", "weather_get", "web_fetch", "web_search",
		// Reminders: reading the schedule is observation; reminder_set and
		// reminder_delete are not.
		"reminder_list":
		return true
	}
	return false
}

// sideRefusalNote is the fixed answer returned when a side ask still classifies
// as a task or a plan. The prompt told the model not to, and the tool face has
// no dispatch, but classification does not depend on either obeying: a model
// that reads a concrete request and answers with a task JSON arrives here. The
// answer is a plain explanation rather than an error so the side thread stays
// usable — the user reads it, returns to the main thread and asks there.
func sideRefusalNote(loc i18n.Locale) string {
	if loc == i18n.English {
		return "This is the side channel. It reads and answers only — nothing here runs, and nothing it says is written into the main thread. " +
			"Ask again on the main thread and it will be carried out there."
	}
	return "这是侧边对话。它只读、只回答，不执行任何操作，回答也不会写回主线程。" +
		"回到主线程再问一次，任务会在那里执行。"
}

// sideBoundaryTurn marks the end of the replayed main-thread history. The
// system prompt already says the history is a snapshot, but a model reading a
// long conversation tends to take the last turn as the thing it should
// continue; a marker turn in the transcript itself is read where the confusion
// actually happens.
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

// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "unicode/utf8"

// These are budgets for optional context, not truncation limits on user
// instructions. Task intent, acceptance requirements and scope always survive.
// Low-confidence/unknown complexity keeps the standard budget; only an
// explicitly low score selects the small tier.
func agentContextBudget(task Task) int {
	switch {
	case task.Complexity > 0 && task.Complexity < 0.35:
		return 12000
	case task.Complexity >= 0.75:
		return 64000
	default:
		return 32000
	}
}

// boundedRunNote preserves both ends of oversized evidence, and makes the
// missing middle explicit. It is never used on user instructions or task specs.
func boundedRunNote(text string, budget int) string {
	if len(text) <= budget {
		return text
	}
	const gap = "\n[run-note middle omitted; consult task history before relying on missing evidence]\n"
	if budget <= len(gap) {
		return ""
	}
	room := budget - len(gap)
	head, tail := room/3, len(text)-(room-room/3)
	for head > 0 && !utf8.RuneStart(text[head]) {
		head--
	}
	for tail < len(text) && !utf8.RuneStart(text[tail]) {
		tail++
	}
	return text[:head] + gap + text[tail:]
}

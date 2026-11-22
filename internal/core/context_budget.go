// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "unicode/utf8"

// The two complexity thresholds the runtime reads. They are named because
// three separate decisions turn on them — how much optional context a run
// gets, which agent the route prefers, and whether the work is described to
// the reader as light or deep — and those must agree: an agent chosen for a
// "deep" task must not be handed the "light" context budget.
const (
	complexityLight = 0.35
	complexityDeep  = 0.75
)

// These are budgets for optional context, not truncation limits on user
// instructions. Task intent, acceptance requirements and scope always survive.
// Low-confidence/unknown complexity keeps the standard budget; only an
// explicitly low score selects the small tier.
func agentContextBudget(task Task) int {
	switch {
	case task.Complexity > 0 && task.Complexity < complexityLight:
		return 12000
	case task.Complexity >= complexityDeep:
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

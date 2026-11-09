// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

// Tool-loop context budgeting.
//
// A "look at this repo" ask once ran fourteen workspace_read/workspace_list
// calls. Every full result was appended to the replayed turns and every
// subsequent classification sent the whole transcript again. By the sixth
// round the request carried the better part of a hundred KiB of file dumps;
// the next call failed, and the loop's error path swallowed the failure and
// answered "the model stopped responding after 14 tool operations". The loop
// had a round ceiling (maxRounds) but no input ceiling.
//
// Two bounds close that hole:
//   - toolResultHardCap caps one individual result before it is replayed;
//   - turnBudgetForWindow caps the total tool-result text across the whole
//     transcript, derived from the serving model's advertised context window.
//
// fitToolTurns enforces the second bound by shrinking the OLDEST tool results
// first: the newest observation is what the model still needs to answer, so it
// stays intact until every older one has been folded down. User and assistant
// prose is never touched — only tool_result blocks and the "Tool result: "
// text pairs the id-less fallback path creates.

import (
	"strings"
	"unicode/utf8"

	"github.com/Xustalis/OpenPanda/internal/entry"
)

const (
	// toolResultHardCap bounds one result in runes. A workspace_read can return
	// 64 KiB on its own; no single observation may dominate a prompt.
	toolResultHardCap = 12000
	// toolDigestCap bounds the copy kept for the interruption digest. The
	// digest itself prints only 300 runes per line, so retaining a 64 KiB
	// file dump per call buys nothing but a growing allocation.
	toolDigestCap = 2000
	// Bounds for the whole transcript's tool-result text. Unknown context
	// windows fall back to the middle value rather than to "unbounded".
	toolTurnMinBudget     = 12000
	toolTurnMaxBudget     = 64000
	toolTurnUnknownWindow = 32000
	// The first pass keeps a short head+tail of an old result; the second
	// folds it down to this one-line marker when the transcript is still over.
	toolStagedLimit = 1500
	// Both markers are read by the model, not the user: ASCII on purpose, so
	// they need no locale twin and cannot break a UTF-8 boundary.
	toolOmissionMarker = "\n[... middle of earlier tool output omitted - re-run the tool if needed ...]\n"
	toolFoldMarker     = "[earlier tool output omitted - re-run the tool if needed]"
)

// turnBudgetForWindow converts the serving model's advertised context window
// (tokens) into a rune budget for tool results. The estimate is deliberately
// pessimistic: ~1.25 runes per token for mixed CJK/ASCII, with only 40% of the
// window available to tool results — the system prompt, replayed conversation,
// tool schemas and the answer take the rest. That product is window/2.
func turnBudgetForWindow(window int) int {
	if window <= 0 {
		return toolTurnUnknownWindow
	}
	return clampInt(window/2, toolTurnMinBudget, toolTurnMaxBudget)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// boundToolResult keeps the head and tail of an oversized result with an
// explicit omission marker between them. limit <= 0 folds the whole result to
// the one-line marker. Slicing by rune keeps CJK output intact.
func boundToolResult(text string, limit int) string {
	if limit <= 0 {
		return toolFoldMarker
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	gap := utf8.RuneCountInString(toolOmissionMarker)
	if limit <= gap+32 {
		return string(runes[:limit])
	}
	room := limit - gap
	head := room * 2 / 3
	tail := room - head
	return string(runes[:head]) + toolOmissionMarker + string(runes[len(runes)-tail:])
}

// toolResultPrefixLen reports the prefix of a plain-text tool-result turn the
// id-less appendToolCalls path writes. Both locales are recognized because a
// turn may be replayed into a later ask with a different reply locale.
func toolResultPrefixLen(s string) (int, bool) {
	for _, p := range []string{"工具结果：", "Tool result: "} {
		if strings.HasPrefix(s, p) {
			return len(p), true
		}
	}
	return 0, false
}

// turnToolRunes counts the tool-result text one turn carries. tool_use blocks
// hold arguments (small) and are deliberately not counted: they are the
// question, not the evidence that grows without bound.
func turnToolRunes(t entry.Turn) int {
	n := 0
	for _, b := range t.Blocks {
		if b.Type == "tool_result" {
			n += utf8.RuneCountInString(b.Content)
		}
	}
	if p, ok := toolResultPrefixLen(t.Content); ok {
		n += utf8.RuneCountInString(t.Content[p:])
	}
	return n
}

func toolResultRunes(turns []entry.Turn) int {
	n := 0
	for i := range turns {
		n += turnToolRunes(turns[i])
	}
	return n
}

func cloneTurn(t entry.Turn) entry.Turn {
	out := t
	if len(t.Blocks) > 0 {
		out.Blocks = append([]entry.ContentBlock(nil), t.Blocks...)
	}
	return out
}

// shrinkToolTurn bounds every tool result in a turn to limit and returns the
// number of runes freed. The caller owns a copy of the turn (cloneTurn), so
// the original transcript stays untouched.
func shrinkToolTurn(t *entry.Turn, limit int) int {
	freed := 0
	for i := range t.Blocks {
		b := &t.Blocks[i]
		if b.Type != "tool_result" {
			continue
		}
		before := utf8.RuneCountInString(b.Content)
		if before <= limit {
			continue
		}
		b.Content = boundToolResult(b.Content, limit)
		freed += before - utf8.RuneCountInString(b.Content)
	}
	if p, ok := toolResultPrefixLen(t.Content); ok {
		before := utf8.RuneCountInString(t.Content[p:])
		if before > limit {
			t.Content = t.Content[:p] + boundToolResult(t.Content[p:], limit)
			freed += before - utf8.RuneCountInString(t.Content[p:])
		}
	}
	return freed
}

// fitToolTurns returns a transcript whose total tool-result text fits budget,
// shrinking the oldest results first, plus the number of runes removed. It is
// idempotent: calling it again with the same budget returns the transcript
// unchanged. A budget of 0 still leaves the one-line markers, so an empty
// transcript is never produced by trimming.
func fitToolTurns(turns []entry.Turn, budget int) ([]entry.Turn, int) {
	remaining := toolResultRunes(turns)
	if budget < 0 {
		budget = 0
	}
	if remaining <= budget {
		return turns, 0
	}
	out := append([]entry.Turn(nil), turns...)
	freed := 0
	// Pass 1: keep a short head+tail of each old result.
	for i := range out {
		if remaining <= budget {
			break
		}
		if turnToolRunes(out[i]) == 0 {
			continue
		}
		c := cloneTurn(out[i])
		n := shrinkToolTurn(&c, toolStagedLimit)
		if n == 0 {
			continue
		}
		out[i] = c
		remaining -= n
		freed += n
	}
	// Pass 2: still over budget — fold the oldest payloads to one line.
	for i := range out {
		if remaining <= budget {
			break
		}
		if turnToolRunes(out[i]) == 0 {
			continue
		}
		c := cloneTurn(out[i])
		n := shrinkToolTurn(&c, 0)
		if n == 0 {
			continue
		}
		out[i] = c
		remaining -= n
		freed += n
	}
	return out, freed
}

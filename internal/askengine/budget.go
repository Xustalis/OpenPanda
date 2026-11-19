// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"sort"
	"strconv"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// Sub-agent task budget: how many tasks one ask may dispatch.
//
// The number cannot be a constant. "Test the agents I picked" is an instruction
// whose width the user already stated — the roster — and a fixed ceiling of 3
// silently dropped the fourth pick: three tasks went out, opencode was never
// submitted, and no surface said so. An unbounded fan-out is just as wrong: a
// model looping on task_submit would spawn work until the round budget ran out.
//
// So the budget is derived, and every source can only raise it:
//
//   - defaultTaskBudget — the floor when nothing else applies (a plain ask with
//     no roster and no declared plan).
//   - the roster — one task per agent the user explicitly picked. This is the
//     only *deterministic* statement of width available: it is already
//     validated (panel.cleanTeam) and already enforced at routing, so it needs
//     no reading of the prompt. Parsing "test these 4" out of prose would mean
//     guessing across five locales, and a wrong guess either truncates work or
//     raises a safety limit on a hunch.
//   - task_budget — the model declares how many tasks the round needs, bounded
//     by taskCeiling. That covers the no-roster fan-out ("check these five
//     modules") without the engine trying to read numbers out of a sentence.
//
// Whatever the source, a refusal is never silent: the model gets a tool result
// naming what was dropped and how to raise the limit, and the user gets the
// same fact on Result.Warning.
const (
	// defaultTaskBudget is the floor for an ask that named no hands.
	defaultTaskBudget = 3
	// taskCeiling bounds what the model may declare for itself. A roster is
	// not held to it: sixteen picked agents are sixteen picks, and the panel
	// already caps a roster at maxTeam.
	taskCeiling = 8
)

// taskBudget counts one ask's dispatches. Not safe for concurrent use: a single
// AskTurnsScoped owns it and the tool loop is sequential.
type taskBudget struct {
	limit int
	used  int
	// refused holds titles dropped for lack of budget, keyed by title so a
	// later round that dispatches the same task clears its own advisory.
	refused map[string]bool
}

func newTaskBudget(roster int) *taskBudget {
	limit := defaultTaskBudget
	if roster > limit {
		limit = roster
	}
	return &taskBudget{limit: limit, refused: make(map[string]bool)}
}

// remaining is how many more tasks this ask may dispatch.
func (b *taskBudget) remaining() int {
	if b.limit <= b.used {
		return 0
	}
	return b.limit - b.used
}

// exhausted reports whether the next dispatch must be refused.
func (b *taskBudget) exhausted() bool { return b.remaining() == 0 }

// spend books one dispatched task.
func (b *taskBudget) spend() { b.used++ }

// request raises the limit to want — never lowers it, and never past the
// ceiling unless the roster already justified more. It returns the limit in
// force afterwards.
func (b *taskBudget) request(want int) int {
	if want < 0 {
		want = 0
	}
	ceiling := taskCeiling
	if b.limit > ceiling {
		ceiling = b.limit
	}
	if want > ceiling {
		want = ceiling
	}
	if want > b.limit {
		b.limit = want
	}
	return b.limit
}

// refuse records a task the budget turned away.
func (b *taskBudget) refuse(title string) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "?"
	}
	b.refused[title] = true
}

// settled drops title from the refusal list: a later round dispatched it after
// raising the limit, so it is no longer missing.
func (b *taskBudget) settled(title string) {
	delete(b.refused, strings.TrimSpace(title))
}

// dropped lists the titles still missing, in a stable order.
func (b *taskBudget) dropped() []string {
	out := make([]string, 0, len(b.refused))
	for title := range b.refused {
		out = append(out, title)
	}
	sort.Strings(out)
	return out
}

// dispatchKey identifies one task_submit call by what it asks for, so a batch
// the model re-sends after a refusal can be recognised as the same work rather
// than dispatched twice.
func dispatchKey(args map[string]any) string {
	title, _ := args["title"].(string)
	target, _ := args["target"].(string)
	abilities := toStringSlice(args["abilities"])
	return strings.Join([]string{
		strings.TrimSpace(title),
		strings.TrimSpace(target),
		strings.Join(abilities, ","),
		strings.Join(toStringSlice(args["paths"]), "\x00"),
	}, "\x00")
}

// alreadyDispatchedNote is the tool result for a task_submit this ask already
// ran. It carries the original id so the model can report on it instead of
// believing the dispatch failed.
func alreadyDispatchedNote(taskID string, loc ...i18n.Locale) string {
	targetLoc := i18n.Detect()
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	}
	return i18n.Tf(targetLoc, "prompt.subagent.already_dispatched", "id", taskID)
}

// budgetRefusalNote is what the model reads when a dispatch is turned away. It
// names the task, states the limit in force, and points at the one way to
// change it — a refusal the model cannot act on is a dropped agent with extra
// words.
func budgetRefusalNote(b *taskBudget, title string, loc ...i18n.Locale) string {
	targetLoc := i18n.Detect()
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	}
	return i18n.Tf(targetLoc, "prompt.subagent.budget_refusal",
		"title", title,
		"n", strconv.Itoa(b.limit),
		"used", strconv.Itoa(b.used),
		"ceiling", strconv.Itoa(taskCeiling))
}

// budgetWarning is the user-facing advisory carried on Result.Warning when the
// round ended with dispatches still owed. Empty when nothing was dropped.
func budgetWarning(b *taskBudget, loc ...i18n.Locale) string {
	dropped := b.dropped()
	if len(dropped) == 0 {
		return ""
	}
	targetLoc := i18n.Detect()
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	}
	sep := ", "
	if targetLoc == i18n.ChineseSimp || targetLoc == i18n.Japanese {
		sep = "、"
	}
	return i18n.Tf(targetLoc, "ask.budget.truncated",
		"n", strconv.Itoa(b.limit),
		"k", strconv.Itoa(len(dropped)),
		"titles", strings.Join(dropped, sep))
}

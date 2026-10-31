// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"strconv"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// clarifyChoice returns the first selected label (or the free-text answer) for
// one question id. "" means the user left it unanswered, which the plan card
// treats as "accept the recommendation".
func clarifyChoice(answers []entry.ClarifyAnswer, qid string) string {
	for _, a := range answers {
		if a.ID != qid {
			continue
		}
		if other := strings.TrimSpace(a.Other); other != "" {
			return other
		}
		if len(a.Selected) > 0 {
			return strings.TrimSpace(a.Selected[0])
		}
	}
	return ""
}

// planConfirmNeeded reports whether a classified task is large or risky
// enough to deserve an explicit approval card before it runs. The thresholds
// mirror the "large task" class the entry prompt already marks: high
// complexity, or a high/critical risk rating.
func planConfirmNeeded(t *entry.TaskSpec) bool {
	if t == nil {
		return false
	}
	if t.Complexity >= 0.6 {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(t.Risk)) {
	case "high", "critical":
		return true
	}
	return false
}

// planConfirmSpec builds the approval card for a classified task: the target,
// the success definition, and the run / read-only / cancel choice. It reuses
// the clarify card so one renderer serves both gates.
func planConfirmSpec(t *entry.TaskSpec, loc i18n.Locale) (spec entry.ClarifySpec, execLabel, readOnlyLabel, cancelLabel string) {
	execLabel = i18n.T(loc, "confirm.exec")
	readOnlyLabel = i18n.T(loc, "confirm.readonly")
	cancelLabel = i18n.T(loc, "confirm.cancel")
	var plan strings.Builder
	target := strings.TrimSpace(t.Spec.Target)
	if r := []rune(target); len(r) > 1200 {
		target = string(r[:1200]) + "…"
	}
	plan.WriteString(target)
	if sd := strings.TrimSpace(t.Spec.SuccessDefinition); sd != "" {
		plan.WriteString("\n\n" + i18n.T(loc, "confirm.verify") + " " + sd)
	}
	spec = entry.ClarifySpec{
		Plan: plan.String(),
		Questions: []entry.ClarifyQuestion{{
			ID:       "go",
			Header:   i18n.T(loc, "confirm.header"),
			Question: i18n.T(loc, "confirm.question"),
			Options: []entry.ClarifyOption{
				{Label: execLabel},
				{Label: readOnlyLabel},
				{Label: cancelLabel},
			},
			Recommended: execLabel,
		}},
	}
	return spec, execLabel, readOnlyLabel, cancelLabel
}

// renderClarify is the human-readable form of a pre-execution question round.
// It is what a surface without a choice card shows (CLI, panel transcript) and
// what a NeedsClarification Result carries as its Answer: plan prose first,
// then each question with its numbered options, then how to answer.
func renderClarify(spec *entry.ClarifySpec, loc i18n.Locale) string {
	if spec == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(i18n.T(loc, "clarify.title"))
	if plan := strings.TrimSpace(spec.Plan); plan != "" {
		b.WriteString("\n\n")
		b.WriteString(plan)
	}
	for i, q := range spec.Questions {
		b.WriteString("\n\n")
		header := strings.TrimSpace(q.Header)
		if header != "" {
			b.WriteString("[" + header + "] ")
		}
		b.WriteString(strconv.Itoa(i+1) + ". " + strings.TrimSpace(q.Question))
		for j, o := range q.Options {
			b.WriteString("\n   " + strconv.Itoa(j+1) + ") " + strings.TrimSpace(o.Label))
			if o.Label == q.Recommended {
				b.WriteString(" " + i18n.T(loc, "clarify.recommended"))
			}
			if d := strings.TrimSpace(o.Description); d != "" {
				b.WriteString(" — " + d)
			}
		}
		if q.MultiSelect {
			b.WriteString("\n   " + i18n.T(loc, "clarify.multi"))
		}
	}
	b.WriteString("\n\n" + i18n.T(loc, "clarify.answer_hint"))
	return b.String()
}

// clarifyQuestionTurn renders the question round as the assistant turn the
// model sees after the user answers. It is a record of what was asked, in the
// same order the answers arrive.
func clarifyQuestionTurn(spec *entry.ClarifySpec) string {
	var b strings.Builder
	b.WriteString("[clarify] I asked the user before acting:")
	if plan := strings.TrimSpace(spec.Plan); plan != "" {
		b.WriteString("\nproposed plan:\n" + plan)
	}
	for _, q := range spec.Questions {
		b.WriteString("\nQ " + q.ID + " (" + q.Header + "): " + q.Question)
		for _, o := range q.Options {
			b.WriteString("\n  - " + o.Label)
		}
		if q.MultiSelect {
			b.WriteString("\n  (multi-select)")
		}
	}
	return b.String()
}

// clarifyAnswerTurn folds the user's choices into one user turn. Unanswered
// questions fall back to the model's recommendation — the same rule the ask
// prompt states — so the next classification has a complete decision set
// instead of asking again.
func clarifyAnswerTurn(answers []entry.ClarifyAnswer, spec *entry.ClarifySpec, loc i18n.Locale) string {
	byID := make(map[string]entry.ClarifyAnswer, len(answers))
	for _, a := range answers {
		byID[a.ID] = a
	}
	var b strings.Builder
	b.WriteString("[clarify answers]")
	answered := 0
	for _, q := range spec.Questions {
		a, ok := byID[q.ID]
		var picks []string
		if ok {
			for _, sel := range a.Selected {
				if strings.TrimSpace(sel) != "" {
					picks = append(picks, strings.TrimSpace(sel))
				}
			}
			if other := strings.TrimSpace(a.Other); other != "" {
				picks = append(picks, other)
			}
		}
		if len(picks) == 0 && q.Recommended != "" {
			picks = []string{q.Recommended + " (your recommendation; the user did not choose)"}
		}
		if len(picks) == 0 {
			b.WriteString("\n- " + q.ID + " (" + q.Question + "): " + i18n.T(loc, "clarify.unanswered"))
			continue
		}
		answered++
		b.WriteString("\n- " + q.ID + " (" + q.Question + "): " + strings.Join(picks, ", "))
	}
	if answered == 0 {
		b.WriteString("\nNo question was answered. Proceed with the recommended option for each question, state the assumption in the task constraints, and do not ask again.")
	} else {
		b.WriteString("\nUse these answers for the work ahead. Do not ask the same questions again.")
	}
	return b.String()
}

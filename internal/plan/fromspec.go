package plan

// FromSpec is the model-generated half of the plan entry point. The YAML file
// (Parse) is the half you keep: a pipeline you run every week should be readable
// and diffable. This half is the one that makes the flagship scenario reachable
// by a sentence — "训练一个图像分类模型" spoken at the Pi has to become develop →
// train → report across three machines without the user authoring a file first,
// because the whole point is not switching between devices.
//
// The conversion is deliberately dumb: copy fields, then run the same Validate
// every other plan goes through. Nothing here trusts the model — a plan with a
// dangling dependency or a cycle is rejected by the same check that rejects a
// hand-written one, before any stage becomes a task row.

import (
	"strings"

	"github.com/Xustalis/OpenPanda/internal/entry"
)

// FromSpec converts a model-emitted plan into an executable Plan, validating it.
// A stage with no title borrows its intent's first line: the title is what shows
// up on the task board, and "stage 2" tells an operator nothing.
func FromSpec(spec entry.PlanSpec) (Plan, error) {
	p := Plan{
		Goal:   strings.TrimSpace(spec.Goal),
		Stages: make([]Stage, 0, len(spec.Stages)),
	}
	for _, s := range spec.Stages {
		intent := strings.TrimSpace(s.Intent)
		title := strings.TrimSpace(s.Title)
		if title == "" {
			title = firstLine(intent)
		}
		p.Stages = append(p.Stages, Stage{
			ID:        strings.TrimSpace(s.ID),
			Title:     title,
			Requires:  trimAll(s.Requires),
			Needs:     trimAll(s.Needs),
			Intent:    intent,
			Resources: s.Resources,
		})
	}
	if err := Validate(p); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// titleMax bounds a borrowed title so a stage whose intent is one long paragraph
// does not push a queue row off the terminal.
const titleMax = 60

// firstLine returns the first line of s, truncated to titleMax runes.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > titleMax {
		return string(r[:titleMax-1]) + "…"
	}
	return s
}

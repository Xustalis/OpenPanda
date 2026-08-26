package plan

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/entry"
)

// TestFromSpec pins the model-generated half of the plan entry point: a plan the
// entry model emitted must reach execution through the same Validate a
// hand-written file does, and must carry the same routing vocabulary.
func TestFromSpec(t *testing.T) {
	p, err := FromSpec(entry.PlanSpec{
		Goal: "训练一个图像分类模型",
		Stages: []entry.PlanStageSpec{
			{ID: "develop", Title: "写脚本", Intent: "写 train.py", Requires: []string{"agent:claude_code"},
				Resources: entry.ResourceProfile{CPU: 2, RAMGB: 4, DurationHint: "short"}},
			{ID: "train", Intent: "运行 train.py，等它跑完\n第二行会被标题丢掉", Needs: []string{"develop"},
				Requires:  []string{"agent:codex"},
				Resources: entry.ResourceProfile{CPU: 8, RAMGB: 16, GPUVRAMGB: 8, DurationHint: "long"}},
		},
	})
	if err != nil {
		t.Fatalf("from spec: %v", err)
	}
	order, err := Order(p)
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	if order[0].ID != "develop" || order[1].ID != "train" {
		t.Fatalf("order = %s → %s, want develop → train", order[0].ID, order[1].ID)
	}
	// The VRAM must survive the conversion: it is the hard filter that keeps the
	// training stage off the Pi, and dropping it silently is the failure that
	// looks like success until a training run lands on a 4-core board.
	if order[1].Resources.GPUVRAMGB != 8 {
		t.Errorf("train VRAM = %v, want 8", order[1].Resources.GPUVRAMGB)
	}
	// A stage with no title borrows its intent's first line: "stage 2" on the task
	// board tells an operator nothing.
	if order[1].Title != "运行 train.py，等它跑完" {
		t.Errorf("borrowed title = %q", order[1].Title)
	}
}

// TestFromSpecValidates is the point of routing model output through Validate: a
// dangling dependency or a cycle must be refused before any stage becomes a task
// row on a real machine, not discovered after work has been dispatched.
func TestFromSpecValidates(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec entry.PlanSpec
		want string
	}{
		{"dangling need", entry.PlanSpec{Goal: "g", Stages: []entry.PlanStageSpec{
			{ID: "a", Intent: "i", Needs: []string{"nope"}},
		}}, "not in the plan"},
		{"cycle", entry.PlanSpec{Goal: "g", Stages: []entry.PlanStageSpec{
			{ID: "a", Intent: "i", Needs: []string{"b"}},
			{ID: "b", Intent: "i", Needs: []string{"a"}},
		}}, "cycle"},
		{"no goal", entry.PlanSpec{Stages: []entry.PlanStageSpec{{ID: "a", Intent: "i"}}}, "goal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := FromSpec(tc.spec)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

// TestFromSpecTitleTruncation keeps a one-paragraph intent from pushing a queue
// row off the terminal.
func TestFromSpecTitleTruncation(t *testing.T) {
	long := strings.Repeat("很长的一段说明", 20)
	p, err := FromSpec(entry.PlanSpec{Goal: "g", Stages: []entry.PlanStageSpec{{ID: "a", Intent: long}}})
	if err != nil {
		t.Fatalf("from spec: %v", err)
	}
	if n := len([]rune(p.Stages[0].Title)); n > titleMax {
		t.Errorf("borrowed title is %d runes, limit is %d", n, titleMax)
	}
	if !strings.HasSuffix(p.Stages[0].Title, "…") {
		t.Errorf("a truncated title does not say it was truncated: %q", p.Stages[0].Title)
	}
}

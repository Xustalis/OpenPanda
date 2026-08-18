package askengine

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/entry"
)

// TestToTaskInput verifies the TaskSpec → TaskInput translation, especially the
// intent composition that drives the agent adapter.
func TestToTaskInput(t *testing.T) {
	spec := &entry.TaskSpec{
		Title:       "重构导航栏",
		Project:     "117club",
		ContextType: "file",
		Requires:    entry.Requires{Abilities: []string{"code:modify"}},
		Spec: entry.TaskSpecDetail{
			Scope:             "Hero.vue",
			Target:            "改为响应式布局",
			Constraints:       []string{"不能改 API", "保持移动端优先"},
			SuccessDefinition: "npm run build 通过",
		},
		Complexity: 0.6,
		Risk:       "medium",
		Resources:  entry.ResourceProfile{CPU: 2, RAMGB: 4, DurationHint: "short"},
	}

	in := toTaskInput(spec)

	if in.Title != "重构导航栏" || in.Project != "117club" || in.ContextType != "file" {
		t.Fatalf("identity fields wrong: %+v", in)
	}
	if in.Complexity != 0.6 || in.Risk != "medium" {
		t.Fatalf("detail fields wrong: %+v", in)
	}
	if len(in.Requires) != 1 || in.Requires[0] != "code:modify" {
		t.Fatalf("requires = %v", in.Requires)
	}

	// Intent must carry all four spec sections.
	for _, want := range []string{"重构导航栏", "目标：改为响应式布局", "范围：Hero.vue", "约束：不能改 API；保持移动端优先", "成功标准：npm run build 通过"} {
		if !strings.Contains(in.Intent, want) {
			t.Fatalf("intent missing %q:\n%s", want, in.Intent)
		}
	}

	if !strings.Contains(in.SpecJSON, `"scope"`) || !strings.Contains(in.ResourceJSON, `"cpu"`) {
		t.Fatalf("spec/resource JSON not marshaled: %q / %q", in.SpecJSON, in.ResourceJSON)
	}
}

func TestSchedulerTier(t *testing.T) {
	cases := map[string]int{"Full": 10, "Standard": 5, "Micro": 1, "": 1, "bogus": 1}
	for in, want := range cases {
		if got := schedulerTier(in); got != want {
			t.Errorf("schedulerTier(%q) = %d, want %d", in, got, want)
		}
	}
}

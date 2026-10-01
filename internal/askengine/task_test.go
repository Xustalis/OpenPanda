package askengine

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// TestToTaskInput verifies the TaskSpec → TaskInput translation, especially the
// intent composition that drives the agent adapter.
func TestToTaskInput(t *testing.T) {
	spec := &entry.TaskSpec{
		Title:       "重构导航栏",
		Project:     "demo-portal",
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

	if in.Title != "重构导航栏" || in.Project != "demo-portal" || in.ContextType != "file" {
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

func TestToTaskInput_MultiLanguage(t *testing.T) {
	spec := &entry.TaskSpec{
		Title:       "Refactor Navigation Bar",
		Project:     "demo-portal",
		ContextType: "file",
		Requires:    entry.Requires{Abilities: []string{"code:modify"}},
		Spec: entry.TaskSpecDetail{
			Scope:             "Hero.vue",
			Target:            "Convert to responsive layout",
			Constraints:       []string{"Do not modify API", "Keep mobile-first"},
			SuccessDefinition: "npm run build passes",
		},
		Complexity: 0.6,
		Risk:       "medium",
		Resources:  entry.ResourceProfile{CPU: 2, RAMGB: 4, DurationHint: "short"},
	}

	inEn := toTaskInput(spec, i18n.English)
	if inEn.UserLocale != i18n.English {
		t.Errorf("UserLocale = %v, want %v", inEn.UserLocale, i18n.English)
	}
	wantsEn := []string{
		"Refactor Navigation Bar",
		"Target: Convert to responsive layout",
		"Scope: Hero.vue",
		"Constraints: Do not modify API; Keep mobile-first",
		"Success Definition: npm run build passes",
	}
	for _, want := range wantsEn {
		if !strings.Contains(inEn.Intent, want) {
			t.Errorf("English intent missing %q:\n%s", want, inEn.Intent)
		}
	}

	inZh := toTaskInput(spec, i18n.ChineseSimp)
	if inZh.UserLocale != i18n.ChineseSimp {
		t.Errorf("UserLocale = %v, want %v", inZh.UserLocale, i18n.ChineseSimp)
	}
	wantsZh := []string{
		"Refactor Navigation Bar",
		"目标：Convert to responsive layout",
		"范围：Hero.vue",
		"约束：Do not modify API；Keep mobile-first",
		"成功标准：npm run build passes",
	}
	for _, want := range wantsZh {
		if !strings.Contains(inZh.Intent, want) {
			t.Errorf("Chinese intent missing %q:\n%s", want, inZh.Intent)
		}
	}
}

// TestToTaskInputCarriesExecutionOverrides pins the spec_json contract the
// executor depends on: tools_policy and max_turns are emitted by the model on
// the outer TaskSpec, but spec_json is what persists and crosses the wire, so
// toTaskInput must fold them into the marshalled detail or they evaporate at
// this boundary (the pre-v0.0.10 bug this test guards).
func TestToTaskInputCarriesExecutionOverrides(t *testing.T) {
	spec := &entry.TaskSpec{
		Title:       "deep refactor",
		ContextType: "file",
		ToolsPolicy: "extended",
		MaxTurns:    60,
		Requires:    entry.Requires{Abilities: []string{"code:modify"}},
		Spec:        entry.TaskSpecDetail{Scope: "internal", Target: "refactor internals"},
	}
	in := toTaskInput(spec)
	if !strings.Contains(in.SpecJSON, `"tools_policy":"extended"`) {
		t.Fatalf("spec_json lost tools_policy: %s", in.SpecJSON)
	}
	if !strings.Contains(in.SpecJSON, `"max_turns":60`) {
		t.Fatalf("spec_json lost max_turns: %s", in.SpecJSON)
	}

	// A detail-level value wins over the outer default — the detail is the
	// durable contract, so it cannot be silently overwritten.
	spec.Spec.ToolsPolicy = "minimal"
	spec.Spec.MaxTurns = 10
	in = toTaskInput(spec)
	if !strings.Contains(in.SpecJSON, `"tools_policy":"minimal"`) ||
		!strings.Contains(in.SpecJSON, `"max_turns":10`) {
		t.Fatalf("detail overrides lost: %s", in.SpecJSON)
	}
}

// TestToTaskInputCarriesActionSpec pins the §7.2 wire contract: the model's
// action_spec rides inside spec_json so the executor's SubstituteActionSpec
// sees exactly what the model emitted — on this node or after delegation.
// It also pins the two normalizations an action_spec implies: context is
// hardware whatever the model labelled it, and the exact target id leads
// requires so MatchActuator resolves THIS actuator, not a sibling a vaguer
// token ("servo") might hit first.
func TestToTaskInputCarriesActionSpec(t *testing.T) {
	spec := &entry.TaskSpec{
		Title:       "turn servo",
		ContextType: "command", // model mislabels on purpose — spec wins
		Requires:    entry.Requires{Abilities: []string{"servo"}},
		Spec: entry.TaskSpecDetail{
			Target: "turn the servo to 90 degrees",
			ActionSpec: &ledger.ActionSpec{
				TargetActuator: "hardware:servo_rotate",
				Action:         "rotate",
				Parameters:     map[string]any{"angle": 90.0},
			},
		},
	}
	in := toTaskInput(spec)
	if !strings.Contains(in.SpecJSON, `"action_spec"`) ||
		!strings.Contains(in.SpecJSON, `"target_actuator":"hardware:servo_rotate"`) ||
		!strings.Contains(in.SpecJSON, `"angle":90`) {
		t.Fatalf("spec_json lost action_spec: %s", in.SpecJSON)
	}
	if in.ContextType != "hardware" {
		t.Fatalf("context_type = %q, want hardware", in.ContextType)
	}
	if len(in.Requires) == 0 || in.Requires[0] != "hardware:servo_rotate" {
		t.Fatalf("target actuator must lead requires, got %v", in.Requires)
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

// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"errors"
	"strings"
	"testing"
)

// truncatedTaskDirective mirrors a real model reply (2026-11-17): a complete
// task spec whose envelope stops one closing brace short. ParseOutput used to
// return it as the answer text, so the chat pane showed the envelope and the
// task it described was never created.
const truncatedTaskDirective = `{"kind":"task","task":{"title":"新建鹈鹕骑车单文件 HTML 插画","context_type":"file","requires":{"abilities":["agent:claude_code"]},"spec":{"objectives":{"version":1,"revision":1,"dimensions":[{"dimension":"deliverable","weight":1,"priority":"MAX","requirement":"在目录中新增一个「鹈鹕骑车」的 HTML 文件","reason":"用户明确要求产出一个 html","criteria":["文件实际存在"]}]},"task_kind":"edit","scope":"pelican-bicycle.html"},"complexity":0.55,"risk":"low","tools_policy":"minimal","max_turns":12,"resource_profile":{"cpu":2,"ram_gb":2,"gpu_vram_gb":0,"duration_hint":"short"}}`

func TestTruncatedDirectiveIsRepairedNotAnswered(t *testing.T) {
	if _, ok := closeTruncatedJSON(truncatedTaskDirective); !ok {
		t.Fatalf("the recorded reply must be recognised as truncated")
	}
	out, err := ParseOutput(truncatedTaskDirective)
	if err != nil {
		t.Fatalf("ParseOutput: %v", err)
	}
	if out.Kind != KindTask {
		t.Fatalf("kind = %q, want task (the envelope describes real work)", out.Kind)
	}
	if out.Task == nil || out.Task.Title == "" {
		t.Fatalf("task payload lost: %+v", out.Task)
	}
	if out.Task.Spec.Scope != "pelican-bicycle.html" {
		t.Fatalf("scope = %q, want pelican-bicycle.html", out.Task.Spec.Scope)
	}
	if out.Answer != "" {
		t.Fatalf("answer carries %d bytes of envelope", len(out.Answer))
	}
}

func TestMisNestedDirectiveIsRefused(t *testing.T) {
	// The recorded failure was not a clean tail truncation: the model put the
	// spec's own fields inside objectives and closed one level late. Closing the
	// tail balances the braces, but a lenient decode would then drop scope and
	// target and dispatch a task with no destination.
	raw := `{"kind":"task","task":{"title":"t","context_type":"file","requires":{"abilities":["agent:claude_code"]},` +
		`"spec":{"objectives":{"version":1,"revision":1,"dimensions":[{"dimension":"d","weight":1,"priority":"MAX","requirement":"r","reason":"x","criteria":["c"]}],"task_kind":"edit","scope":"s"}},` +
		`"complexity":0.5,"risk":"low","resource_profile":{"cpu":1}}`
	out, err := ParseOutput(raw)
	if !errors.Is(err, ErrDirectiveProse) {
		t.Fatalf("err = %v, want ErrDirectiveProse (got kind=%q, task=%+v)", err, out.Kind, out.Task)
	}
}

func TestUnrepairableDirectiveIsRefused(t *testing.T) {
	// Truncated inside the value, not just the tail: no suffix can close this.
	raw := `{"kind":"task","task":{"title":"x","spec":`
	out, err := ParseOutput(raw)
	if !errors.Is(err, ErrDirectiveProse) {
		t.Fatalf("err = %v, want ErrDirectiveProse", err)
	}
	if out.Kind != "" || out.Answer != "" {
		t.Fatalf("refused output must be empty, got kind=%q answer=%q", out.Kind, out.Answer)
	}
}

func TestDirectiveInProseIsRefused(t *testing.T) {
	raw := "好的，我来派发这个任务：\n" + strings.TrimSuffix(truncatedTaskDirective, "}")
	out, err := ParseOutput(raw)
	if !errors.Is(err, ErrDirectiveProse) {
		t.Fatalf("err = %v, want ErrDirectiveProse (kind=%q)", err, out.Kind)
	}
}

func TestIllustrativeDirectiveStaysAnswer(t *testing.T) {
	raw := `例如把任务写成 {"kind":"task","task":{"title":"x"}} 这样的结构。`
	out, err := ParseOutput(raw)
	if err != nil {
		t.Fatalf("ParseOutput: %v", err)
	}
	if out.Kind != KindAnswer {
		t.Fatalf("kind = %q, want answer — the prose is showing an example", out.Kind)
	}
	if out.Answer != raw {
		t.Fatalf("answer was rewritten: %q", out.Answer)
	}
}

func TestDataJSONTruncationStaysAnswer(t *testing.T) {
	// Fail-open is still the rule for text that never announced a directive.
	raw := `{"a":1`
	out, err := ParseOutput(raw)
	if err != nil {
		t.Fatalf("ParseOutput: %v", err)
	}
	if out.Kind != KindAnswer || out.Answer != raw {
		t.Fatalf("kind=%q answer=%q, want the raw text as an answer", out.Kind, out.Answer)
	}
}

func TestAnswerEnvelopeStillUnwrapped(t *testing.T) {
	out, err := ParseOutput(`{"kind":"answer","answer":"直接回答"}`)
	if err != nil {
		t.Fatalf("ParseOutput: %v", err)
	}
	if out.Kind != KindAnswer || out.Answer != "直接回答" {
		t.Fatalf("kind=%q answer=%q", out.Kind, out.Answer)
	}
}

func TestDeclaresDirective(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"task envelope", `{"kind":"task","task":{}}`, true},
		{"plan envelope", `{"kind": "plan", "plan":{}}`, true},
		{"tool_call envelope", `{"kind":"tool_call","tool":{}}`, true},
		{"clarify envelope", `{"kind":"clarify","clarify":{}}`, true},
		{"short lead-in", `好的，派发：{"kind":"task"}`, true},
		{"answer envelope is not a directive", `{"kind":"answer","answer":"hi"}`, false},
		{"nested task_kind is not a kind field", `{"spec":{"task_kind":"edit"}}`, false},
		{"plain prose", "这是一段普通回答，没有 JSON。", false},
		{"data object", `{"name":"pelican"}`, false},
		{"buried far into prose", strings.Repeat("长", jsonLeadInMax) + `{"kind":"task"}`, false},
	}
	for _, c := range cases {
		if got := DeclaresDirective(c.text); got != c.want {
			t.Errorf("%s: DeclaresDirective = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCloseTruncatedJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"one missing brace", `{"kind":"task","task":{}`, true},
		{"balanced", `{"a":1}`, false},
		{"unterminated string", `{"a":"x`, false},
		{"mismatched closer", `{"a":[1}`, false},
		{"empty", "", false},
	}
	for _, c := range cases {
		got, ok := closeTruncatedJSON(c.in)
		if ok != c.want {
			t.Errorf("%s: ok = %v, want %v", c.name, ok, c.want)
			continue
		}
		if ok && len(got) <= len(c.in) {
			t.Errorf("%s: repair did not extend the text", c.name)
		}
	}
}

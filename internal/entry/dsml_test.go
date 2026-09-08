package entry

import (
	"strings"
	"testing"
)

// The exact markup shape observed in the wild: DeepSeek-compatible endpoints
// returning a tool call as DSML text (here with full-width pipes) instead of
// native tool_use blocks.
const dsmlFullWidth = `<｜｜DSML｜｜tool_calls>
<｜｜DSML｜｜invoke name="taskq_priority">
<｜｜DSML｜｜parameter name="priority" string="true">high</｜｜DSML｜｜parameter>
<｜｜DSML｜｜parameter name="task_id" string="true">01a0773f</｜｜DSML｜｜parameter>
</｜｜DSML｜｜invoke>
</｜｜DSML｜｜tool_calls>`

const dsmlASCII = `<||DSML||tool_calls>
<||DSML||invoke name="taskq_move">
<||DSML||parameter name="task_id" string="true">abc123</||DSML||parameter>
<||DSML||parameter name="seq">3</||DSML||parameter>
</||DSML||invoke>
</||DSML||tool_calls>`

func TestContainsDSMLToolCall(t *testing.T) {
	for _, s := range []string{dsmlFullWidth, dsmlASCII, "trailing </||DSML||invoke>", "<｜｜DSML｜｜invoke name=\"x\">"} {
		if !ContainsDSMLToolCall(s) {
			t.Errorf("ContainsDSMLToolCall(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "普通回答，没有标记", "<|DSML|> 不是完整标记", "DSML 单独出现不算"} {
		if ContainsDSMLToolCall(s) {
			t.Errorf("ContainsDSMLToolCall(%q) = true, want false", s)
		}
	}
}

func TestParseDSMLToolCalls(t *testing.T) {
	uses, preamble, ok := parseDSMLToolCalls("我来调度 codex 去探索这个项目。\n" + dsmlFullWidth)
	if !ok {
		t.Fatal("parseDSMLToolCalls ok = false, want true")
	}
	if preamble != "我来调度 codex 去探索这个项目。" {
		t.Errorf("preamble = %q", preamble)
	}
	if len(uses) != 1 || uses[0].Name != "taskq_priority" {
		t.Fatalf("uses = %+v", uses)
	}
	if uses[0].Input["priority"] != "high" || uses[0].Input["task_id"] != "01a0773f" {
		t.Errorf("input = %+v", uses[0].Input)
	}
	if uses[0].ID != "" {
		t.Errorf("recovered call must carry no native id, got %q", uses[0].ID)
	}
}

func TestParseDSMLToolCallsTypesAndMultiple(t *testing.T) {
	uses, _, ok := parseDSMLToolCalls(dsmlASCII + "\n<||DSML||invoke name=\"taskq_list\">\n</||DSML||invoke>")
	if !ok || len(uses) != 2 {
		t.Fatalf("uses = %+v ok=%v", uses, ok)
	}
	if uses[0].Input["seq"] != float64(3) {
		t.Errorf("seq = %#v, want numeric 3", uses[0].Input["seq"])
	}
	if uses[1].Name != "taskq_list" || len(uses[1].Input) != 0 {
		t.Errorf("second invoke = %+v", uses[1])
	}
}

func TestParseDSMLToolCallsUnparsable(t *testing.T) {
	if _, _, ok := parseDSMLToolCalls("<||DSML||tool_calls>\n(no invoke here)\n</||DSML||tool_calls>"); ok {
		t.Error("ok = true for markup without invoke, want false")
	}
}

func TestResolveDSMLRecoversWhenToolsOffered(t *testing.T) {
	out, err := resolveDSML(Response{Text: "现在调度 codex。\n" + dsmlFullWidth}, true)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if out.Kind != KindToolCall || out.Tool == nil {
		t.Fatalf("out = %+v, want KindToolCall", out)
	}
	if out.Tool.Tool != "taskq_priority" {
		t.Errorf("tool = %q", out.Tool.Tool)
	}
	if out.Tool.ID != "" {
		t.Errorf("ID = %q, want empty (prose replay path)", out.Tool.ID)
	}
	if out.Note == "" || !strings.Contains(out.Note, "现在调度 codex") {
		t.Errorf("Note = %q, want preamble carried", out.Note)
	}
	if err := ValidateToolCall(out.Tool); err != nil {
		t.Errorf("ValidateToolCall: %v", err)
	}
}

func TestResolveDSMLToolFreeRoundStripsMarkup(t *testing.T) {
	out, err := resolveDSML(Response{Text: "我现在就调度 codex。\n" + dsmlFullWidth}, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if out.Kind != KindAnswer || out.Answer != "我现在就调度 codex。" {
		t.Errorf("out = %+v, want stripped answer", out)
	}
	if ContainsDSMLToolCall(out.Answer) {
		t.Error("answer still carries DSML markup")
	}
}

func TestResolveDSMLToolFreeRoundMarkupOnlyRejects(t *testing.T) {
	_, err := resolveDSML(Response{Text: dsmlFullWidth}, false)
	if err == nil {
		t.Fatal("err = nil, want reject")
	}
	ce, ok := err.(*ClassifyError)
	if !ok {
		t.Fatalf("err = %T, want *ClassifyError", err)
	}
	if IsFatalModelError(ce) {
		t.Error("DSML reject must not trigger model fallback")
	}
	if ContainsDSMLToolCall(ce.UserMsg) {
		t.Error("user message must not quote the markup")
	}
}

func TestResolveDSMLUnparsableRejects(t *testing.T) {
	_, err := resolveDSML(Response{Text: "<||DSML||tool_calls>\nbroken\n</||DSML||tool_calls>"}, true)
	ce, ok := err.(*ClassifyError)
	if !ok {
		t.Fatalf("err = %T (%v), want *ClassifyError", err, err)
	}
	if IsFatalModelError(ce) {
		t.Error("unparsable DSML must not trigger model fallback")
	}
}

func TestResolveResponseDSMLRecovery(t *testing.T) {
	out, err := resolveResponse(Response{Text: dsmlFullWidth}, true)
	if err != nil || out.Kind != KindToolCall {
		t.Fatalf("out = %+v err = %v", out, err)
	}
	// A native tool_use still wins over any text.
	out, err = resolveResponse(Response{
		Text:     dsmlFullWidth,
		ToolUses: []ToolUse{{ID: "toolu_1", Name: "taskq_list", Input: map[string]any{}}},
	}, true)
	if err != nil || out.Kind != KindToolCall || out.Tool.ID != "toolu_1" {
		t.Fatalf("native path broken: out = %+v err = %v", out, err)
	}
}

func TestDeltaGuardSuppressesDSML(t *testing.T) {
	var got []string
	g := newDeltaGuard(func(s string) { got = append(got, s) }, nil)
	// Marker split across deltas, preceded by prose.
	for _, d := range []string{"我来调度 codex", " 去探索。<", "||DS", "ML||tool_calls>\n<||DSML||invoke name=\"taskq_priority\">rest"} {
		g.on(d)
	}
	g.flush()
	joined := strings.Join(got, "")
	if ContainsDSMLToolCall(joined) {
		t.Errorf("streamed text carries DSML markup: %q", joined)
	}
	if !strings.Contains(joined, "我来调度 codex 去探索。") {
		t.Errorf("prose before markup lost: %q", joined)
	}
}

func TestDeltaGuardSuppressesLeadingDSML(t *testing.T) {
	var got []string
	g := newDeltaGuard(func(s string) { got = append(got, s) }, nil)
	for _, d := range []string{"<｜", "｜DSML｜｜tool_calls>\n<｜｜DSML｜｜invoke name=\"x\">…"} {
		g.on(d)
	}
	g.flush()
	if joined := strings.Join(got, ""); ContainsDSMLToolCall(joined) {
		t.Errorf("streamed text carries DSML markup: %q", joined)
	}
}

func TestDeltaGuardKeepsLegitAngleBracketProse(t *testing.T) {
	var got []string
	g := newDeltaGuard(func(s string) { got = append(got, s) }, nil)
	for _, d := range []string{"<deta", "ils> 是一个合法的 HTML 标签，<tag> 也是。"} {
		g.on(d)
	}
	g.flush()
	if joined := strings.Join(got, ""); joined != "<details> 是一个合法的 HTML 标签，<tag> 也是。" {
		t.Errorf("prose corrupted: %q", joined)
	}
}

// A stream that ends on a marker prefix fragment (e.g. a lone "<") must not
// lose the withheld bytes at flush.
func TestDeltaGuardFlushDeliversUndecidedBuffer(t *testing.T) {
	var got []string
	g := newDeltaGuard(func(s string) { got = append(got, s) }, nil)
	for _, d := range []string{"比较 a 和 b：a ", "<", " b"} {
		g.on(d)
	}
	g.flush()
	if joined := strings.Join(got, ""); joined != "比较 a 和 b：a < b" {
		t.Errorf("withheld bytes lost at flush: %q", joined)
	}
}

func TestStripDSMLToolCalls(t *testing.T) {
	if got := StripDSMLToolCalls("普通回答"); got != "普通回答" {
		t.Errorf("plain text changed: %q", got)
	}
	if got := StripDSMLToolCalls("前半保留。\n" + dsmlFullWidth); got != "前半保留。" {
		t.Errorf("strip = %q", got)
	}
	if got := StripDSMLToolCalls("前。\n" + dsmlASCII + "\n尾巴保留。"); got != "前。\n尾巴保留。" {
		t.Errorf("strip with suffix = %q", got)
	}
	if got := StripDSMLToolCalls(dsmlFullWidth); got != "" {
		t.Errorf("markup-only strip = %q, want empty", got)
	}
}

func TestRegistryCopy(t *testing.T) {
	reg := NewRegistry()
	reg.Register(Tool{Name: "taskq_list", Schema: map[string]any{}})
	reg.Register(Tool{Name: "task_submit", Schema: map[string]any{}})
	dup := reg.Copy()
	if _, ok := dup.Lookup("taskq_list"); !ok {
		t.Error("copy must carry every tool")
	}
	dup.Register(Tool{Name: "extra_tool", Schema: map[string]any{}})
	if _, ok := reg.Lookup("extra_tool"); ok {
		t.Error("registering on the copy must not leak into the shared registry")
	}
	if len(dup.Specs()) != len(reg.Specs())+1 {
		t.Error("copy and original specs diverged unexpectedly")
	}
}

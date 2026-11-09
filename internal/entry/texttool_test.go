// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"strings"
	"testing"
)

// The shape GLM emitted in the wild: a bare name, one arg pair, the closing
// tag on its own line. This is the case that leaked to a user's side
// conversation as an "answer".
const glmToolCall = "<tool_call>workspace_list<arg_key>path</arg_key><arg_value>projects</arg_value>\n</tool_call>"

func TestContainsTextualToolCall(t *testing.T) {
	for _, s := range []string{
		glmToolCall,
		"前缀说明。\n" + glmToolCall,
		"<tool_call>taskq_list</tool_call>",
		dsmlFullWidth, // the DSML dialect is still covered
	} {
		if !ContainsTextualToolCall(s) {
			t.Errorf("ContainsTextualToolCall(%q) = false, want true", s)
		}
	}
	for _, s := range []string{
		"",
		"普通回答，没有标记",
		"<tool> 不是调用标记",
		"tool_call 单独出现不算",
	} {
		if ContainsTextualToolCall(s) {
			t.Errorf("ContainsTextualToolCall(%q) = true, want false", s)
		}
	}
}

func TestParseXMLToolCalls(t *testing.T) {
	uses, preamble, ok := parseXMLToolCalls("我来列一下目录。\n" + glmToolCall)
	if !ok || len(uses) != 1 {
		t.Fatalf("uses = %+v ok = %v", uses, ok)
	}
	if uses[0].Name != "workspace_list" {
		t.Errorf("name = %q", uses[0].Name)
	}
	if got := uses[0].Input["path"]; got != "projects" {
		t.Errorf("path = %#v, want projects", got)
	}
	if preamble != "我来列一下目录。" {
		t.Errorf("preamble = %q", preamble)
	}
}

func TestParseXMLToolCallsMultipleAndNoArgs(t *testing.T) {
	src := "<tool_call>taskq_list</tool_call>\n" +
		"<tool_call>workspace_list<arg_key>path</arg_key><arg_value>.</arg_value></tool_call>"
	uses, _, ok := parseXMLToolCalls(src)
	if !ok || len(uses) != 2 {
		t.Fatalf("uses = %+v ok = %v", uses, ok)
	}
	if uses[0].Name != "taskq_list" || len(uses[0].Input) != 0 {
		t.Errorf("first = %+v", uses[0])
	}
	if uses[1].Name != "workspace_list" || uses[1].Input["path"] != "." {
		t.Errorf("second = %+v", uses[1])
	}
}

// A response cut at max_tokens leaves the closing tag missing. The call the
// model did write must still run.
func TestParseXMLToolCallsTruncatedBlock(t *testing.T) {
	uses, _, ok := parseXMLToolCalls("<tool_call>workspace_read<arg_key>path</arg_key><arg_value>a.go</arg_value>")
	if !ok || len(uses) != 1 || uses[0].Name != "workspace_read" {
		t.Fatalf("uses = %+v ok = %v", uses, ok)
	}
}

// Prose that talks about the template is not a call: the name slot has to be
// a real tool-name token.
func TestParseXMLToolCallsRejectsProse(t *testing.T) {
	for _, src := range []string{
		"<tool_call> 是一个标签，用来包裹工具名",
		"<tool_call>\n(see the docs)\n</tool_call>",
		"<tool_call><arg_key>path</arg_key><arg_value>x</arg_value></tool_call>",
	} {
		if uses, _, ok := parseXMLToolCalls(src); ok {
			t.Errorf("parseXMLToolCalls(%q) = %+v, want no call", src, uses)
		}
	}
}

func TestXMLParamValue(t *testing.T) {
	cases := map[string]any{
		"true":     true,
		"false":    false,
		"null":     nil,
		"42":       float64(42),
		"3.5":      float64(3.5),
		"projects": "projects",
		"a b":      "a b",
		`{"k":1}`:  map[string]any{"k": float64(1)},
	}
	for in, want := range cases {
		if got := xmlParamValue(in); !equalAny(got, want) {
			t.Errorf("xmlParamValue(%q) = %#v, want %#v", in, got, want)
		}
	}
}

func equalAny(a, b any) bool {
	am, ok1 := a.(map[string]any)
	bm, ok2 := b.(map[string]any)
	if ok1 || ok2 {
		if !ok1 || !ok2 || len(am) != len(bm) {
			return false
		}
		for k, av := range am {
			if bv, ok := bm[k]; !ok || av != bv {
				return false
			}
		}
		return true
	}
	return a == b
}

func TestResolveXMLToolCallRecovers(t *testing.T) {
	out, err := resolveXMLToolCall(Response{Text: glmToolCall}, true)
	if err != nil || out.Kind != KindToolCall {
		t.Fatalf("out = %+v err = %v", out, err)
	}
	if out.Tool.Tool != "workspace_list" || out.Tool.Arguments["path"] != "projects" {
		t.Errorf("tool = %+v", out.Tool)
	}
}

func TestResolveXMLToolCallToolFreeRound(t *testing.T) {
	// Prose plus markup: keep the prose, drop the markup.
	out, err := resolveXMLToolCall(Response{Text: "先说明一句。\n" + glmToolCall}, false)
	if err != nil || out.Kind != KindAnswer || out.Answer != "先说明一句。" {
		t.Fatalf("out = %+v err = %v", out, err)
	}
	// Markup alone: reject with an actionable message that does not quote it.
	_, err = resolveXMLToolCall(Response{Text: glmToolCall}, false)
	ce, ok := err.(*ClassifyError)
	if !ok {
		t.Fatalf("err = %T (%v), want *ClassifyError", err, err)
	}
	if IsFatalModelError(ce) {
		t.Error("textual tool-call reject must not trigger model fallback")
	}
	if strings.Contains(ce.UserMsg, "tool_call") {
		t.Error("user message must not quote the markup")
	}
}

func TestResolveResponseXMLToolCall(t *testing.T) {
	out, err := resolveResponse(Response{Text: glmToolCall}, true)
	if err != nil || out.Kind != KindToolCall {
		t.Fatalf("out = %+v err = %v", out, err)
	}
	// A native tool_use still wins over textual markup.
	out, err = resolveResponse(Response{
		Text:     glmToolCall,
		ToolUses: []ToolUse{{ID: "toolu_1", Name: "taskq_list", Input: map[string]any{}}},
	}, true)
	if err != nil || out.Kind != KindToolCall || out.Tool.ID != "toolu_1" {
		t.Fatalf("native path broken: out = %+v err = %v", out, err)
	}
}

func TestStripTextualToolCalls(t *testing.T) {
	if got := StripTextualToolCalls("普通回答"); got != "普通回答" {
		t.Errorf("plain text changed: %q", got)
	}
	if got := StripTextualToolCalls("前半保留。\n" + glmToolCall); got != "前半保留。" {
		t.Errorf("strip = %q", got)
	}
	if got := StripTextualToolCalls("前。\n" + glmToolCall + "\n尾巴保留。"); got != "前。\n尾巴保留。" {
		t.Errorf("strip with suffix = %q", got)
	}
	if got := StripTextualToolCalls(glmToolCall); got != "" {
		t.Errorf("markup-only strip = %q, want empty", got)
	}
	if got := StripTextualToolCalls("前。\n" + dsmlFullWidth + "\n尾。"); got != "前。\n尾。" {
		t.Errorf("DSML strip through the shared helper = %q", got)
	}
}

func TestDeltaGuardSuppressesLeadingXMLToolCall(t *testing.T) {
	var got []string
	g := newDeltaGuard(func(s string) { got = append(got, s) }, nil)
	for _, d := range []string{"<tool_c", "all>workspace_list<arg_key>path</arg_key>", "<arg_value>projects</arg_value></tool_call>"} {
		g.on(d)
	}
	g.flush()
	if joined := strings.Join(got, ""); joined != "" {
		t.Errorf("markup leaked to the stream: %q", joined)
	}
}

// The tag is ordinary text when it appears inside an answer — only a response
// that OPENS with it is markup. Swallowing the rest of a real answer would be
// worse than showing a tag.
func TestDeltaGuardKeepsInlineXMLToolCallProse(t *testing.T) {
	var got []string
	g := newDeltaGuard(func(s string) { got = append(got, s) }, nil)
	g.on("GLM 的模板是 ")
	g.on(glmToolCall)
	g.flush()
	if joined := strings.Join(got, ""); !strings.Contains(joined, "GLM 的模板是") || !strings.Contains(joined, "<tool_call>") {
		t.Errorf("inline prose corrupted: %q", joined)
	}
}

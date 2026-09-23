package entry

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// DSML ("<||DSML||tool_calls>…") is the textual tool-call markup some
// compatible endpoints (DeepSeek/Kimi-style relays and small models behind
// them) emit inside the content field instead of native tool_use blocks. The
// delimiters also appear with full-width pipes ("<｜｜DSML｜｜…") when the model
// is generating CJK text. Without a recovery path the markup falls through
// the JSON/prose parser and leaks to the user as the "answer" — and the tool
// call the model intended never happens.

// dsmlOpenMarkers are the opening delimiters of any DSML tag (tool_calls,
// invoke, parameter). DeepSeek's own template uses single full-width pipes
// ("<｜DSML｜…", same convention as its "<｜begin▁of▁sentence｜>" tokens); relays
// and lookalike models emit the other three half/full × single/double
// combinations, so all four are markers. dsmlCloseMarkers are the closing
// forms; they only widen detection (a response carrying just a stray closing
// tag is still markup, never an answer).
var dsmlOpenMarkers = []string{"<||DSML||", "<|DSML|", "<｜｜DSML｜｜", "<｜DSML｜"}
var dsmlCloseMarkers = []string{"</||DSML||", "</|DSML|", "</｜｜DSML｜｜", "</｜DSML｜"}

// ContainsDSMLToolCall reports whether s carries DSML tool-call markup in
// either pipe variant, opening or closing.
func ContainsDSMLToolCall(s string) bool {
	for _, m := range dsmlOpenMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	for _, m := range dsmlCloseMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// StripDSMLToolCalls removes DSML tool-call markup from text, keeping the
// prose around it: everything before the first opening marker, plus anything
// after the final closing tool_calls/invoke tag. Text without markup is
// returned unchanged.
func StripDSMLToolCalls(text string) string {
	start := dsmlStartIndex(text)
	if start < 0 {
		return text
	}
	out := strings.TrimSpace(text[:start])
	// Keep whatever prose follows the LAST closing tag of any variant — taking
	// the maximum end position so a truncated document whose final tag is an
	// invoke (or a different pipe variant) still strips everything markup-side.
	end := -1
	for _, m := range dsmlCloseMarkers {
		for _, tag := range []string{m + "tool_calls>", m + "invoke>", m + "parameter>"} {
			if i := strings.LastIndex(text, tag); i >= 0 && i+len(tag) > end {
				end = i + len(tag)
			}
		}
	}
	if end >= 0 {
		if suffix := strings.TrimSpace(text[end:]); suffix != "" {
			if out != "" {
				out += "\n" + suffix
			} else {
				out = suffix
			}
		}
	}
	return out
}

// dsmlStartIndex returns the offset of the first opening DSML marker in s,
// or -1. Used by the stream guard to suppress markup mid-stream and by the
// parser to split the prose preamble from the markup section.
func dsmlStartIndex(s string) int {
	best := -1
	for _, m := range dsmlOpenMarkers {
		if i := strings.Index(s, m); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best
}

// dsmlMarkerPrefix reports whether s is a proper prefix of an opening marker
// — a marker split across stream deltas. The input is always complete UTF-8
// runes (deltas arrive as strings), and a complete rune can never equal a
// byte-prefix of another rune, so byte-level prefix matching is safe even
// though the full-width variant is multi-byte.
func dsmlMarkerPrefix(s string) bool {
	for _, m := range dsmlOpenMarkers {
		if len(s) < len(m) && strings.HasPrefix(m, s) {
			return true
		}
	}
	return false
}

// dsmlHoldbackLen returns the length of the longest suffix of s that is a
// proper prefix of an opening marker — the bytes a stream guard must withhold
// because the next delta may complete the marker.
func dsmlHoldbackLen(s string) int {
	n := 0
	for _, m := range dsmlOpenMarkers {
		for l := len(m) - 1; l > n; l-- {
			if l <= len(s) && strings.HasSuffix(s, m[:l]) {
				n = l
				break
			}
		}
	}
	return n
}

// dsmlInvokeRe matches one invoke block on the pipe-normalized text (full-
// width ｜ is folded to | first, so \|{1,2} covers all four delimiter
// variants); the closing tag is optional for a truncated final invoke. The
// name attribute tolerates double quotes, single quotes, and no quotes —
// relays rewrite attributes on the way through. dsmlParamRe does the same
// for one parameter inside an invoke body.
var (
	dsmlInvokeRe = regexp.MustCompile(`(?s)<\|{1,2}DSML\|{1,2}invoke\s+name=(?:"([^"]*)"|'([^']*)'|([^\s>/]+))[^>]*>(.*?)(?:<\/\|{1,2}DSML\|{1,2}invoke>|$)`)
	dsmlParamRe  = regexp.MustCompile(`(?s)<\|{1,2}DSML\|{1,2}parameter\s+name=(?:"([^"]*)"|'([^']*)'|([^\s>/]+))([^>]*)>(.*?)<\/\|{1,2}DSML\|{1,2}parameter>`)
)

// parseDSMLToolCalls extracts the invoke blocks from text carrying DSML
// markup, returning them as ToolUses (no IDs: like the text-JSON fallback,
// the call is replayed as prose, not as native tool_use blocks) plus the
// prose that preceded the markup. ok is false when no usable invoke exists —
// the markup is then treated as unparsable.
func parseDSMLToolCalls(text string) (uses []ToolUse, preamble string, ok bool) {
	start := dsmlStartIndex(text)
	if start < 0 {
		return nil, "", false
	}
	preamble = strings.TrimSpace(text[:start])
	norm := strings.ReplaceAll(text[start:], "｜", "|")
	for _, m := range dsmlInvokeRe.FindAllStringSubmatch(norm, -1) {
		name := strings.TrimSpace(firstNonEmpty(m[1], m[2], m[3]))
		if name == "" {
			continue
		}
		body := m[4]
		params := map[string]any{}
		for _, pm := range dsmlParamRe.FindAllStringSubmatch(body, -1) {
			key := strings.TrimSpace(firstNonEmpty(pm[1], pm[2], pm[3]))
			if key == "" {
				continue
			}
			params[key] = dsmlParamValue(pm[4], pm[5])
		}
		if len(params) == 0 {
			// Some relays emit the invoke body as a bare JSON object instead of
			// parameter tags; accept it rather than dropping a clean call.
			if js := strings.TrimSpace(body); strings.HasPrefix(js, "{") {
				var obj map[string]any
				if json.Unmarshal([]byte(js), &obj) == nil {
					params = obj
				}
			}
		}
		uses = append(uses, ToolUse{Name: name, Input: params})
	}
	return uses, preamble, len(uses) > 0
}

// firstNonEmpty returns the first non-empty alternative — the invoke/parameter
// name regexes offer three capture groups (double-quoted, single-quoted,
// bare) of which at most one is populated.
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// dsmlParamValue types one parameter value: an explicit string="true" stays a
// string; anything else tries JSON first (numbers, booleans) so tools with
// numeric arguments (e.g. taskq_move's seq) still parse, and falls back to a
// plain string.
func dsmlParamValue(attrs, raw string) any {
	v := strings.TrimSpace(raw)
	if strings.Contains(attrs, `string="true"`) || strings.Contains(attrs, `string='true'`) || strings.Contains(attrs, `string=true`) {
		return v
	}
	var parsed any
	if err := json.Unmarshal([]byte(v), &parsed); err == nil {
		switch parsed.(type) {
		case float64, bool:
			return parsed
		}
	}
	return v
}

// resolveDSML routes a response whose tool call arrived as DSML markup in the
// text content. With tools offered, a parseable call is RECOVERED into a
// KindToolCall output — the loop executes it through the registry exactly
// like a native tool_use, and a hallucinated tool name comes back as the
// registry's unknown-tool result the model can correct. In a tool-free round
// the call cannot run: the markup is stripped and any prose the model emitted
// alongside is kept as the answer, and a markup-only response is REJECTED
// with an actionable error.
//
// Markup that carries no usable invoke is not an automatic failure. A stray or
// half-emitted marker can sit next to a perfectly good answer ("… done.
// <||DSML||tool_calls>" with nothing after it), and rejecting the whole turn
// for it cost the user the entire ask — the tool loop retried the same
// deterministic response until its budget ran out. So the markup is stripped
// and the remainder is handed to the ordinary JSON/prose parser; the error only
// stands when nothing usable is left. What never happens either way is raw
// markup reaching the user as an answer.
func resolveDSML(resp Response, toolsOffered bool) (Output, error) {
	uses, preamble, ok := parseDSMLToolCalls(resp.Text)
	if ok && toolsOffered {
		// Every invoke becomes a call the loop executes this round — a batch
		// request ("clean the queue") emits one invoke per target and must not
		// be serialized into one round per call.
		out := Output{Kind: KindToolCall, Note: preamble}
		for _, u := range uses {
			out.Tools = append(out.Tools, &ToolCall{Tool: u.Name, Arguments: u.Input})
		}
		out.Tool = out.Tools[0]
		return out, nil
	}
	if ok {
		if preamble != "" {
			return Output{Kind: KindAnswer, Answer: preamble}, nil
		}
		return Output{}, &ClassifyError{
			UserMsg: "模型输出了文本化工具调用（DSML 协议），但当前会话阶段不接受工具调用，已拒绝执行。请重试，或在设置中更换支持原生工具调用的入口模型。",
			Err:     fmt.Errorf("entry: DSML tool call %q in a tool-free round", uses[0].Name),
		}
	}
	// Unparsable markup: strip it and let the normal parser try the rest.
	if stripped := strings.TrimSpace(StripDSMLToolCalls(resp.Text)); stripped != "" {
		if out, err := ParseOutput(stripped); err == nil {
			const note = "（已忽略模型输出中无法解析的工具调用标记）"
			if out.Note == "" {
				out.Note = note
			} else {
				out.Note += " " + note
			}
			return out, nil
		}
	}
	return Output{}, &ClassifyError{
		UserMsg: "模型以 DSML 文本形式返回了工具调用，但无法解析为有效调用（接入点协议不兼容）。请重试，或更换支持原生工具调用的入口模型/接入点。",
		// The offending markup goes into the wrapped cause, not the user message:
		// it is the only evidence of what the endpoint actually emitted, and an
		// "unparsable" error with no sample is undiagnosable. Bounded and
		// collapsed to one line so a runaway response cannot flood the log.
		Err: fmt.Errorf("entry: unparsable DSML tool-call markup: %s", dsmlSample(resp.Text)),
	}
}

// dsmlSample renders a bounded, single-line preview of markup that failed to
// parse, for the log. This is what turns "unparsable" into a fixable report: the
// delimiters and the attribute order are exactly what the parser needs to match.
func dsmlSample(text string) string {
	const limit = 300
	s := strings.Join(strings.Fields(text), " ")
	if s == "" {
		return "(empty)"
	}
	if len(s) > limit {
		s = s[:limit] + "…"
	}
	return s
}

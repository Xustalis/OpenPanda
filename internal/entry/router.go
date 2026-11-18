// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// jsonLeadInMax is the longest prose lead-in tolerated before an embedded JSON
// object is treated as a directive. Reasoning models emit a chain-of-thought
// preamble before committing to the JSON, so the limit must be generous; it
// only stops a JSON object buried deep in a long prose answer from being
// executed (paired with looksIllustrative below).
const jsonLeadInMax = 2000

// illustrativeMarkers are words that signal a prose answer is *showing* a JSON
// example rather than *committing* to a directive. When any appears in the
// lead-in prose, the embedded JSON is not executed.
var illustrativeMarkers = []string{
	"示例", "例如", "比如", "样例", "仅供参考",
	"for example", "e.g.",
}

// looksIllustrative reports whether prose reads as showing an example JSON
// rather than committing to a directive.
func looksIllustrative(prose string) bool {
	for _, m := range illustrativeMarkers {
		if strings.Contains(prose, m) {
			return true
		}
	}
	return false
}

// ErrDirectiveProse marks model text that announced an entry directive but is
// not a usable one. It is an error rather than an answer on purpose: the
// envelope is a protocol artifact, and handing it to the user is the exact
// failure this sentinel exists to prevent (a truncated task JSON once reached
// the chat pane verbatim, and the task it described was never created). The
// ask loop recognizes it and asks the model to redo the turn through a tool.
var ErrDirectiveProse = errors.New("entry: directive written as prose")

// directiveKinds are the envelope kinds the runtime must execute rather than
// show. kind=answer is excluded: that envelope is unwrapped by decodeEnvelope.
var directiveKinds = []string{
	string(KindTask), string(KindPlan), string(KindToolCall), string(KindClarify),
}

// directiveKindHead bounds the search for the kind field, so a prose answer
// that happens to contain an early brace cannot be scanned to the end.
const directiveKindHead = 512

// maxDirectiveRepairClosers bounds how many unterminated containers the repair
// pass will close. A directive is one shallow tree; a body that left more than
// this open was not cut off at the tail, it was never a directive.
const maxDirectiveRepairClosers = 8

// DeclaresDirective reports whether text carries — or tried to carry — an entry
// directive envelope: a JSON object announcing kind=task/plan/tool_call/clarify.
// A short prose lead-in is tolerated (reasoning models emit one), but a JSON
// object buried deep in a long answer is data the user asked about, not a
// directive. The detection is deliberately syntactic: it must work on text that
// failed to parse, which is the whole point.
func DeclaresDirective(text string) bool {
	i := strings.IndexByte(text, '{')
	if i < 0 || i > jsonLeadInMax {
		return false
	}
	head := text[i:]
	if len(head) > directiveKindHead {
		head = head[:directiveKindHead]
	}
	return declaresDirectiveKind(head)
}

// declaresDirectiveKind reports whether obj — which must start at a '{' — opens
// with a kind field naming an executable directive.
func declaresDirectiveKind(obj string) bool {
	j := strings.Index(obj, `"kind"`)
	if j < 0 {
		return false
	}
	rest := strings.TrimLeft(obj[j+len(`"kind"`):], " \t\r\n")
	if !strings.HasPrefix(rest, ":") {
		return false
	}
	rest = strings.TrimLeft(rest[1:], " \t\r\n")
	for _, k := range directiveKinds {
		if strings.HasPrefix(rest, `"`+k+`"`) {
			return true
		}
	}
	return false
}

// closeTruncatedJSON closes the containers a directive left open at its tail.
// A model that stops one brace short still described valid work — the observed
// failure was exactly three closers for a four-level envelope — and the repair
// is only ever accepted after the same validation a complete envelope passes,
// so a half-written spec is refused, not executed. ok is false when the text is
// not merely truncated: inside an unterminated string (the tail is lost, not
// just the closing brace), unbalanced in a way no suffix can fix, or already
// balanced (its problem is not truncation).
func closeTruncatedJSON(s string) (string, bool) {
	var open []byte
	inString, escaped := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			open = append(open, '}')
		case '[':
			open = append(open, ']')
		case '}', ']':
			if len(open) == 0 || open[len(open)-1] != c {
				return "", false
			}
			open = open[:len(open)-1]
		}
	}
	if inString || len(open) == 0 || len(open) > maxDirectiveRepairClosers {
		return "", false
	}
	var b strings.Builder
	b.Grow(len(s) + len(open))
	b.WriteString(s)
	for i := len(open) - 1; i >= 0; i-- {
		b.WriteByte(open[i])
	}
	return b.String(), true
}

// ParseOutput turns raw model text into a validated Output. The model may emit
// JSON for tool_call/task or plain prose for answer. Text that is not a
// directive falls back to KindAnswer (never a silent error for the user —
// design doc §7.4); text that *announced* a directive and then failed to be one
// is refused with ErrDirectiveProse instead, because failing open there turns
// protocol into chat.
func ParseOutput(raw string) (Output, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Output{}, fmt.Errorf("entry: empty model output")
	}

	// Strip markdown fences the model may wrap the JSON in.
	raw = stripFences(raw)

	// Fast path: the whole text is a structured directive.
	if out, ok, err := decodeEnvelope(raw); err != nil {
		return Output{}, err
	} else if ok {
		return out, nil
	}

	// The model sometimes prefixes a structured directive with prose — a short
	// lead-in sentence or a chain-of-thought preamble — or trails one after it.
	// Extract the first balanced JSON object and accept it only if the lead-in is
	// not too long and does not read as showing an example (rather than
	// committing to a directive).
	if obj := extractJSONObject(raw); obj != "" && obj != raw {
		lead := strings.TrimSpace(raw[:strings.IndexByte(raw, '{')])
		if len(lead) <= jsonLeadInMax && !looksIllustrative(lead) {
			if out, ok, err := decodeEnvelope(obj); err != nil {
				return Output{}, err
			} else if ok {
				return out, nil
			}
		}
	}

	// A directive cut short at its tail is still a directive — no balanced
	// object exists, so the extraction above could not reach it. Close what the
	// model left open, then decode the repair STRICTLY: the observed failure that
	// motivated this path wrote the spec's own fields inside objectives, so a
	// brace count can balance while the envelope is mis-shaped. encoding/json
	// drops an unknown field silently, validation then passes on a task whose
	// scope and target were lost, and the runtime dispatches the wrong work.
	// Strict decoding turns that into a refusal, which the ask loop retries
	// through the task_submit tool — a path that cannot truncate mid-message.
	if strings.HasPrefix(raw, "{") && declaresDirectiveKind(raw) {
		if fixed, ok := closeTruncatedJSON(raw); ok {
			if out, ok, err := decodeRepairedEnvelope(fixed); err != nil {
				return Output{}, err
			} else if ok {
				return out, nil
			}
		}
		return Output{}, ErrDirectiveProse
	}

	// Nothing parsed, yet the text still announces a directive: the model wrote
	// its structured output into the message body. Prose that merely *shows* an
	// example keeps its answer status.
	if lead, ok := directiveLeadIn(raw); ok && !looksIllustrative(lead) {
		return Output{}, ErrDirectiveProse
	}

	return Output{Kind: KindAnswer, Answer: raw}, nil
}

// directiveLeadIn returns the prose before an announced directive, and whether
// the text announces one at all.
func directiveLeadIn(raw string) (string, bool) {
	if !DeclaresDirective(raw) {
		return "", false
	}
	return strings.TrimSpace(raw[:strings.IndexByte(raw, '{')]), true
}

// decodeEnvelope unmarshals raw into the top-level envelope and resolves it to
// an Output. The ok result is true when raw is a valid tool_call/task/plan
// directive or an {"kind":"answer","answer":"…"} envelope with a non-empty
// answer; a non-JSON payload, an empty/contentless answer, or an unknown kind
// returns ok=false with a nil error. An error is returned only when raw is valid
// JSON but fails validation (a model error that must surface, never degrade to
// an answer).
func decodeEnvelope(raw string) (Output, bool, error) {
	// Only an object can be an envelope. A bare JSON scalar ("2", "true",
	// "\"yes\"") is a valid JSON value that merely fails to unmarshal into
	// the struct — that is a terse answer, not a malformed directive, so it
	// must fall back to KindAnswer instead of surfacing a validation error.
	if raw == "" || raw[0] != '{' {
		return Output{}, false, nil
	}
	var envelope envelopeBody
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		// A syntax error means raw is not JSON, so the caller may try extraction
		// or fall back to an answer. A type error means raw is JSON but does not
		// match the envelope schema — a model error that must surface, never
		// silently degrade to an answer.
		var ute *json.UnmarshalTypeError
		if errors.As(err, &ute) {
			return Output{}, false, err
		}
		return Output{}, false, nil
	}
	return resolveEnvelope(envelope)
}

// decodeRepairedEnvelope decodes a directive this package closed itself, and
// refuses anything the schema does not name. The leniency above is right for
// text the model produced on purpose: an unknown field is forward compatibility.
// It is wrong for a repair, where an unknown field is evidence that the model
// mis-nested its closers — the observed failure put the spec fields inside
// objectives, and a lenient decode would drop scope, target and success
// definition, pass validation, and dispatch the wrong work.
func decodeRepairedEnvelope(raw string) (Output, bool, error) {
	if raw == "" || raw[0] != '{' {
		return Output{}, false, nil
	}
	var envelope envelopeBody
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&envelope); err != nil {
		return Output{}, false, nil
	}
	return resolveEnvelope(envelope)
}

// envelopeBody is the top-level directive shape. Exactly one payload matches
// Kind; the rest stay nil.
type envelopeBody struct {
	Kind    Kind         `json:"kind"`
	Answer  string       `json:"answer"`
	Tool    *ToolCall    `json:"tool"`
	Task    *TaskSpec    `json:"task"`
	Plan    *PlanSpec    `json:"plan"`
	Clarify *ClarifySpec `json:"clarify"`
}

// resolveEnvelope validates a decoded envelope and turns it into an Output.
func resolveEnvelope(envelope envelopeBody) (Output, bool, error) {
	switch envelope.Kind {
	case KindToolCall:
		if envelope.Tool == nil {
			return Output{}, false, fmt.Errorf("entry: tool_call missing tool object")
		}
		if err := ValidateToolCall(envelope.Tool); err != nil {
			return Output{}, false, err
		}
		return Output{Kind: KindToolCall, Tool: envelope.Tool}, true, nil

	case KindTask:
		if envelope.Task == nil {
			return Output{}, false, fmt.Errorf("entry: task missing task object")
		}
		if err := ValidateTaskSpec(envelope.Task); err != nil {
			return Output{}, false, err
		}
		return Output{Kind: KindTask, Task: envelope.Task}, true, nil

	case KindPlan:
		if envelope.Plan == nil {
			return Output{}, false, fmt.Errorf("entry: plan missing plan object")
		}
		if err := ValidatePlanSpec(envelope.Plan); err != nil {
			return Output{}, false, err
		}
		return Output{Kind: KindPlan, Plan: envelope.Plan}, true, nil

	case KindClarify:
		if envelope.Clarify == nil {
			return Output{}, false, fmt.Errorf("entry: clarify missing clarify object")
		}
		if err := ValidateClarifySpec(envelope.Clarify); err != nil {
			return Output{}, false, err
		}
		return Output{Kind: KindClarify, Clarify: envelope.Clarify}, true, nil

	case KindAnswer:
		// A model that wraps its reply as {"kind":"answer","answer":"…"} instead
		// of emitting bare prose must be unwrapped, or the raw JSON envelope leaks
		// to the user as the "answer". A contentless envelope is a model error:
		// falling back to prose handling would print the hollow "{...}" verbatim
		// (the whole text is the envelope), so surface it for a retry instead.
		if strings.TrimSpace(envelope.Answer) == "" {
			return Output{}, false, fmt.Errorf("entry: answer envelope carries no content")
		}
		return Output{Kind: KindAnswer, Answer: envelope.Answer}, true, nil

	default:
		// Unknown kinds carry no payload; the caller falls back to prose handling.
		return Output{}, false, nil
	}
}

// stripFences removes a surrounding ```json / ``` fence if present.
func stripFences(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the opening fence line (optionally ```json).
	rest := s[3:]
	if i := strings.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[i+1:]
	}
	// Drop the trailing closing fence.
	if idx := strings.LastIndex(rest, "```"); idx >= 0 {
		rest = strings.TrimSpace(rest[:idx])
	}
	return rest
}

// extractJSONObject returns the first balanced JSON object in s — from the
// first '{' to its matching '}' — or "" when none exists. It walks the bytes so
// a brace inside a JSON string or a nested object does not terminate the scan
// early.
func extractJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

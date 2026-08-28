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

// ParseOutput turns raw model text into a validated Output. The model may emit
// JSON for tool_call/task or plain prose for answer. Anything that is not a
// valid tool_call/task JSON object falls back to KindAnswer (never a silent
// error for the user — design doc §7.4).
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
	// lead-in sentence or a chain-of-thought preamble. Extract the first balanced
	// JSON object and accept it only if the lead-in is not too long and does not
	// read as showing an example (rather than committing to a directive).
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

	return Output{Kind: KindAnswer, Answer: raw}, nil
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
	var envelope struct {
		Kind   Kind      `json:"kind"`
		Answer string    `json:"answer"`
		Tool   *ToolCall `json:"tool"`
		Task   *TaskSpec `json:"task"`
		Plan   *PlanSpec `json:"plan"`
	}
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

	case KindAnswer:
		// A model that wraps its reply as {"kind":"answer","answer":"…"} instead
		// of emitting bare prose must be unwrapped, or the raw JSON envelope leaks
		// to the user as the "answer". Only a non-empty answer field is a real
		// directive; an empty one falls back to prose handling so we never render
		// a contentless envelope.
		if strings.TrimSpace(envelope.Answer) == "" {
			return Output{}, false, nil
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

package entry

import (
	"encoding/json"
	"fmt"
	"strings"
)

// jsonLeadInMax is the longest prose lead-in tolerated before an embedded JSON
// object is treated as a directive. A short lead-in ("任务如下：") is a real
// directive; a JSON object buried deeper in prose is an illustrative example.
const jsonLeadInMax = 100

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

	// The model sometimes prefixes a structured directive with a sentence of
	// prose. Extract the first balanced JSON object and retry — but only when the
	// object starts near the beginning (a short lead-in); a JSON object buried in
	// a long prose answer is an illustrative example, not a directive, and must
	// fall back to answer.
	if obj := extractJSONObject(raw); obj != "" && obj != raw {
		if lead := strings.TrimSpace(raw[:strings.IndexByte(raw, '{')]); len(lead) <= jsonLeadInMax {
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
// an Output. The ok result is true only when raw is a valid tool_call/task
// directive; a non-JSON payload or an answer/unknown kind returns ok=false with
// a nil error. An error is returned only when raw is valid JSON but fails
// validation (a model error that must surface, never degrade to an answer).
func decodeEnvelope(raw string) (Output, bool, error) {
	var envelope struct {
		Kind Kind      `json:"kind"`
		Tool *ToolCall `json:"tool"`
		Task *TaskSpec `json:"task"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		// Not JSON: let the caller decide whether to try extraction or fall
		// back to a natural-language answer.
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

	default:
		// KindAnswer and unknown kinds carry no payload; the caller falls back
		// to prose handling.
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

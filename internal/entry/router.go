package entry

import (
	"encoding/json"
	"fmt"
	"strings"
)

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

	var envelope struct {
		Kind Kind      `json:"kind"`
		Tool *ToolCall `json:"tool"`
		Task *TaskSpec `json:"task"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		// Not JSON: treat the whole text as a natural-language answer.
		return Output{Kind: KindAnswer, Answer: raw}, nil
	}

	switch envelope.Kind {
	case KindToolCall:
		if envelope.Tool == nil {
			return Output{}, fmt.Errorf("entry: tool_call missing tool object")
		}
		if err := ValidateToolCall(envelope.Tool); err != nil {
			return Output{}, err
		}
		return Output{Kind: KindToolCall, Tool: envelope.Tool}, nil

	case KindTask:
		if envelope.Task == nil {
			return Output{}, fmt.Errorf("entry: task missing task object")
		}
		if err := ValidateTaskSpec(envelope.Task); err != nil {
			return Output{}, err
		}
		return Output{Kind: KindTask, Task: envelope.Task}, nil

	case KindAnswer:
		// KindAnswer JSON carries no payload; fall through to prose handling.
		return Output{Kind: KindAnswer, Answer: raw}, nil

	default:
		// Unknown or missing kind: not a structured directive, so treat as an
		// answer rather than failing the user.
		return Output{Kind: KindAnswer, Answer: raw}, nil
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

package entry

import "fmt"

// validRisk is the set of acceptable risk levels. The model must pick one;
// anything else is rejected so callers can rely on the field.
var validRisk = map[string]bool{
	"low": true, "medium": true, "high": true, "critical": true,
}

// validContextType is the set of context kinds (design doc §12.5). The model
// chooses one; the Go core validates it rather than trusting the model.
var validContextType = map[string]bool{
	"file": true, "command": true, "hardware": true, "stream": true,
}

// ValidateTaskSpec checks a task emitted by the model for the fields the core
// needs before it can be persisted and routed. It returns an error describing
// the first problem; the model output is never trusted verbatim.
func ValidateTaskSpec(t *TaskSpec) error {
	if t == nil {
		return fmt.Errorf("entry: task is nil")
	}
	if t.Title == "" {
		return fmt.Errorf("entry: task.title is required")
	}
	if t.ContextType == "" {
		return fmt.Errorf("entry: task.context_type is required")
	}
	if !validContextType[t.ContextType] {
		return fmt.Errorf("entry: task.context_type %q invalid", t.ContextType)
	}
	if len(t.Requires.Abilities) == 0 {
		return fmt.Errorf("entry: task.requires.abilities is required")
	}
	if t.Complexity < 0 || t.Complexity > 1 {
		return fmt.Errorf("entry: task.complexity out of range [0,1]: %v", t.Complexity)
	}
	if t.Risk != "" && !validRisk[t.Risk] {
		return fmt.Errorf("entry: task.risk %q invalid", t.Risk)
	}
	return nil
}

// ValidateToolCall checks a tool invocation. The tool name must be present;
// the Go core's tool whitelist (not this package) decides whether the named
// tool is actually allowed to run.
func ValidateToolCall(tc *ToolCall) error {
	if tc == nil {
		return fmt.Errorf("entry: tool is nil")
	}
	if tc.Tool == "" {
		return fmt.Errorf("entry: tool.name is required")
	}
	if tc.Arguments == nil {
		return fmt.Errorf("entry: tool.arguments is required")
	}
	return nil
}

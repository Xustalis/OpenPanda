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
	if t.ToolsPolicy != "" && t.ToolsPolicy != "minimal" && t.ToolsPolicy != "extended" {
		return fmt.Errorf("entry: task.tools_policy %q invalid (must be minimal or extended)", t.ToolsPolicy)
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

// maxPlanStages bounds a model-emitted plan at the entry boundary. The plan
// package enforces the same ceiling (plan.MaxStages) when it converts the spec;
// this check exists so a runaway plan is refused before anything downstream
// allocates for it. The two constants are deliberately independent — this one
// guards the untrusted-input boundary, that one guards the execution model.
const maxPlanStages = 64

// ValidatePlanSpec checks the shape of a model-emitted plan: the fields the
// converter needs, and the two mistakes a model actually makes — a stage with no
// id (so nothing can depend on it) and a duplicate id (so a Needs reference is
// ambiguous). Dependency correctness itself — dangling needs, cycles — is the
// plan package's job, checked once against the real Plan rather than twice
// against two representations.
func ValidatePlanSpec(p *PlanSpec) error {
	if p == nil {
		return fmt.Errorf("entry: plan is nil")
	}
	if p.Goal == "" {
		return fmt.Errorf("entry: plan.goal is required")
	}
	if len(p.Stages) == 0 {
		return fmt.Errorf("entry: plan.stages is required")
	}
	if len(p.Stages) > maxPlanStages {
		return fmt.Errorf("entry: plan has %d stages, limit is %d", len(p.Stages), maxPlanStages)
	}
	seen := make(map[string]bool, len(p.Stages))
	for i, s := range p.Stages {
		if s.ID == "" {
			return fmt.Errorf("entry: plan.stages[%d].id is required", i)
		}
		if seen[s.ID] {
			return fmt.Errorf("entry: plan has two stages named %q", s.ID)
		}
		seen[s.ID] = true
		if s.Intent == "" {
			return fmt.Errorf("entry: plan.stages[%d] (%s) has no intent", i, s.ID)
		}
	}
	return nil
}

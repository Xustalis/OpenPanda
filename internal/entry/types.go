// Package entry implements the unified entry model (design doc §7): one model
// call classifies a user request as an answer, a controlled tool call, a
// structured task, or a multi-stage plan. The model never performs side
// effects — the Go core validates and executes whatever the model emits.
package entry

// Kind is the entry model's top-level output category.
type Kind string

const (
	// KindAnswer is a plain natural-language reply.
	KindAnswer Kind = "answer"
	// KindToolCall requests a controlled tool invocation.
	KindToolCall Kind = "tool_call"
	// KindTask requests a persistent, multi-step or cross-device task.
	KindTask Kind = "task"
	// KindPlan requests a multi-stage pipeline: several tasks whose ordering and
	// data flow matter, each routed to whichever node suits it. A single task
	// cannot express "develop where the coding agent lives, train where the GPU
	// lives, report back through the device the user is holding", so a request
	// that needs different machines for different steps becomes this instead.
	KindPlan Kind = "plan"
)

// Output is the parsed result of one entry-model call. Exactly one of
// Answer/Tool/Task/Plan is populated, matching Kind. Note carries content the
// executor does not act on — text emitted alongside a tool call and any
// tool_use after the first — so callers can surface it rather than drop it.
type Output struct {
	Kind   Kind
	Answer string    // KindAnswer
	Tool   *ToolCall // KindToolCall
	Task   *TaskSpec // KindTask
	Plan   *PlanSpec // KindPlan
	Note   string    // dropped text / extra tool_use (KindToolCall)
}

// ToolCall is a validated tool invocation request. ID carries the tool_use id
// when the call came from native tool_use (so the loop can reply with a matching
// tool_result); it is empty for the text-JSON fallback path.
type ToolCall struct {
	ID        string         `json:"id,omitempty"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

// TaskSpec is the structured task payload the model emits for KindTask
// (design doc §7.3).
type TaskSpec struct {
	Title       string          `json:"title"`
	Project     string          `json:"project"`
	ContextType string          `json:"context_type"`
	Requires    Requires        `json:"requires"`
	Spec        TaskSpecDetail  `json:"spec"`
	Complexity  float64         `json:"complexity"`
	Risk        string          `json:"risk"`
	Resources   ResourceProfile `json:"resource_profile"`
}

// Requires lists the abilities a task needs (design doc §7.3).
type Requires struct {
	Abilities []string `json:"abilities"`
}

// TaskSpecDetail captures what to do, where, and how to verify success.
type TaskSpecDetail struct {
	Scope             string   `json:"scope"`
	Target            string   `json:"target"`
	Node              string   `json:"node,omitempty"` // preferred node id/name; empty lets the scheduler choose
	Constraints       []string `json:"constraints"`
	SuccessDefinition string   `json:"success_definition"`
}

// ResourceProfile is a coarse resource hint, not a safety rating (design doc
// §7.3; complexity/risk are recorded but do not auto-switch model tiers in MVP).
type ResourceProfile struct {
	CPU          int     `json:"cpu"`
	RAMGB        float64 `json:"ram_gb"`
	GPUVRAMGB    float64 `json:"gpu_vram_gb"`
	DurationHint string  `json:"duration_hint"` // short | long
}

// PlanSpec is the structured multi-stage payload the model emits for KindPlan.
// It is deliberately a mirror of internal/plan.Plan rather than that type
// itself: the plan package imports this one (a stage's resources are an
// entry.ResourceProfile), so the dependency can only run one way. The plan
// package converts a validated PlanSpec into its own Plan.
type PlanSpec struct {
	Goal   string          `json:"goal"`
	Stages []PlanStageSpec `json:"stages"`
}

// PlanStageSpec is one stage of a model-emitted plan. Requires and Resources are
// the same routing vocabulary a single task uses, so a stage is matched to a node
// by the existing scoring — the GPU stage cannot land on the Pi because of what
// it declares here.
type PlanStageSpec struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	Intent    string          `json:"intent"`
	Requires  []string        `json:"requires"`
	Needs     []string        `json:"needs"`
	Resources ResourceProfile `json:"resource_profile"`
}

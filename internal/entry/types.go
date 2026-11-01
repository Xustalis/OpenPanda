// SPDX-License-Identifier: AGPL-3.0-or-later

// Package entry implements the unified entry model (design doc §7): one model
// call classifies a user request as an answer, a controlled tool call, a
// structured task, or a multi-stage plan. The model never performs side
// effects — the Go core validates and executes whatever the model emits.
package entry

import (
	"strings"

	"github.com/Xustalis/OpenPanda/internal/contract"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

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
	// KindClarify asks the user a bounded set of choices before a task or plan
	// is created. It is the pre-execution gate: a large or ambiguous request
	// whose outcome materially depends on a decision only the user can make
	// gets one question round instead of a guess. The runtime renders the
	// questions, folds the answers into the conversation, and asks the model
	// to classify again.
	KindClarify Kind = "clarify"
)

// Output is the parsed result of one entry-model call. Exactly one of
// Answer/Tool/Task/Plan is populated, matching Kind. Note carries content the
// executor does not act on — text emitted alongside a tool call — so callers
// can surface it rather than drop it.
type Output struct {
	Kind    Kind
	Answer  string       // KindAnswer
	Tool    *ToolCall    // KindToolCall — the first call (kept for simple consumers)
	Tools   []*ToolCall  // KindToolCall — every call the model emitted this round
	Task    *TaskSpec    // KindTask
	Plan    *PlanSpec    // KindPlan
	Clarify *ClarifySpec // KindClarify
	Note    string       // text emitted alongside the tool call(s) (KindToolCall)
}

// ClarifySpec is the pre-execution question round the model emits for
// KindClarify. Plan is optional prose shown above the questions — the proposed
// approach the user is being asked to confirm or redirect.
type ClarifySpec struct {
	Plan      string            `json:"plan,omitempty"`
	Questions []ClarifyQuestion `json:"questions"`
}

// ClarifyQuestion is one bounded choice. The option set is closed on purpose:
// a free-text question is the model asking the user to do its job, while a
// short option list is a decision the user can make in one keystroke. Other
// free text stays available through the surface's "Other" affordance.
type ClarifyQuestion struct {
	ID          string          `json:"id"`
	Header      string          `json:"header"`
	Question    string          `json:"question"`
	Options     []ClarifyOption `json:"options"`
	MultiSelect bool            `json:"multi_select,omitempty"`
	// Recommended names the option label the model would pick if the user has
	// no preference or cannot answer interactively. It must match an option.
	Recommended string `json:"recommended,omitempty"`
}

// ClarifyOption is one selectable answer. Preview optionally renders a short
// markdown sample of what choosing this option produces (a layout sketch, a
// code shape); it is single-select only, because a multi-select preview has no
// single result to show.
type ClarifyOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Preview     string `json:"preview,omitempty"`
}

// ClarifyAnswer is one question's answer: the selected option labels, in the
// order the user picked them, plus the free-text value when the user chose
// "Other". Both may be present for a multi-select question.
type ClarifyAnswer struct {
	ID       string   `json:"id"`
	Selected []string `json:"selected,omitempty"`
	Other    string   `json:"other,omitempty"`
}

// ClarifyAnswerText renders an answer set as the text a resumed agent receives.
// It is the one place the CLI, the web console and the TUI agree on the shape,
// so the model reads the same reply whichever surface asked: one line per
// question, unanswered questions explicitly deferred to the recommendation.
func ClarifyAnswerText(spec *ClarifySpec, answers []ClarifyAnswer) string {
	if spec == nil {
		return ""
	}
	byID := make(map[string]ClarifyAnswer, len(answers))
	for _, a := range answers {
		byID[a.ID] = a
	}
	var lines []string
	for _, q := range spec.Questions {
		var picks []string
		if a, ok := byID[q.ID]; ok {
			for _, s := range a.Selected {
				if s = strings.TrimSpace(s); s != "" {
					picks = append(picks, s)
				}
			}
			if other := strings.TrimSpace(a.Other); other != "" {
				picks = append(picks, other)
			}
		}
		if len(picks) == 0 {
			if q.Recommended != "" {
				picks = []string{q.Recommended + " (recommended fallback — the user did not choose)"}
			} else {
				lines = append(lines, "- "+q.Question+": (not answered)")
				continue
			}
		}
		lines = append(lines, "- "+q.Question+": "+strings.Join(picks, ", "))
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// ToolCalls returns every call the model emitted this round. Models emit
// several tool_use blocks per response for batch operations ("cancel these 5
// tasks"), and the loop must honour all of them in one round: executing only
// the first burns one round per call until the round budget runs out on work
// that was fully specified up front.
func (o Output) ToolCalls() []*ToolCall {
	if len(o.Tools) > 0 {
		return o.Tools
	}
	if o.Tool != nil {
		return []*ToolCall{o.Tool}
	}
	return nil
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
	// ToolsPolicy is the per-task tool-face override. Empty means "use the
	// global routing.tools_policy"; "minimal" keeps the safe whitelist;
	// "extended" lifts it so the agent's native skills, MCP and sub-agent
	// tools are reachable. The entry model sets this based on the task's
	// complexity and risk: high-complexity tasks that need the agent's
	// full capability set can request extended without operator intervention
	// at the routing layer. The engine copies it into Spec.ToolsPolicy so it
	// rides spec_json to the executor; this outer field only exists for the
	// model's emit shape.
	ToolsPolicy string `json:"tools_policy,omitempty"`
	// MaxTurns is the per-task agent turn cap the entry model may request for
	// a task whose complexity needs more (or fewer) rounds than the adapter
	// default. Copied into Spec.MaxTurns for the same reason as ToolsPolicy.
	MaxTurns int `json:"max_turns,omitempty"`
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
	// ToolsPolicy / MaxTurns live here (not on the outer TaskSpec) because
	// spec_json is what persists on the task row and crosses the wire in
	// TaskDelegatePayload — an outer-field-only override would evaporate at
	// toTaskInput's marshal and never reach the executor.
	ToolsPolicy string `json:"tools_policy,omitempty"`
	MaxTurns    int    `json:"max_turns,omitempty"`
	// AllowAgents narrows the agent pool for this task to the named agents —
	// the thread's team pick, filled by the ask engine from the composer's
	// roster. It lives in spec for the same reason as ToolsPolicy: spec_json
	// is what persists on the row and what a delegated task carries to the
	// executor, so the restriction survives a hop to another node instead of
	// being a submitter-side suggestion. Empty means no restriction.
	AllowAgents []string `json:"allow_agents,omitempty"`
	// PermissionMode is the conversation's permission policy that created
	// this task (defense.ModeManual, ModePlan, …). Empty means auto: the
	// thread never picked one, and the executor's ordinary tier gate applies.
	PermissionMode string `json:"permission_mode,omitempty"`
	// ActionSpec is the hardware/software actuator dispatch (§7.2): when the
	// request drives a declared actuator (a hardware:* ability on a device),
	// the model names the target actuator, the action verb and the scalar
	// parameters the actuator's driver placeholders need. It lives inside
	// spec for the same reason as ToolsPolicy — spec_json is what reaches the
	// executor, where SubstituteActionSpec fills {action}/{param:<name>}.
	ActionSpec *ledger.ActionSpec `json:"action_spec,omitempty"`
	// Acceptance is the machine-checkable contract: commands and artifact
	// assertions the orchestrator runs itself after the agent claims done.
	// It lives here because spec_json persists on the row and crosses the
	// wire, so a delegated task is verified by the same criteria on the
	// executor. Empty means no contract: the task cannot be "verified".
	Acceptance []contract.Check `json:"acceptance,omitempty"`
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
// it declares here. Node is the hard pin ("在 Mac 上做"): a stage carrying it may
// only run on that node — the requires match alone is a coincidence across a
// fleet where every machine advertises the same agent.
type PlanStageSpec struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	Intent    string          `json:"intent"`
	Requires  []string        `json:"requires"`
	Needs     []string        `json:"needs"`
	Node      string          `json:"node,omitempty"`
	Resources ResourceProfile `json:"resource_profile"`
}

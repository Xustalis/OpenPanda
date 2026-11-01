// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Xustalis/OpenPanda/internal/contract"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

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
	if err := validateActionSpec(t.Spec.ActionSpec); err != nil {
		return err
	}
	if len(t.Spec.Acceptance) > 0 {
		checks, err := contract.Validate(t.Spec.Acceptance)
		if err != nil {
			return fmt.Errorf("entry: %w", err)
		}
		t.Spec.Acceptance = checks
	}
	return nil
}

// maxActionParams bounds an action_spec's parameter map. Driver placeholders
// are few ({param:angle}, {param:seconds}); a map of hundreds is a confused
// or hostile spec, and every key is substituted at execution anyway.
const maxActionParams = 32

// validateActionSpec checks the actuator dispatch block a model may attach to
// a task (§7.2). The executor re-checks everything at substitution, but the
// boundary validation exists for the same reason the rest of
// ValidateTaskSpec does: a malformed spec must be refused while the model can
// still fix it, not after it has been persisted and routed to a device.
func validateActionSpec(s *ledger.ActionSpec) error {
	if s == nil {
		return nil
	}
	if s.TargetActuator == "" {
		return fmt.Errorf("entry: spec.action_spec.target_actuator is required")
	}
	if s.Action == "" {
		return fmt.Errorf("entry: spec.action_spec.action is required")
	}
	if len(s.Parameters) > maxActionParams {
		return fmt.Errorf("entry: spec.action_spec.parameters has %d entries, limit is %d",
			len(s.Parameters), maxActionParams)
	}
	for k, v := range s.Parameters {
		if k == "" || len(k) > 64 || strings.ContainsAny(k, "{} \t") {
			return fmt.Errorf("entry: spec.action_spec parameter name %q invalid", k)
		}
		switch v.(type) {
		case nil, string, float64, bool:
		default:
			return fmt.Errorf("entry: spec.action_spec parameter %q must be a scalar value", k)
		}
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

// Clarify limits. The shape mirrors the interaction a person can actually
// answer in one sitting: up to four questions, each with two to four concrete
// options. A longer list is a menu, not a decision, and an open-ended question
// belongs in the answer stream, not in a choice card.
const (
	maxClarifyQuestions     = 4
	minClarifyOptions       = 2
	maxClarifyOptions       = 4
	maxClarifyHeaderRunes   = 12
	maxClarifyQuestionRunes = 500
	maxClarifyLabelRunes    = 60
	maxClarifyDescRunes     = 200
	// maxClarifyPreviewBytes bounds one option's preview: it is prompt space
	// and UI space, not a document attachment.
	maxClarifyPreviewBytes = 4000
	maxClarifyPlanBytes    = 4000
)

// ValidateClarifySpec checks the shape of a model-emitted question round.
// The failures it catches are the ones a model actually makes: a question with
// no options (nothing to select), duplicate labels (the answer becomes
// ambiguous), and a "recommended" that names no option (the non-interactive
// fallback would have no default).
func ValidateClarifySpec(c *ClarifySpec) error {
	if c == nil {
		return fmt.Errorf("entry: clarify is nil")
	}
	if len(c.Plan) > maxClarifyPlanBytes {
		return fmt.Errorf("entry: clarify.plan is too long (%d bytes, limit %d)", len(c.Plan), maxClarifyPlanBytes)
	}
	if len(c.Questions) == 0 {
		return fmt.Errorf("entry: clarify.questions is required")
	}
	if len(c.Questions) > maxClarifyQuestions {
		return fmt.Errorf("entry: clarify has %d questions, limit is %d", len(c.Questions), maxClarifyQuestions)
	}
	seen := make(map[string]bool, len(c.Questions))
	for i, q := range c.Questions {
		if strings.TrimSpace(q.ID) == "" {
			return fmt.Errorf("entry: clarify.questions[%d].id is required", i)
		}
		if seen[q.ID] {
			return fmt.Errorf("entry: clarify has two questions with id %q", q.ID)
		}
		seen[q.ID] = true
		header := strings.TrimSpace(q.Header)
		if header == "" {
			return fmt.Errorf("entry: clarify.questions[%d] (%s) has no header", i, q.ID)
		}
		if utf8.RuneCountInString(header) > maxClarifyHeaderRunes {
			return fmt.Errorf("entry: clarify.questions[%d] header is longer than %d characters", i, maxClarifyHeaderRunes)
		}
		question := strings.TrimSpace(q.Question)
		if question == "" {
			return fmt.Errorf("entry: clarify.questions[%d] (%s) has no question text", i, q.ID)
		}
		if utf8.RuneCountInString(question) > maxClarifyQuestionRunes {
			return fmt.Errorf("entry: clarify.questions[%d] question is longer than %d characters", i, maxClarifyQuestionRunes)
		}
		if len(q.Options) < minClarifyOptions || len(q.Options) > maxClarifyOptions {
			return fmt.Errorf("entry: clarify.questions[%d] has %d options, want %d-%d",
				i, len(q.Options), minClarifyOptions, maxClarifyOptions)
		}
		labels := make(map[string]bool, len(q.Options))
		for j, o := range q.Options {
			label := strings.TrimSpace(o.Label)
			if label == "" {
				return fmt.Errorf("entry: clarify.questions[%d].options[%d] has no label", i, j)
			}
			if utf8.RuneCountInString(label) > maxClarifyLabelRunes {
				return fmt.Errorf("entry: clarify.questions[%d].options[%d] label is longer than %d characters", i, j, maxClarifyLabelRunes)
			}
			if labels[label] {
				return fmt.Errorf("entry: clarify.questions[%d] has two options labelled %q", i, label)
			}
			labels[label] = true
			if utf8.RuneCountInString(o.Description) > maxClarifyDescRunes {
				return fmt.Errorf("entry: clarify.questions[%d].options[%d] description is longer than %d characters", i, j, maxClarifyDescRunes)
			}
			if len(o.Preview) > maxClarifyPreviewBytes {
				return fmt.Errorf("entry: clarify.questions[%d].options[%d] preview is too long (%d bytes, limit %d)", i, j, len(o.Preview), maxClarifyPreviewBytes)
			}
			if q.MultiSelect && strings.TrimSpace(o.Preview) != "" {
				return fmt.Errorf("entry: clarify.questions[%d] is multi-select and cannot carry option previews", i)
			}
		}
		if rec := strings.TrimSpace(q.Recommended); rec != "" && !labels[rec] {
			return fmt.Errorf("entry: clarify.questions[%d].recommended %q names no option", i, rec)
		}
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

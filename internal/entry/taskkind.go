// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

// TaskKind describes the requested outcome, independently of context_type
// (transport) and permission_mode (authorization). Empty supports legacy specs.
type TaskKind string

const (
	TaskCodeQuestion  TaskKind = "code_question"
	TaskInvestigation TaskKind = "investigation"
	TaskEdit          TaskKind = "edit"
	TaskOperations    TaskKind = "operations"
)

func (k TaskKind) Valid() bool {
	switch k {
	case "", TaskCodeQuestion, TaskInvestigation, TaskEdit, TaskOperations:
		return true
	}
	return false
}

// EvidencePolicy is shared by the executor and completion judge. It sets
// checkable requirements without prescribing a tool count or an exact trace.
func (k TaskKind) EvidencePolicy() string {
	switch k {
	case TaskCodeQuestion, TaskInvestigation:
		return "Read the actual implementation and relevant callers/tests for core claims; cite file locations and explain how they support each conclusion. Check counterexamples before asserting absence. A bounded or truncated search proves only what was searched, not repository-wide absence. Distinguish observed facts, inferences and unknowns; state covered and unexamined paths. Do not claim runtime verification without actual results. A static investigation does not require builds, tests or edits merely to look complete."
	case TaskEdit:
		return "Deliver the requested behavior within scope; report the actual changes and relevant verification results. Distinguish inherited failures from regressions. Unexecuted checks are unverified, not passing; do not weaken tests to claim completion."
	case TaskOperations:
		return "Observe and report the requested postcondition within the authorized scope; command invocation or exit code alone is not proof that the target state was reached. Disclose blocked or unverified conditions."
	}
	return ""
}

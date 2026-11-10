// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
)

// RecoveryAssessment deliberately does not infer replay safety from exit
// codes or argv. An agent can have changed a workspace before failing. A
// checkpoint is only meaningful on its owning node/adapter; a remote replay
// also needs workspace transfer, pins and authorization to be revalidated.
// Until such a handoff contract exists, automatic retries stay on this node.
type RecoveryAssessment struct {
	Category        string `json:"category"`
	Strategy        string `json:"strategy"`
	CrossNodeReplay bool   `json:"cross_node_replay"`
	ReplaySafety    string `json:"replay_safety"`
}

func assessRecovery(r bus.TaskResultPayload) RecoveryAssessment {
	a := RecoveryAssessment{Category: "execution_failure", Strategy: "bounded_same_node_retry", ReplaySafety: "unknown_side_effects"}
	switch {
	case r.NoRetry:
		a.Category = "non_retryable"
		a.Strategy = "stop"
	case commander.AdapterUnavailable(r.Stderr):
		// The node cannot spawn the agent at all: the adapter script or its
		// interpreter is missing. Nothing about the work was attempted, so
		// there is no judgement to make and no retry that could differ — the
		// fix is an install, and the row stays failed until one happens.
		a.Category = "adapter_unavailable"
		a.Strategy = "stop"
		a.ReplaySafety = "safe_after_environment_fix"
	case commander.IsAuthorizationRefusal(r.Stderr):
		a.Category = "authorization"
		a.Strategy = "human_review"
	case r.ExitCode == 124 && r.AgentSessionID != "":
		a.Category = "checkpoint"
		a.Strategy = "same_node_session_resume"
		a.ReplaySafety = "node_adapter_bound_checkpoint"
	case r.ExitCode == 124:
		a.Category = "timeout_without_checkpoint"
	}
	return a
}

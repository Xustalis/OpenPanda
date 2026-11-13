// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"encoding/json"
	"strings"

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

// The strategy vocabulary, shared with every reader of the assessment event:
// `panda task add --wait` decides from these words whether the failed row it is
// watching will move again, so a literal typed in two places would eventually
// mean two different things.
const (
	RecoveryStop          = "stop"
	RecoveryHumanReview   = "human_review"
	RecoveryBoundedRetry  = "bounded_same_node_retry"
	RecoverySessionResume = "same_node_session_resume"
)

func assessRecovery(r bus.TaskResultPayload) RecoveryAssessment {
	a := RecoveryAssessment{Category: "execution_failure", Strategy: RecoveryBoundedRetry, ReplaySafety: "unknown_side_effects"}
	switch {
	case r.NoRetry:
		a.Category = "non_retryable"
		a.Strategy = RecoveryStop
	case commander.AdapterUnavailable(r.Stderr):
		// The node cannot spawn the agent at all: the adapter script or its
		// interpreter is missing. Nothing about the work was attempted, so
		// there is no judgement to make and no retry that could differ — the
		// fix is an install, and the row stays failed until one happens.
		a.Category = "adapter_unavailable"
		a.Strategy = RecoveryStop
		a.ReplaySafety = "safe_after_environment_fix"
	case commander.IsAuthorizationRefusal(r.Stderr):
		a.Category = "authorization"
		a.Strategy = RecoveryHumanReview
	case r.Tokens == 0 && authenticationSetupRequired(r.Stderr):
		// Commander already tried permitted provider rescue and alternates.
		// Re-dispatching a logged-out CLI cannot change its credentials and
		// merely hits the dispatch-failure cooldown. Preserve the first cause.
		a.Category = "authentication_required"
		a.Strategy = RecoveryStop
		a.ReplaySafety = "safe_after_environment_fix"
	case providerBillingRequired(r.Stderr):
		a.Category = "provider_billing_required"
		a.Strategy = RecoveryStop
		a.ReplaySafety = "safe_after_environment_fix"
	case r.ExitCode == 124 && r.AgentSessionID != "":
		a.Category = "checkpoint"
		a.Strategy = RecoverySessionResume
		a.ReplaySafety = "node_adapter_bound_checkpoint"
	case r.ExitCode == 124:
		a.Category = "timeout_without_checkpoint"
	}
	return a
}

// Commander has already attempted allowed provider rescue. Replaying an
// exhausted account cannot replenish it and blocks other queued tasks.
func providerBillingRequired(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) > 2048 || !strings.HasPrefix(s, "provider failure:") {
		return false
	}
	for _, phrase := range []string{"insufficient balance", "insufficient account funds", "insufficient_quota", "credit balance", "out of credits", "payment required"} {
		if strings.Contains(s, phrase) {
			return true
		}
	}
	return false
}

// Match a short CLI diagnostic, never a quoted phrase buried in a task report.
func authenticationSetupRequired(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) > 512 {
		return false
	}
	for _, prefix := range []string{"not logged in", "not signed in", "please run /login", "login required", "invalid api key", "invalid_api_key", "missing api key"} {
		if s == prefix || strings.HasPrefix(s, prefix+" ") || strings.HasPrefix(s, prefix+":") {
			return true
		}
	}
	return false
}

// RecoveryPending distinguishes a transient failed row from a settled failure.
// Readers must wait through the result->assessment write gap and retry backoff.
func RecoveryPending(events []Event) bool {
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].Type {
		case EvReview, EvExpire, EvCancel:
			return false
		case EvRecoveryAssessment:
			var p struct{ Assessment RecoveryAssessment }
			if json.Unmarshal([]byte(events[i].DataJSON), &p) == nil {
				switch p.Assessment.Strategy {
				case RecoveryBoundedRetry, RecoverySessionResume:
					return true
				case RecoveryStop, RecoveryHumanReview:
					return false
				}
			}
		case EvResult:
			start := 0
			for j := i - 1; j >= 0; j-- {
				if events[j].Type == EvSubmit || events[j].Type == EvRetry {
					start = j + 1
					break
				}
			}
			for j := start; j < i; j++ {
				if events[j].Type == EvExecAgentStart || events[j].Type == EvSupervisionRound {
					return true
				}
			}
			return false
		}
	}
	return false
}

package core

import (
	"context"
	"log/slog"
)

// Trace event types for decision-orbit visibility. These are written to
// task_events alongside the existing lifecycle events; the panel's SSE feed
// forwards them in real time to the orbit component, and GET /api/tasks/{id}
// aggregates them for a replay when a client opens the detail mid-run.
//
// Best-effort only: a write failure is logged and otherwise ignored. The
// scheduling/delegation/supervision machines must never stall on visibility.
const (
	EvClassifyResult   = "classify_result"    // entry.Classify success → kind/note/stages
	EvRouteDecision    = "route_decision"     // scheduler.Route → action/target/reason/score_breakdown/candidates
	EvDelegationHop    = "delegation_hop"     // handleTaskAccept/Result → from/to/via/chain/attempt_id
	EvExecAgentStart   = "exec_agent_start"   // router.Execute before agent run → agent/adapter/injected
	EvJudgeStart       = "judge_start"        // supervision loop right before the judge call → round/budget; the CLI's reviewing stage starts here
	EvSupervisionRound = "supervision_round"  // supervise loop each round → round/budget/verdict/judge_summary
	EvTier2Triggered   = "tier2_triggered"    // defense.Authorize tier≥2 result → operations/parked_in_review
	EvPlanStageChanged = "plan_stage_changed" // plan stage unlock/start/complete → plan_id/stage_id/transition
	EvArtifactTransfer = "artifact_transfer"  // artifact fetch → from/to/hash/size/ok/elapsed
	EvAgentUsage       = "agent_usage"        // adapter's structured token breakdown → agent/input/output/cache_*
	EvContextOverflow  = "context_overflow"   // agent failed on its context window → parked in review, retry cannot help
	EvSubagentEvent    = "subagent_event"     // agent spawned a sub-agent (Claude Task tool) → note
)

// EvTrace records one trace event. Errors are downgraded to a warning log: a
// visibility layer outage must never mask the actual execution path.
func (c *Core) EvTrace(ctx context.Context, taskID, typ string, data map[string]any) {
	if c == nil || c.store == nil || taskID == "" || typ == "" {
		return
	}
	if err := c.store.RecordEvent(ctx, taskID, typ, data); err != nil {
		if c.logger != nil {
			c.logger.Warn("trace event dropped",
				"task", taskID, "type", typ, "err", err,
			)
		} else {
			slog.Warn("trace event dropped",
				"task", taskID, "type", typ, "err", err,
			)
		}
	}
}

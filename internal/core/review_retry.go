// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
	"github.com/Xustalis/OpenPanda/internal/security"
)

// ReviewFailure describes a parked failure separately from accepting completed
// work. Legacy failed executions use needs_changed_input even for temporary
// outages; an explicit retry must neither accept that work nor grant consent.
type ReviewFailure struct {
	Reason         string
	Cause          string
	RetryAvailable bool
}

// ReviewFailureFromEvents is shared by the store and both panel read paths.
// The execution-failure marker can be written atomically with the review or
// reconstructed from the preceding legacy result for the same failure.
// A policy park cannot become retryable because its text mentions an API error.
func ReviewFailureFromEvents(disposition ApprovalDisposition, reviewRaw, outcomeType, outcomeRaw string) ReviewFailure {
	var review struct {
		Reason          string `json:"reason"`
		ExecutionFailed bool   `json:"execution_failed"`
	}
	var outcome struct {
		Failed string `json:"failed"`
	}
	_ = json.Unmarshal([]byte(reviewRaw), &review)
	_ = json.Unmarshal([]byte(outcomeRaw), &outcome)
	reason := strings.TrimSpace(review.Reason)
	return ReviewFailure{
		Reason: security.Redact(reason),
		Cause:  ReviewFailureCause(reason),
		RetryAvailable: disposition == ApprovalNeedsChangedInput && reason != "" &&
			(review.ExecutionFailed || (outcomeType == EvResult && strings.TrimSpace(outcome.Failed) == reason)),
	}
}

// ReviewFailureCause labels short execution diagnostics, not phrases quoted in
// generated reports. The original (redacted) reason is always shown as evidence.
func ReviewFailureCause(reason string) string {
	if commander.IsModelConfigFailure(reason) {
		return "model_config"
	}
	s := strings.ToLower(strings.TrimSpace(reason))
	if len(s) > 4096 {
		return ""
	}
	if !strings.HasPrefix(s, "api_retry error ") && !strings.HasPrefix(s, "api error:") &&
		!strings.HasPrefix(s, "provider failure:") && !strings.HasPrefix(s, "api_transport error") {
		return ""
	}
	switch {
	case strings.Contains(s, "429") || strings.Contains(s, "rate_limited") || strings.Contains(s, "rate limit"):
		return "rate_limited"
	case strings.Contains(s, "rejected for security reasons") || strings.Contains(s, "content_filter"):
		return "input_rejected"
	default:
		return "provider_error"
	}
}

type reviewQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readReviewFailure(ctx context.Context, q reviewQueryer, task Task) (ReviewFailure, error) {
	var reviewRaw, outcomeType, outcomeRaw string
	err := q.QueryRowContext(ctx, `SELECT
		COALESCE((SELECT data_json FROM task_events WHERE task_id=? AND type=? ORDER BY id DESC LIMIT 1), ''),
		COALESCE((SELECT type FROM task_events WHERE task_id=? AND type IN (?,?,?) ORDER BY id DESC LIMIT 1), ''),
		COALESCE((SELECT data_json FROM task_events WHERE task_id=? AND type IN (?,?,?) ORDER BY id DESC LIMIT 1), '')`,
		task.TaskID, EvReview, task.TaskID, EvResult, EvRetry, EvSubmit, task.TaskID, EvResult, EvRetry, EvSubmit).Scan(&reviewRaw, &outcomeType, &outcomeRaw)
	if err != nil {
		return ReviewFailure{}, err
	}
	return ReviewFailureFromEvents(task.ApprovalDisposition, reviewRaw, outcomeType, outcomeRaw), nil
}

func (s *TaskStore) ReviewFailure(ctx context.Context, taskID string) (ReviewFailure, error) {
	task, err := s.Get(ctx, taskID)
	if err != nil {
		return ReviewFailure{}, err
	}
	if task.State != StateReview {
		return ReviewFailure{}, nil
	}
	return readReviewFailure(ctx, s.db, task)
}

// claimReviewRetry rotates the failed attempt and claims its execution in the
// same transaction. The permission flags and the original task payload stay
// intact. The version fence also prevents re-playing a newly changed review.
func (s *TaskStore) claimReviewRetry(ctx context.Context, task Task) error {
	attempt, err := newUUID()
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		info, err := readReviewFailure(ctx, tx, task)
		if err != nil {
			return err
		}
		if !info.RetryAvailable {
			return fmt.Errorf("%w: task is not a reviewed execution failure", ErrConflict)
		}
		res, err := tx.ExecContext(ctx, `UPDATE tasks SET state=?,attempt_id=?,scheduled=0,
			result_json=NULL,approval_disposition=NULL,verification='',lease_expires_at=NULL,
			state_version=state_version+1,updated_at=?
			WHERE task_id=? AND state=? AND owner_node=? AND attempt_id=? AND state_version=?`,
			StateDispatched, attempt, s.now(), task.TaskID, StateReview, task.OwnerNode, task.AttemptID, task.StateVersion)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("%w: retry was claimed concurrently", ErrConflict)
		}
		if err := s.recordEventTx(ctx, tx, task.TaskID, EvRetry, map[string]any{"manual": true}); err != nil {
			return err
		}
		return s.recordEventTx(ctx, tx, task.TaskID, EvDelegate, map[string]any{"target": task.OwnerNode})
	})
}

// RetryReviewed runs a failed task again after an explicit operator action.
// Like ContinueReviewed this belongs to the local executor: retrying a peer's
// workspace on the origin would silently change its environment.
func (c *Core) RetryReviewed(ctx context.Context, taskID string) (Task, bus.TaskResultPayload, error) {
	cur, err := c.store.Get(ctx, taskID)
	if err != nil {
		return Task{}, bus.TaskResultPayload{}, err
	}
	if cur.State != StateReview {
		return cur, bus.TaskResultPayload{}, fmt.Errorf("%w: task is not awaiting review", ErrConflict)
	}
	if target, err := c.store.DispatchTarget(ctx, taskID); err != nil {
		return cur, bus.TaskResultPayload{}, err
	} else if target != "" && !scheduler.SameRuntimeIdentity(target, c.nodeID) {
		return cur, bus.TaskResultPayload{}, fmt.Errorf("%w: retry this task on executor %s", ErrConflict, target)
	}
	if !scheduler.SameRuntimeIdentity(cur.OwnerNode, c.nodeID) {
		return cur, bus.TaskResultPayload{}, fmt.Errorf("%w: retry belongs to task owner %s", ErrConflict, cur.OwnerNode)
	}
	router := c.currentRouter()
	if router == nil {
		return cur, bus.TaskResultPayload{}, fmt.Errorf("retry requires a configured executor")
	}
	if err := c.store.claimReviewRetry(ctx, cur); err != nil {
		return cur, bus.TaskResultPayload{}, err
	}
	c.reviewReset(taskID)
	// An explicit retry may probe the same executor again while its automatic
	// routing cooldown is still active. This clears only that agent's health
	// hint; capability checks, sandboxing and authorization still run normally.
	var raw string
	if err := c.store.db.QueryRowContext(ctx, `SELECT data_json FROM task_events WHERE task_id=? AND type=? ORDER BY id DESC LIMIT 1`, taskID, EvExecAgentStart).Scan(&raw); err == nil {
		var start struct {
			Agent   string `json:"agent"`
			Attempt string `json:"attempt_id"`
		}
		if json.Unmarshal([]byte(raw), &start) == nil && start.Attempt == cur.AttemptID {
			router.RetryAgent(start.Agent)
			if start.Agent != "" {
				c.breaker.Reset("agent:" + start.Agent)
			}
		}
	}
	result, runErr := c.run(ctx, taskID, cur.Intent, cur.Requires, nil)
	if ctx.Err() != nil {
		cleanup := context.WithoutCancel(ctx)
		_, _ = c.CancelTree(cleanup, taskID)
		final, _ := c.store.Get(cleanup, taskID)
		return final, result, ctx.Err()
	}
	final, result, err := c.retryLoop(ctx, taskID, cur.Intent, cur.Requires, result, runErr)
	if err == nil {
		c.advanceStagePlan(ctx, final)
		c.relayResultForRemote(ctx, final)
	}
	return final, result, err
}

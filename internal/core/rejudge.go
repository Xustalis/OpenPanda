// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/objectives"
)

// ObjectiveEvidenceError reports why accepting a review row's stored work would
// fail the MAX-objective gate, or nil when nothing blocks it. The assessments
// the gate demands come from the reviewing model, never from the executor, so
// a row parked without them can never be approved as-is. Callers use this to
// explain the refusal instead of a bare "approve failed", and to offer the
// re-review that can actually produce the missing evidence.
func ObjectiveEvidenceError(t Task) error {
	var result bus.TaskResultPayload
	if strings.TrimSpace(t.ResultJSON) != "" {
		// A result that does not decode is the gate's own input problem: report
		// it the same way the accept path does rather than pretending the row
		// has no objectives.
		if err := json.Unmarshal([]byte(t.ResultJSON), &result); err != nil {
			return fmt.Errorf("objectives: decode stored result: %w", err)
		}
	}
	return objectiveResultEvidence(t, result)
}

// RejudgeReviewed asks the reviewing model again about work already stored on a
// review-parked task. It exists for the one park a human cannot resolve by
// approving: the supervisor answered without a usable verdict (unparsable
// output, an unreachable judge), so the row carries no objective assessments,
// and the accept path refuses it forever. Re-judging re-reads the stored result
// instead of re-running the agent — the work is already on disk; only the
// verdict is missing.
//
// It never accepts work the fresh verdict does not accept: a "continue" or
// "blocked" verdict leaves the task in review with the new reason, and only a
// "done" verdict goes on to the normal accept path (which re-checks the gate
// against the assessments this call just recorded).
func (c *Core) RejudgeReviewed(ctx context.Context, taskID string) (Task, bus.TaskResultPayload, error) {
	cur, err := c.store.Get(ctx, taskID)
	if err != nil {
		return Task{}, bus.TaskResultPayload{}, err
	}
	if cur.State != StateReview {
		return cur, bus.TaskResultPayload{}, fmt.Errorf("%w: task is not awaiting review", ErrConflict)
	}
	disposition, err := c.store.ApprovalDisposition(ctx, taskID)
	if err != nil {
		return cur, bus.TaskResultPayload{}, err
	}
	if disposition != ApprovalAcceptWork {
		// A question park or a consent park is waiting on the human, not on the
		// judge: re-judging would answer a different question.
		return cur, bus.TaskResultPayload{}, fmt.Errorf("%w: only a reviewed result can be re-judged", ErrConflict)
	}
	policy, perr := objectives.FromSpecJSON(cur.SpecJSON)
	if perr != nil {
		return cur, bus.TaskResultPayload{}, fmt.Errorf("%w: task objectives are unreadable: %w", ErrConflict, perr)
	}
	if !objectives.HasMax(policy) {
		return cur, bus.TaskResultPayload{}, fmt.Errorf("%w: task carries no objective contract to re-judge", ErrConflict)
	}
	_, _, supervisor := c.executionSnapshot()
	if supervisor == nil {
		return cur, bus.TaskResultPayload{}, fmt.Errorf("%w: re-review requires a configured supervisor model", ErrConflict)
	}
	var stored bus.TaskResultPayload
	if strings.TrimSpace(cur.ResultJSON) != "" {
		if err := json.Unmarshal([]byte(cur.ResultJSON), &stored); err != nil {
			return cur, bus.TaskResultPayload{}, fmt.Errorf("%w: decode stored result: %w", ErrConflict, err)
		}
	}

	c.EvTrace(ctx, taskID, EvJudgeStart, map[string]any{"round": 1, "budget": 1, "rejudge": true})
	judgeStart := time.Now()
	options := c.supervisionOptions(ctx, taskID, supervisor)
	options.BypassCache = true
	v, serr := entry.SuperviseObjectives(ctx, supervisor, cur.Intent+objectiveTaskContext(cur), rejudgePayload(cur, stored), maxCriteria(policy), options)
	if serr != nil {
		// Supervise already turned the outage into a review verdict; the error
		// is for the log, not the operator.
		c.logger.Warn("re-review supervise call failed", "task", taskID, "err", serr)
	}
	verdict := objectiveVerdict(cur, v)
	if err := c.store.UpdateReviewEvidence(ctx, taskID, cur.AttemptID,
		verdict.ObjectiveAssessments, verdict.Status, verdict.Reason, verdict.Followup); err != nil {
		return cur, bus.TaskResultPayload{}, err
	}
	if rerr := c.store.RecordEvent(context.WithoutCancel(ctx), taskID, EvSupervise, map[string]any{
		"round": 1, "status": verdict.Status, "reason": verdict.Reason, "followup": verdict.Followup,
		"objective_assessments": verdict.ObjectiveAssessments, "rejudge": true,
		"duration_ms": time.Since(judgeStart).Milliseconds(),
	}); rerr != nil {
		c.logger.Warn("record re-review event", "task", taskID, "err", rerr)
	}
	if verdict.Status != entry.VerdictDone {
		reason := verdict.Reason
		if strings.TrimSpace(reason) == "" {
			reason = "the reviewing model did not accept the stored result"
		}
		return cur, bus.TaskResultPayload{
			TaskID: cur.TaskID, AttemptID: cur.AttemptID, State: StateReview,
			OK: false, Verdict: verdict.Status, VerdictReason: reason, Followup: verdict.Followup,
			ObjectiveAssessments: verdict.ObjectiveAssessments,
		}, fmt.Errorf("%w: %s", ErrConflict, reason)
	}
	// The fresh verdict accepted the work: run the ordinary accept path, which
	// re-checks the gate (now satisfied), mirrors the decision to a peer copy
	// and advances the stage plan.
	return c.resumeApproved(ctx, taskID, true)
}

// UpdateReviewEvidence rewrites a review row's stored result with a fresh
// verdict and its objective assessments. Guarded by state and attempt so a
// concurrent approve, reject or retry cannot be overwritten by a stale judge
// answer: the row must still be the same attempt, still parked in review.
func (s *TaskStore) UpdateReviewEvidence(ctx context.Context, taskID, attemptID string, assessments []objectives.Assessment, status, reason, followup string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var raw string
		err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(result_json,'') FROM tasks WHERE task_id=? AND state=? AND attempt_id=?`,
			taskID, StateReview, attemptID).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: task %s is no longer parked in review", ErrConflict, taskID)
		}
		if err != nil {
			return fmt.Errorf("read review result: %w", err)
		}
		payload := map[string]any{}
		if strings.TrimSpace(raw) != "" {
			if err := json.Unmarshal([]byte(raw), &payload); err != nil {
				// A result the accept path would also refuse to decode: keep it
				// untouched rather than replacing the operator's only evidence.
				return fmt.Errorf("%w: stored result is unreadable: %w", ErrConflict, err)
			}
		}
		payload["verdict"] = status
		payload["verdict_reason"] = reason
		payload["followup"] = followup
		payload["objective_assessments"] = assessments
		body, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode review result: %w", err)
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE tasks SET result_json=?, state_version=state_version+1, updated_at=?
			 WHERE task_id=? AND state=? AND attempt_id=?`,
			string(body), s.now(), taskID, StateReview, attemptID)
		if err != nil {
			return fmt.Errorf("update review evidence: %w", err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("%w: review evidence changed concurrently", ErrConflict)
		}
		return nil
	})
}

// maxCriteria lists every MAX criterion as "dimension:index = text", the shape
// the reviewing model must answer one assessment per line for.
func maxCriteria(p *objectives.Policy) []string {
	if p == nil {
		return nil
	}
	var out []string
	for _, d := range p.Dimensions {
		if d.Priority != objectives.Max {
			continue
		}
		for i, c := range d.Criteria {
			out = append(out, fmt.Sprintf("%s:%d = %s", d.Dimension, i, c))
		}
	}
	return out
}

// rejudgePayload rebuilds the reviewer's input from the stored result. It is
// the round loop's payload minus the live-only blocks (this round's test
// evidence and pre-run baseline were never persisted), and it says so — a
// judge told "no command evidence" would otherwise fail work that was verified
// at the time.
func rejudgePayload(t Task, stored bus.TaskResultPayload) string {
	var b strings.Builder
	b.WriteString(stored.Stdout)
	if strings.TrimSpace(stored.Stderr) != "" {
		b.WriteString("\n\n错误输出（stderr）：\n")
		b.WriteString(stored.Stderr)
	}
	b.WriteString("\n\n[执行退出码] ")
	b.WriteString(strconv.Itoa(stored.ExitCode))
	if scope := taskScope(t.SpecJSON); scope != "" {
		b.WriteString("\n[任务范围] ")
		b.WriteString(scope)
	}
	if len(stored.FilesChanged) > 0 {
		shown := stored.FilesChanged
		if len(shown) > 50 {
			shown = shown[:50]
		}
		b.WriteString("\n\n[本轮实际变更的文件] ")
		b.WriteString(strings.Join(shown, ", "))
	}
	b.WriteString("\n\n[重新评审] 以上是上一轮已保存的执行结果；本轮没有重新执行，因此不附带新的测试/基线证据块。请依据上述结果与任务要求逐项核验。")
	return b.String()
}

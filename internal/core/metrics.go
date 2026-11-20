// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// DelegationMetric is one row of the delegation_metrics table.
type DelegationMetric struct {
	ID            int64
	TaskID        string
	Delegator     string
	Executor      string
	AbilitiesJSON string
	Success       bool
	LatencyMs     int64
	Tokens        sql.NullInt64
	Cost          sql.NullFloat64
	CreatedAt     int64
}

// RecordDelegationMetric writes a delegation outcome for later scheduling
// analysis. It is intentionally best-effort on the hot path: failures are
// returned so the caller can log them without aborting the result flow.
func (s *TaskStore) RecordDelegationMetric(
	ctx context.Context,
	taskID, delegator, executor string,
	abilities []string,
	success bool,
	latencyMs int64,
	tokens int,
	cost ...float64,
) error {
	return s.recordDelegationMetric(ctx, taskID, delegator, executor, abilities, success, latencyMs, tokens, "task", cost...)
}

// RecordAskMetric identifies the ask's aggregate usage. Goal accounting bills
// it through its round ledger, while task supervisor metrics remain task spend.
func (s *TaskStore) RecordAskMetric(ctx context.Context, taskID, delegator, executor string, success bool, latencyMs int64, tokens int, cost float64) error {
	return s.recordDelegationMetric(ctx, taskID, delegator, executor, nil, success, latencyMs, tokens, "ask", cost)
}

func (s *TaskStore) recordDelegationMetric(ctx context.Context, taskID, delegator, executor string, abilities []string, success bool, latencyMs int64, tokens int, source string, cost ...float64) error {
	abilitiesJSON, err := json.Marshal(abilities)
	if err != nil {
		return fmt.Errorf("marshal abilities: %w", err)
	}

	var tokensArg any
	if tokens > 0 {
		tokensArg = tokens
	} else {
		tokensArg = nil
	}

	var costArg any
	if len(cost) > 0 && cost[0] > 0 {
		costArg = cost[0]
	} else {
		costArg = nil
	}

	var writer interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	} = s.db
	var tx *sql.Tx
	if source != "ask" && tokens > 0 {
		tx, err = s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		writer = tx
	}
	_, err = writer.ExecContext(ctx,
		`INSERT INTO delegation_metrics
			(task_id, delegator, executor, abilities_json, success, latency_ms, tokens, cost, created_at, usage_source)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		taskID, delegator, executor, string(abilitiesJSON), boolToInt(success), latencyMs, tokensArg, costArg, storage.Now(), source,
	)
	if err != nil || tx == nil {
		return err
	}
	if err = chargeSettledGoalMetric(ctx, tx, taskID, int64(tokens)); err != nil {
		return err
	}
	return tx.Commit()
}

// recordEntryUsage bills the entry (commander) model's own token consumption
// for one judge call into the delegation metrics, so the panel's tokens column
// reflects the commander's cost alongside adapter delegations. The executor
// label "entry:<model>" keeps the rows distinguishable; a provider that does
// not report usage (delta zero) records nothing. delta belongs to this request,
// never the difference between snapshots of a shared client's counters.
func (c *Core) recordEntryUsage(ctx context.Context, taskID string, client *entry.Client, delta entry.Usage, success bool, latency time.Duration) {
	if delta.Total() == 0 {
		return
	}
	cost := client.EstimateCost(delta.InputTokens, delta.OutputTokens)
	if err := c.store.RecordDelegationMetric(ctx, taskID, string(c.nodeID), "entry:"+client.ModelName(),
		nil, success, latency.Milliseconds(), int(delta.Total()), cost); err != nil {
		c.logger.Warn("record entry usage", "task", taskID, "err", err)
	}
	// §6.1: the judge's own spend comes out of the same mesh-wide token quota
	// the executing agent draws on — a budget that ignored supervision would
	// leak a full round's cost per verdict.
	if _, err := c.store.SpendTokens(ctx, taskID, delta.Total()); err != nil {
		c.logger.Warn("spend judge tokens", "task", taskID, "err", err)
	}
}

// ListDelegationMetrics returns all recorded delegation metrics, newest first.
func (s *TaskStore) ListDelegationMetrics(ctx context.Context) ([]DelegationMetric, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, delegator, executor, abilities_json, success, latency_ms, tokens, cost, created_at
		 FROM delegation_metrics ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("query delegation metrics: %w", err)
	}
	defer rows.Close()

	var out []DelegationMetric
	for rows.Next() {
		var m DelegationMetric
		if err := rows.Scan(&m.ID, &m.TaskID, &m.Delegator, &m.Executor, &m.AbilitiesJSON,
			&m.Success, &m.LatencyMs, &m.Tokens, &m.Cost, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan delegation metric: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("delegation metrics rows: %w", err)
	}
	return out, nil
}

// DelegationMetricsFor returns one task's delegation metric rows, oldest
// first — the per-hop executor timings `panda task <id> --trace` pairs with
// the event log's hop boundaries (Track 3).
func (s *TaskStore) DelegationMetricsFor(ctx context.Context, taskID string) ([]DelegationMetric, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, delegator, executor, abilities_json, success, latency_ms, tokens, cost, created_at
		 FROM delegation_metrics WHERE task_id=? ORDER BY id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("query task delegation metrics: %w", err)
	}
	defer rows.Close()

	var out []DelegationMetric
	for rows.Next() {
		var m DelegationMetric
		if err := rows.Scan(&m.ID, &m.TaskID, &m.Delegator, &m.Executor, &m.AbilitiesJSON,
			&m.Success, &m.LatencyMs, &m.Tokens, &m.Cost, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan delegation metric: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// TaskActivityByDay counts tasks per local calendar day, keyed "2006-01-02".
// It backs the /heatmap usage view: one point per day a task entered the
// system, no matter how the task ended. The 'localtime' modifier keeps day
// boundaries where the operator perceives them — a task created at 23:58
// belongs to today, not to tomorrow's UTC.
func (s *TaskStore) TaskActivityByDay(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT date(created_at, 'unixepoch', 'localtime') AS day, COUNT(*)
		 FROM tasks WHERE created_at > 0 GROUP BY day`)
	if err != nil {
		return nil, fmt.Errorf("query task activity: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int)
	for rows.Next() {
		var day string
		var n int
		if err := rows.Scan(&day, &n); err != nil {
			return nil, fmt.Errorf("scan task activity: %w", err)
		}
		out[day] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("task activity rows: %w", err)
	}
	return out, nil
}

// TaskDayHour is one (local day, local hour) bucket of task creations.
type TaskDayHour struct {
	Day   string // YYYY-MM-DD, local time
	Hour  int    // 0–23, local time
	Count int
}

// TaskActivityByDayHour buckets task creations by local day AND local hour in
// one pass. The panel's usage endpoint needs both shapes from the same rows —
// a day series for the calendar and an hour histogram for "peak hour" — and
// two separate scans of the same table would be two chances to disagree.
func (s *TaskStore) TaskActivityByDayHour(ctx context.Context) ([]TaskDayHour, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT date(created_at, 'unixepoch', 'localtime') AS day,
		        CAST(strftime('%H', created_at, 'unixepoch', 'localtime') AS INTEGER) AS hour,
		        COUNT(*)
		 FROM tasks WHERE created_at > 0 GROUP BY day, hour`)
	if err != nil {
		return nil, fmt.Errorf("query task activity by day/hour: %w", err)
	}
	defer rows.Close()

	var out []TaskDayHour
	for rows.Next() {
		var b TaskDayHour
		if err := rows.Scan(&b.Day, &b.Hour, &b.Count); err != nil {
			return nil, fmt.Errorf("scan task activity: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("task activity rows: %w", err)
	}
	return out, nil
}

// LastDelegateTime returns the timestamp (Unix seconds) of the most recent
// EvDelegate event for a task. It is used to compute delegation latency.
func (s *TaskStore) LastDelegateTime(ctx context.Context, taskID string) (int64, error) {
	var ts sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT ts FROM task_events WHERE task_id=? AND type=? ORDER BY id DESC LIMIT 1`,
		taskID, EvDelegate).Scan(&ts)
	if errors.Is(err, sql.ErrNoRows) || !ts.Valid {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("last delegate time: %w", err)
	}
	return ts.Int64, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xenith/panda/internal/storage"
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
) error {
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

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO delegation_metrics
			(task_id, delegator, executor, abilities_json, success, latency_ms, tokens, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		taskID, delegator, executor, string(abilitiesJSON), boolToInt(success), latencyMs, tokensArg, storage.Now(),
	)
	return err
}

// ListDelegationMetrics returns all recorded delegation metrics, newest first.
func (s *TaskStore) ListDelegationMetrics(ctx context.Context) ([]DelegationMetric, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, delegator, executor, abilities_json, success, latency_ms, tokens, created_at
		 FROM delegation_metrics ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("query delegation metrics: %w", err)
	}
	defer rows.Close()

	var out []DelegationMetric
	for rows.Next() {
		var m DelegationMetric
		if err := rows.Scan(&m.ID, &m.TaskID, &m.Delegator, &m.Executor, &m.AbilitiesJSON,
			&m.Success, &m.LatencyMs, &m.Tokens, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan delegation metric: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("delegation metrics rows: %w", err)
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

// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/taskproviders/tsync"
	"github.com/Xustalis/OpenPanda/internal/util"
	"github.com/google/uuid"
)

type RequestedExecution struct {
	RequestID      string `json:"requestId"`
	AlreadyStarted bool   `json:"alreadyStarted"`
	State          string `json:"state"`
}

// RequestExecution requests the current canonical generation. Explicit reruns
// are human TaskSync operations; device requests cannot create a new generation
// or bypass local policy. The owning runtime later claims the cloud slot.
func (s *Service) RequestExecution(ctx context.Context, connection string, key taskproviders.Key, requestID string) (RequestedExecution, error) {
	if _, err := uuid.Parse(requestID); err != nil {
		return RequestedExecution{}, taskproviders.ErrPrecondition
	}
	provider, c, err := s.businessProvider(ctx, connection)
	if err != nil {
		return RequestedExecution{}, err
	}
	live, err := provider.Task(ctx, key)
	if err != nil {
		return RequestedExecution{}, err
	}
	p, err := s.Policy(ctx, connection, key.SpaceID)
	if err != nil {
		return RequestedExecution{}, err
	}
	if err = eligible(c, p, live, s.now()); err != nil {
		return RequestedExecution{}, err
	}
	command, err := util.UUIDv7()
	if err != nil {
		return RequestedExecution{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RequestedExecution{}, err
	}
	defer tx.Rollback()
	// Lock local authority before checking cached task, policy and request state.
	result, err := tx.ExecContext(ctx, `UPDATE task_connections SET updated_at=updated_at WHERE connection_id=? AND account_id=? AND auth_epoch=? AND generation=? AND state='connected'`, c.ID, c.AccountID, c.AuthEpoch, c.Generation)
	if err == nil {
		err = requireOne(result)
	}
	if err != nil {
		return RequestedExecution{}, err
	}
	current, err := scanConnection(tx.QueryRowContext(ctx, `SELECT `+connectionColumns+` FROM task_connections WHERE connection_id=?`, c.ID))
	if err != nil {
		return RequestedExecution{}, err
	}
	if err = eligible(current, p, live, s.now()); err != nil {
		return RequestedExecution{}, err
	}
	if err = s.automationSnapshotTx(ctx, tx, c, p, live); err != nil {
		return RequestedExecution{}, err
	}
	id, already, err := s.persistExecutionTx(ctx, tx, c, p, live, requestID, command)
	if err != nil {
		return RequestedExecution{}, err
	}
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM task_flow_runs WHERE request_id=?`, id).Scan(&state); err != nil {
		return RequestedExecution{}, err
	}
	if err = tx.Commit(); err != nil {
		return RequestedExecution{}, err
	}
	return RequestedExecution{RequestID: id, AlreadyStarted: already, State: state}, nil
}

// All foreground and automatic requests converge on the same trigger identity.
func (s *Service) persistExecutionTx(ctx context.Context, tx *sql.Tx, c Connection, p Policy, t taskproviders.Snapshot, id, command string) (string, bool, error) {
	var wire tsync.Task
	if err := json.Unmarshal(t.ProviderData, &wire); err != nil {
		return "", false, err
	}
	trigger, err := wire.TriggerKey("execute")
	if err != nil {
		return "", false, err
	}
	var previousConnection, previousAccount, previousKey, previousTrigger string
	err = tx.QueryRowContext(ctx, `SELECT connection_id,account_id,task_key,trigger_key FROM task_flow_runs WHERE request_id=?`, id).Scan(&previousConnection, &previousAccount, &previousKey, &previousTrigger)
	if err == nil && (previousConnection != c.ID || previousAccount != c.AccountID || previousKey != t.Key.String() || previousTrigger != trigger) {
		return "", false, taskproviders.ErrConflict
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT request_id FROM task_flow_runs WHERE connection_id=? AND account_id=? AND task_key=? AND trigger_key=?`, c.ID, c.AccountID, t.Key.String(), trigger).Scan(&existing)
	if err == nil {
		return existing, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_flow_runs WHERE connection_id=? AND account_id=? AND state IN ('claiming','leased','running','recovering','lease_lost','needs_reconciliation')`, c.ID, c.AccountID).Scan(&count); err != nil {
		return "", false, err
	}
	if count >= p.MaxConcurrent {
		return "", false, errors.New("local execution concurrency limit reached")
	}
	claim := tsync.ExecutionRequest{RequestID: id, TriggerKey: trigger, InputRevision: wire.Versions.Input, AssignmentRevision: wire.Versions.Assignment, Generation: wire.Versions.Generation, Purpose: "execute", Scope: tsync.ExecutionScope{FullGoal: true, Objectives: []string{}}, Mode: p.Mode, DeviceID: c.DeviceID}
	snap, _ := json.Marshal(t)
	policy, _ := json.Marshal(p)
	body, err := tsync.JSONBytes(claim)
	if err != nil {
		return "", false, err
	}
	now := s.now().UnixMilli()
	_, err = tx.ExecContext(ctx, `INSERT INTO task_flow_runs(request_id,connection_id,account_id,task_key,trigger_key,auth_epoch,connection_generation,input_revision,assignment_revision,automation_generation,snapshot_json,policy_json,claim_json,claim_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, c.ID, c.AccountID, t.Key.String(), trigger, c.AuthEpoch, c.Generation, wire.Versions.Input, wire.Versions.Assignment, t.AutomationGeneration, string(snap), string(policy), string(body), command, now, now)
	return id, false, err
}

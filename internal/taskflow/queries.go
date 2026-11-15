// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
)

// readableSnapshot binds every private read to the current cached grant. Old
// request IDs are not authority to read a removed task or an unauthorized space.
func (s *Service) readableSnapshot(ctx context.Context, c Connection, key string) (taskproviders.Snapshot, error) {
	var t taskproviders.Snapshot
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT s.snapshot_json FROM task_snapshots s
 JOIN task_connections c ON c.connection_id=s.connection_id AND c.account_id=s.account_id AND c.auth_epoch=s.auth_epoch
 WHERE c.connection_id=? AND c.account_id=? AND c.auth_epoch=? AND c.generation=? AND c.state='connected' AND s.task_key=?`, c.ID, c.AccountID, c.AuthEpoch, c.Generation, key).Scan(&raw)
	if err != nil {
		return t, err
	}
	if err = json.Unmarshal([]byte(raw), &t); err != nil {
		return t, err
	}
	space, ok := authorizedSpace(c, t.Key.SpaceID)
	if !ok || space.WorkspaceID != t.Key.WorkspaceID || c.InstanceID != t.Key.InstanceID || t.Key.String() != key || !t.Permissions.Read || t.Permissions.AuthEpoch != c.AuthEpoch || !hasScope(c.Scopes, "tasks:read") || !hasScope(space.Scopes, "tasks:read") {
		return t, taskproviders.ErrAuthorizationChanged
	}
	return t, nil
}

type RunSummary struct {
	RequestID     string                     `json:"requestId"`
	ConnectionID  string                     `json:"connectionId"`
	TaskKey       string                     `json:"taskKey"`
	Title         string                     `json:"title"`
	State         string                     `json:"state"`
	Error         string                     `json:"error"`
	Mode          taskproviders.ApprovalMode `json:"mode"`
	UpdatedAt     int64                      `json:"updatedAt"`
	account, auth string
	generation    int64
}

// Runs is a bounded discovery view. Content is read separately through Work.
func (s *Service) Runs(ctx context.Context, connection, key string) ([]RunSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT f.request_id,f.connection_id,f.task_key,f.state,f.error_code,f.policy_json,f.updated_at,f.account_id,f.auth_epoch,f.connection_generation FROM task_flow_runs f
 JOIN task_connections c ON c.connection_id=f.connection_id AND c.account_id=f.account_id AND c.auth_epoch=f.auth_epoch AND c.generation=f.connection_generation
 JOIN task_snapshots t ON t.connection_id=c.connection_id AND t.account_id=c.account_id AND t.auth_epoch=c.auth_epoch AND t.task_key=f.task_key
 WHERE c.state='connected' AND (?='' OR f.connection_id=?) AND (?='' OR f.task_key=?)
 AND json_extract(t.snapshot_json,'$.permissions.read')=1 ORDER BY f.updated_at DESC,f.request_id LIMIT 100`, connection, connection, key, key)
	if err != nil {
		return nil, err
	}
	items := []RunSummary{}
	for rows.Next() {
		var r RunSummary
		var raw string
		if err = rows.Scan(&r.RequestID, &r.ConnectionID, &r.TaskKey, &r.State, &r.Error, &raw, &r.UpdatedAt, &r.account, &r.auth, &r.generation); err != nil {
			break
		}
		var p Policy
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			break
		}
		r.Mode = p.Mode
		items = append(items, r)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	out := []RunSummary{}
	for _, r := range items {
		c, e := s.Get(ctx, r.ConnectionID)
		if e != nil {
			return nil, e
		}
		if c.AccountID != r.account || c.AuthEpoch != r.auth || c.Generation != r.generation {
			continue
		}
		t, e := s.readableSnapshot(ctx, c, r.TaskKey)
		if errors.Is(e, sql.ErrNoRows) || errors.Is(e, taskproviders.ErrAuthorizationChanged) {
			continue
		}
		if e != nil {
			return nil, e
		}
		r.Title = t.Title
		out = append(out, r)
	}
	return out, nil
}

// SyncStatus intentionally contains no signed proof, request body or credential.
type SyncStatus struct {
	EventID  string `json:"eventId"`
	Kind     string `json:"kind"`
	State    string `json:"state"`
	Error    string `json:"error"`
	Attempts int    `json:"attempts"`
	NextAt   int64  `json:"nextAt"`
}

type ResearchBrief struct {
	Brief          string `json:"brief"`
	State          string `json:"state"`
	InputRevision  string `json:"inputRevision"`
	ModelVersion   string `json:"modelVersion"`
	ReservedTokens int    `json:"reservedTokens"`
	CreatedAt      int64  `json:"createdAt"`
	ExpiresAt      int64  `json:"expiresAt"`
}

// Research remains private and is hidden immediately after assignment, policy
// expiry, scope loss, input changes or account replacement.
func (s *Service) Research(ctx context.Context, connection, key string) ([]ResearchBrief, error) {
	c, err := s.Get(ctx, connection)
	if err != nil {
		return nil, err
	}
	t, err := s.readableSnapshot(ctx, c, key)
	if err != nil {
		return nil, err
	}
	p, err := s.Policy(ctx, connection, t.Key.SpaceID)
	if err != nil {
		return nil, err
	}
	if err = p.validate(s.now()); err != nil {
		return nil, err
	}
	if err = researchEligible(c, p, t); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT brief,state,input_revision,model_version,reserved_tokens,created_at,expires_at FROM research_briefs
 WHERE connection_id=? AND account_id=? AND task_key=? AND input_revision=? AND auth_epoch=? AND expires_at>? AND brief!='' ORDER BY created_at DESC LIMIT 1`, connection, c.AccountID, key, t.InputRevision, c.AuthEpoch, s.now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ResearchBrief{}
	for rows.Next() {
		var b ResearchBrief
		if err = rows.Scan(&b.Brief, &b.State, &b.InputRevision, &b.ModelVersion, &b.ReservedTokens, &b.CreatedAt, &b.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

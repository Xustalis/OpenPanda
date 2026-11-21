// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/taskflow/finalaction"
	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/taskproviders/tsync"
)

// Policy is local, account/space scoped operation authorization. A remote task
// cannot edit it. Connecting a provider creates no policy and grants only sync.
// Capabilities are enforced by concrete adapters; draft has no tools or network
// access beyond the configured model transport.
type Policy struct {
	Research          bool                       `json:"research"`
	Execute           bool                       `json:"execute"`
	FinalAction       bool                       `json:"finalAction"`
	Mode              taskproviders.ApprovalMode `json:"mode"`
	ExpiresAt         int64                      `json:"expiresAt"`
	MaxConcurrent     int                        `json:"maxConcurrent"`
	DailyTokens       int                        `json:"dailyTokens"`
	PreparationTokens int                        `json:"preparationTokens"`
	TaskIDs           []string                   `json:"taskIds"`
	ProjectID         string                     `json:"projectId"`
	Capabilities      []string                   `json:"capabilities"`
	Publication       *FilePublication           `json:"publication,omitempty"`
}

// FilePublication is explicit local authorization. The model cannot choose a
// directory or filename. FullGoal is a separate human assertion of coverage;
// it defaults to false, keeping a published draft from completing the task.
type FilePublication struct {
	Directory         string `json:"directory"`
	DirectoryIdentity string `json:"directoryIdentity,omitempty"`
	Filename          string `json:"filename"`
	FullGoal          bool   `json:"fullGoal"`
}

func (p Policy) validate(now time.Time) error {
	if !p.Research && !p.Execute && !p.FinalAction {
		return nil
	}
	if p.ExpiresAt <= now.UnixMilli() || p.ExpiresAt > now.Add(30*24*time.Hour).UnixMilli() {
		return errors.New("automation policy must expire within 30 days")
	}
	if p.MaxConcurrent < 1 || p.MaxConcurrent > 8 || p.DailyTokens < 2300 || p.DailyTokens > 1000000 || p.PreparationTokens < 256 || p.PreparationTokens > 8192 {
		return errors.New("invalid automation budget or concurrency")
	}
	if p.Mode != taskproviders.BeforeSubmit && p.Mode != taskproviders.AfterExecution {
		return errors.New("explicit approval mode required")
	}
	for _, capability := range p.Capabilities {
		if capability != "draft" && capability != "file_publish" {
			return errors.New("unsupported operation capability")
		}
	}
	if p.Execute && !slices.Contains(p.Capabilities, "draft") {
		return errors.New("draft operation authorization required")
	}
	if p.FinalAction {
		if !p.Execute || !slices.Contains(p.Capabilities, "file_publish") || p.Publication == nil {
			return errors.New("file publication requires explicit operation authorization and destination")
		}
		if err := finalaction.ValidateDestination(p.Publication.Directory, strings.ReplaceAll(p.Publication.Filename, "{request}", "request")); err != nil {
			return err
		}
		if p.Publication.DirectoryIdentity != "" {
			id, err := finalaction.DirectoryIdentity(p.Publication.Directory)
			if err != nil {
				return err
			}
			if id != p.Publication.DirectoryIdentity {
				return errors.New("publication directory changed")
			}
		}
	} else if p.Publication != nil {
		return errors.New("publication requires final action authorization")
	}
	return nil
}

func (s *Service) SetPolicy(ctx context.Context, connection, space string, p Policy) error {
	if err := p.validate(s.now()); err != nil {
		return err
	}
	if p.FinalAction {
		publication := *p.Publication
		var err error
		publication.DirectoryIdentity, err = finalaction.DirectoryIdentity(publication.Directory)
		if err != nil {
			return err
		}
		p.Publication = &publication
	}
	c, err := s.Get(ctx, connection)
	if err != nil {
		return err
	}
	if c.State != "connected" || c.AccountID == "" {
		return ErrStale
	}
	if _, ok := authorizedSpace(c, space); !ok {
		return taskproviders.ErrAuthorizationChanged
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `UPDATE task_connections SET updated_at=updated_at WHERE connection_id=? AND account_id=? AND auth_epoch=? AND generation=? AND state='connected'`, c.ID, c.AccountID, c.AuthEpoch, c.Generation)
	if err == nil {
		err = requireOne(r)
	}
	if err != nil {
		return err
	}
	var old string
	err = tx.QueryRowContext(ctx, `SELECT policy_json FROM task_policies WHERE connection_id=? AND account_id=? AND space_id=?`, c.ID, c.AccountID, space).Scan(&old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if old == string(raw) {
		return tx.Commit()
	}
	if err = invalidateAutomation(ctx, tx, c.ID, space); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_policies(connection_id,account_id,space_id,policy_json,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(connection_id,account_id,space_id) DO UPDATE SET policy_json=excluded.policy_json,updated_at=excluded.updated_at`, c.ID, c.AccountID, space, string(raw), s.now().UnixMilli())
	if err != nil {
		return err
	}
	err = tx.Commit()
	return err
}

func (s *Service) Policy(ctx context.Context, connection, space string) (Policy, error) {
	c, err := s.Get(ctx, connection)
	if err != nil {
		return Policy{}, err
	}
	if _, ok := authorizedSpace(c, space); !ok || c.State != "connected" {
		return Policy{}, taskproviders.ErrAuthorizationChanged
	}
	var raw string
	err = s.db.QueryRowContext(ctx, `SELECT p.policy_json FROM task_policies p JOIN task_connections c ON c.connection_id=p.connection_id AND c.account_id=p.account_id WHERE c.connection_id=? AND c.state='connected' AND p.space_id=?`, connection, space).Scan(&raw)
	var p Policy
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &p)
	}
	return p, err
}

func authorizedSpace(c Connection, space string) (taskproviders.Space, bool) {
	for _, s := range c.Spaces {
		if s.SpaceID == space {
			return s, true
		}
	}
	return taskproviders.Space{}, false
}

// eligible is deterministic and runs before any cloud claim or model call.
// Canonical permissions.claim includes current direct assignment or an accepted,
// version-bound delegation. The server rechecks its scope atomically at claim.
func eligible(c Connection, p Policy, t taskproviders.Snapshot, now time.Time) error {
	if err := t.Validate(); err != nil {
		return err
	}
	if err := p.validate(now); err != nil {
		return err
	}
	space, ok := authorizedSpace(c, t.Key.SpaceID)
	if !ok || space.WorkspaceID != t.Key.WorkspaceID || c.InstanceID != t.Key.InstanceID || c.State != "connected" || c.AuthEpoch != t.Permissions.AuthEpoch {
		return taskproviders.ErrAuthorizationChanged
	}
	if !p.Execute || !t.Permissions.Read || !t.Permissions.Execute {
		return errors.New("task is not authorized for automation")
	}
	for _, scope := range []string{"tasks:read", "execution:read", "execution:claim", "execution:report", "delivery:write"} {
		if !hasScope(c.Scopes, scope) || !hasScope(space.Scopes, scope) {
			return taskproviders.ErrUnsupported
		}
	}
	if !c.Capabilities.Supports("executions") || !c.Capabilities.Supports("deliveries") || (p.FinalAction && !c.Capabilities.Supports("finalActions")) {
		return taskproviders.ErrUnsupported
	}
	if len(p.TaskIDs) > 0 && !slices.Contains(p.TaskIDs, t.Key.TaskID) || p.ProjectID != "" && p.ProjectID != t.ProjectID {
		return errors.New("task outside local automation scope")
	}
	if t.Status == "done" || t.Status == "cancelled" || t.Status == "archived" {
		return errors.New("task is closed")
	}
	if t.StartAt != nil {
		start, err := t.StartAt.ScheduleTime()
		if err != nil {
			return err
		}
		if now.Before(start) {
			return errors.New("task is not due to start")
		}
	}
	if t.DueAt != nil {
		if _, err := t.DueAt.ScheduleTime(); err != nil {
			return err
		}
	}
	var wire tsync.Task
	if err := json.Unmarshal(t.ProviderData, &wire); err != nil {
		return errors.New("canonical task data unavailable")
	}
	if wire.Key != t.Key || wire.Versions.Input != t.InputRevision || wire.Versions.Assignment != t.AssignmentRevision || wire.ApprovalMode != p.Mode || !wire.BodyAvailable || !wire.Permissions.Claim {
		return errors.New("task version, mode or input unavailable")
	}
	for _, d := range wire.Dependencies {
		if d.Direction == "incoming" && d.Status != "done" {
			return errors.New("task dependency is not satisfied")
		}
	}
	return nil
}

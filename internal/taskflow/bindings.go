// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/taskproviders"
)

// ProjectBinding is an explicit account-scoped mapping. An empty RemoteProject
// binds a space; an empty LocalProject in SetBinding removes that mapping.
// Directory and execution permissions remain the local project's own settings.
type ProjectBinding struct {
	RemoteProject string `json:"remoteProjectId"`
	LocalProject  string `json:"localProject"`
}

func bindingSpace(c Connection, space string) (taskproviders.Space, error) {
	sp, ok := authorizedSpace(c, space)
	if !ok || c.State != "connected" || !hasScope(c.Scopes, "tasks:read") || !hasScope(sp.Scopes, "tasks:read") {
		return sp, taskproviders.ErrAuthorizationChanged
	}
	return sp, nil
}

func (s *Service) Bindings(ctx context.Context, connection, space string) ([]ProjectBinding, error) {
	c, err := s.Get(ctx, connection)
	if err != nil {
		return nil, err
	}
	sp, err := bindingSpace(c, space)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT b.remote_project_id,b.local_project FROM task_project_bindings b
 JOIN task_connections c ON c.connection_id=b.connection_id AND c.account_id=b.account_id
 WHERE c.connection_id=? AND c.account_id=? AND c.generation=? AND c.auth_epoch=? AND c.state='connected' AND b.workspace_id=? AND b.space_id=?
 AND EXISTS(SELECT 1 FROM json_each(c.scopes_json) WHERE value='tasks:read')
 AND EXISTS(SELECT 1 FROM json_each(c.spaces_json) sp WHERE json_extract(sp.value,'$.spaceId')=b.space_id AND json_extract(sp.value,'$.workspaceId')=b.workspace_id AND EXISTS(SELECT 1 FROM json_each(json_extract(sp.value,'$.scopes')) WHERE value='tasks:read'))
 ORDER BY b.remote_project_id`, c.ID, c.AccountID, c.Generation, c.AuthEpoch, sp.WorkspaceID, space)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectBinding{}
	for rows.Next() {
		var b ProjectBinding
		if err = rows.Scan(&b.RemoteProject, &b.LocalProject); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Service) SetBinding(ctx context.Context, connection, space string, b ProjectBinding) error {
	c, err := s.Get(ctx, connection)
	if err != nil {
		return err
	}
	sp, err := bindingSpace(c, space)
	if err != nil {
		return err
	}
	if b.LocalProject != "" {
		if _, err = projects.NewStore(s.db).Get(b.LocalProject); err != nil {
			return err
		}
	}
	if b.RemoteProject != "" {
		key := taskproviders.Key{InstanceID: c.InstanceID, WorkspaceID: sp.WorkspaceID, SpaceID: space, TaskID: b.RemoteProject}
		if err = key.Validate(); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `UPDATE task_connections SET updated_at=updated_at WHERE connection_id=? AND account_id=? AND auth_epoch=? AND generation=? AND state='connected'
 AND EXISTS(SELECT 1 FROM json_each(scopes_json) WHERE value='tasks:read')
 AND EXISTS(SELECT 1 FROM json_each(spaces_json) sp WHERE json_extract(sp.value,'$.spaceId')=? AND json_extract(sp.value,'$.workspaceId')=? AND EXISTS(SELECT 1 FROM json_each(json_extract(sp.value,'$.scopes')) WHERE value='tasks:read'))`, c.ID, c.AccountID, c.AuthEpoch, c.Generation, space, sp.WorkspaceID)
	if err == nil {
		err = requireOne(r)
	}
	if err != nil {
		return err
	}
	var old string
	err = tx.QueryRowContext(ctx, `SELECT local_project FROM task_project_bindings WHERE connection_id=? AND account_id=? AND workspace_id=? AND space_id=? AND remote_project_id=?`, c.ID, c.AccountID, sp.WorkspaceID, space, b.RemoteProject).Scan(&old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if old == b.LocalProject {
		return tx.Commit()
	}
	// Stop in-flight publication before changing which local project memory is
	// permitted. A model response using the previous mapping cannot be adopted.
	if err = invalidateAutomation(ctx, tx, c.ID, space); err != nil {
		return err
	}
	if b.LocalProject == "" {
		_, err = tx.ExecContext(ctx, `DELETE FROM task_project_bindings WHERE connection_id=? AND account_id=? AND workspace_id=? AND space_id=? AND remote_project_id=?`, c.ID, c.AccountID, sp.WorkspaceID, space, b.RemoteProject)
	} else {
		var exists int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE name=?`, b.LocalProject).Scan(&exists); err != nil {
			return err
		}
		if exists != 1 {
			return projects.ErrNotFound
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO task_project_bindings(connection_id,account_id,workspace_id,space_id,remote_project_id,local_project) VALUES(?,?,?,?,?,?) ON CONFLICT(connection_id,account_id,workspace_id,space_id,remote_project_id) DO UPDATE SET local_project=excluded.local_project`, c.ID, c.AccountID, sp.WorkspaceID, space, b.RemoteProject, b.LocalProject)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

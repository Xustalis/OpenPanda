// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"encoding/json"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
)

// businessProvider refreshes identity and capabilities for this foreground
// command. It never borrows the synchronization owner's mutable provider.
func (s *Service) businessProvider(ctx context.Context, id string) (taskproviders.TaskProvider, Connection, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return nil, c, err
	}
	if c.State != "connected" || c.AccountID == "" {
		return nil, c, taskproviders.ErrReconnect
	}
	p, _, err := s.source(c)
	if err != nil {
		return nil, c, err
	}
	caps, err := p.Capabilities(ctx)
	if err != nil {
		return nil, c, err
	}
	me, err := p.Me(ctx)
	if err != nil {
		return nil, c, err
	}
	spaces, err := p.Spaces(ctx)
	if err != nil {
		return nil, c, err
	}
	if me.AccountID != c.AccountID || me.DeviceID != c.DeviceID || me.AuthEpoch != c.AuthEpoch || caps.InstanceID != c.InstanceID {
		return nil, c, taskproviders.ErrAuthorizationChanged
	}
	if err = s.currentConnection(ctx, c); err != nil {
		return nil, c, err
	}
	c.Capabilities, c.Scopes, c.Spaces = caps, me.Scopes, spaces
	return p, c, nil
}
func (s *Service) currentConnection(ctx context.Context, expected Connection) error {
	current, err := s.Get(ctx, expected.ID)
	if err != nil {
		return err
	}
	if current.State != "connected" || current.AccountID != expected.AccountID || current.DeviceID != expected.DeviceID || current.AuthEpoch != expected.AuthEpoch || current.Generation != expected.Generation || current.InstanceID != expected.InstanceID {
		return taskproviders.ErrAuthorizationChanged
	}
	return nil
}
func writableSpace(c Connection, key taskproviders.Key) error {
	sp, ok := authorizedSpace(c, key.SpaceID)
	if !ok || key.InstanceID != c.InstanceID || key.WorkspaceID != sp.WorkspaceID || !hasScope(c.Scopes, "tasks:write") || !hasScope(sp.Scopes, "tasks:write") || !hasScope(c.Scopes, "tasks:read") || !hasScope(sp.Scopes, "tasks:read") {
		return taskproviders.ErrAuthorizationChanged
	}
	if !c.Capabilities.Supports("taskWrite") {
		return taskproviders.ErrUnsupported
	}
	return nil
}

func (s *Service) CreateTask(ctx context.Context, connection, space string, input taskproviders.Snapshot, command string) (taskproviders.Snapshot, error) {
	p, c, err := s.businessProvider(ctx, connection)
	if err != nil {
		return taskproviders.Snapshot{}, err
	}
	sp, ok := authorizedSpace(c, space)
	if !ok {
		return taskproviders.Snapshot{}, taskproviders.ErrAuthorizationChanged
	}
	input.Key = taskproviders.Key{InstanceID: c.InstanceID, WorkspaceID: sp.WorkspaceID, SpaceID: space}
	if err = writableSpace(c, input.Key); err != nil {
		return taskproviders.Snapshot{}, err
	}
	writer, ok := p.(taskproviders.TaskWriter)
	if !ok {
		return taskproviders.Snapshot{}, taskproviders.ErrUnsupported
	}
	result, err := writer.CreateTask(ctx, input, command)
	if err != nil {
		return taskproviders.Snapshot{}, err
	}
	if err = s.currentConnection(ctx, c); err != nil {
		return taskproviders.Snapshot{}, err
	}
	current, err := s.Get(ctx, c.ID)
	if err != nil {
		return taskproviders.Snapshot{}, err
	}
	if err = writableSpace(current, result.Key); err != nil {
		return taskproviders.Snapshot{}, err
	}
	// Only the runtime publishes snapshots/cursors. A mutation response cannot
	// overwrite a newer feed revision or publish a partial space snapshot.
	if err = s.RequestSync(ctx, c.ID); err != nil {
		return taskproviders.Snapshot{}, err
	}
	return result, nil
}
func (s *Service) PatchTask(ctx context.Context, connection string, key taskproviders.Key, revision string, patch json.RawMessage, command string) (taskproviders.Snapshot, error) {
	p, c, err := s.businessProvider(ctx, connection)
	if err != nil {
		return taskproviders.Snapshot{}, err
	}
	if err = writableSpace(c, key); err != nil {
		return taskproviders.Snapshot{}, err
	}
	writer, ok := p.(taskproviders.TaskWriter)
	if !ok {
		return taskproviders.Snapshot{}, taskproviders.ErrUnsupported
	}
	result, err := writer.PatchTask(ctx, key, revision, patch, command)
	if err != nil {
		return taskproviders.Snapshot{}, err
	}
	if err = s.currentConnection(ctx, c); err != nil {
		return taskproviders.Snapshot{}, err
	}
	current, err := s.Get(ctx, c.ID)
	if err != nil {
		return taskproviders.Snapshot{}, err
	}
	if err = writableSpace(current, result.Key); err != nil {
		return taskproviders.Snapshot{}, err
	}
	if err = s.RequestSync(ctx, c.ID); err != nil {
		return taskproviders.Snapshot{}, err
	}
	return result, nil
}

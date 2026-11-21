// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
)

// automationSnapshotTx repeats authorization under the publication write lock.
// Network checks cannot cover a local policy edit or sync invalidation that
// commits while a model request is in flight.
func (s *Service) automationSnapshotTx(ctx context.Context, tx *sql.Tx, c Connection, p Policy, t taskproviders.Snapshot) error {
	if err := p.validate(s.now()); err != nil {
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	var currentRaw string
	err = tx.QueryRowContext(ctx, `SELECT s.snapshot_json FROM task_connections c
 JOIN task_policies p ON p.connection_id=c.connection_id AND p.account_id=c.account_id
 JOIN task_snapshots s ON s.connection_id=c.connection_id AND s.account_id=c.account_id
 WHERE c.connection_id=? AND c.account_id=? AND c.auth_epoch=? AND c.generation=? AND c.state='connected'
 AND p.space_id=? AND p.policy_json=? AND s.task_key=? AND s.auth_epoch=c.auth_epoch
 AND json_extract(s.snapshot_json,'$.inputRevision')=?
 AND json_extract(s.snapshot_json,'$.assignmentRevision')=?
 AND CAST(json_extract(s.snapshot_json,'$.automationGeneration') AS TEXT)=?`, c.ID, c.AccountID, c.AuthEpoch, c.Generation,
		t.Key.SpaceID, string(raw), t.Key.String(), t.InputRevision, t.AssignmentRevision, t.AutomationGeneration).Scan(&currentRaw)
	if err != nil {
		return err
	}
	var current taskproviders.Snapshot
	if err := json.Unmarshal([]byte(currentRaw), &current); err != nil {
		return err
	}
	// Permissions and closure can change without an input revision. Publication
	// must not rely on a model call's stale authorization snapshot.
	if current.TaskRevision != t.TaskRevision || current.Status != t.Status ||
		digest(current.Permissions) != digest(t.Permissions) ||
		!slices.Equal(current.AssigneeAccountIDs, t.AssigneeAccountIDs) {
		return taskproviders.ErrAuthorizationChanged
	}
	return nil
}

func researchEligible(c Connection, p Policy, t taskproviders.Snapshot) error {
	// Research is off until the local policy says so. That is a local switch,
	// not a changed grant, and conflating the two hides the remedy.
	if !p.Research {
		return taskproviders.ErrAutomationNotAuthorized
	}
	space, ok := authorizedSpace(c, t.Key.SpaceID)
	if !ok || c.State != "connected" || c.InstanceID != t.Key.InstanceID || space.WorkspaceID != t.Key.WorkspaceID || c.AuthEpoch != t.Permissions.AuthEpoch || !t.Permissions.Read || !hasScope(c.Scopes, "tasks:read") || !hasScope(space.Scopes, "tasks:read") {
		return taskproviders.ErrAuthorizationChanged
	}
	if len(t.AssigneeAccountIDs) != 0 || t.Status == "done" || t.Status == "cancelled" || t.Status == "archived" {
		return taskproviders.ErrUnsupported
	}
	if len(p.TaskIDs) > 0 && !slices.Contains(p.TaskIDs, t.Key.TaskID) || p.ProjectID != "" && p.ProjectID != t.ProjectID {
		return taskproviders.ErrUnsupported
	}
	// Ambiguous time values cannot silently activate background automation.
	for _, at := range []*taskproviders.TypedTime{t.StartAt, t.DueAt} {
		if at != nil {
			if _, err := at.ScheduleTime(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) liveResearch(ctx context.Context, c Connection, p Policy, t taskproviders.Snapshot) error {
	client, err := s.executionClient(c)
	if err != nil {
		return err
	}
	me, err := client.Me(ctx)
	if err != nil {
		return err
	}
	if me.Account.ID != c.AccountID || !me.Account.Active || me.Device == nil || me.Device.ID != c.DeviceID || me.Device.Revoked || fmt.Sprint(me.AuthEpoch) != c.AuthEpoch {
		return taskproviders.ErrAuthorizationChanged
	}
	live := c
	live.Scopes = me.Scopes
	live.Spaces = nil
	for _, sp := range me.Spaces {
		if !sp.Archived {
			live.Spaces = append(live.Spaces, taskproviders.Space{SpaceID: sp.SpaceID, WorkspaceID: sp.WorkspaceID, Scopes: sp.Scopes})
		}
	}
	wire, err := client.Task(ctx, t.Key)
	if err != nil {
		return err
	}
	current, err := wire.Snapshot(me.AuthEpoch, t.SpaceName)
	if err != nil {
		return err
	}
	if current.Key != t.Key || current.InputRevision != t.InputRevision || current.AssignmentRevision != t.AssignmentRevision || current.AutomationGeneration != t.AutomationGeneration {
		return taskproviders.ErrAuthorizationChanged
	}
	return researchEligible(live, p, current)
}

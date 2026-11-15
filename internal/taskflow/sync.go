// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/taskproviders/tsync"
)

func hasScope(scopes []string, want string) bool {
	for _, v := range scopes {
		if v == want {
			return true
		}
	}
	return false
}
func (s *Service) syncConnection(ctx context.Context, owner, id string) error {
	c, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if c.State == "authorizing" {
		if _, err = s.FinishConnect(ctx, id); err != nil {
			return s.failed(ctx, owner, c, err, nil)
		}
	}
	unlock, err := s.lockConnection(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	c, err = s.Get(ctx, id)
	if err != nil {
		return err
	}
	if c.State != "connected" && c.State != "unsupported" {
		return ErrStale
	}
	p, disable, err := s.source(c)
	if err != nil {
		return s.failed(ctx, owner, c, err, nil)
	}
	caps, err := p.Capabilities(ctx)
	if err == nil && (!caps.Supports("taskRead") || !caps.Supports("snapshots") || !caps.Supports("changes")) {
		err = taskproviders.ErrUnsupported
	}
	var account taskproviders.Account
	if err == nil {
		account, err = p.Me(ctx)
	}
	var spaces []taskproviders.Space
	if err == nil {
		spaces, err = p.Spaces(ctx)
	}
	if err == nil && (!hasScope(account.Scopes, "tasks:read") || account.AccountID == "" || account.DeviceID == "" || account.AuthEpoch == "") {
		err = taskproviders.ErrReconnect
	}
	if err == nil && (c.AccountID != "" && c.AccountID != account.AccountID || c.DeviceID != "" && c.DeviceID != account.DeviceID || c.InstanceID != "" && c.InstanceID != caps.InstanceID) {
		err = taskproviders.ErrReconnect
	}
	if err != nil {
		return s.failed(ctx, owner, c, err, disable)
	}
	readable := []taskproviders.Space{}
	for _, space := range spaces {
		if hasScope(space.Scopes, "tasks:read") {
			readable = append(readable, space)
		}
	}
	spaces = readable
	if err = s.identity(ctx, owner, c, caps, account, spaces); err != nil {
		return s.failed(ctx, owner, c, err, disable)
	}
	c, err = s.Get(ctx, id)
	if err != nil {
		return err
	}
	active := false
	for _, space := range spaces {
		changed, e := s.syncSpace(ctx, owner, c, p, space)
		if e != nil {
			return s.failed(ctx, owner, c, e, disable)
		}
		active = active || changed
	}
	delay := 5 * time.Minute
	if active {
		delay = 30 * time.Second
	}
	delay = time.Duration(float64(delay) * (0.9 + rand.Float64()*0.2))
	now := s.now().UnixMilli()
	return s.write(ctx, owner, c, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE task_connections SET synced_at=?,next_sync_at=?,failures=0,last_error='',updated_at=? WHERE connection_id=?`, now, now+delay.Milliseconds(), now, id)
		return err
	})
}
func (s *Service) write(ctx context.Context, owner string, c Connection, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Take SQLite's write reservation before checking ownership; a transaction
	// must not upgrade a stale read snapshot over a reconnect or lease takeover.
	r, err := tx.ExecContext(ctx, `UPDATE task_connections SET updated_at=updated_at WHERE connection_id=? AND generation=? AND state IN ('connected','authorizing','unsupported') AND EXISTS(SELECT 1 FROM task_runtime_leases WHERE lease_key='sync' AND owner_id=? AND expires_at>?)`, c.ID, c.Generation, owner, s.now().UnixMilli())
	if err == nil {
		err = requireOne(r)
	}
	if err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Service) identity(ctx context.Context, owner string, c Connection, caps taskproviders.Capabilities, account taskproviders.Account, spaces []taskproviders.Space) error {
	capsJSON, _ := json.Marshal(caps)
	spacesJSON, _ := json.Marshal(spaces)
	scopesJSON, _ := json.Marshal(account.Scopes)
	return s.write(ctx, owner, c, func(tx *sql.Tx) error {
		if c.AuthEpoch != account.AuthEpoch || c.AccountID != account.AccountID {
			if err := clearConnection(ctx, tx, c.ID); err != nil {
				return err
			}
		} else {
			keep := map[string]bool{}
			for _, space := range spaces {
				keep[space.SpaceID] = true
			}
			for _, old := range c.Spaces {
				if !keep[old.SpaceID] {
					if err := deleteSpace(ctx, tx, c, old); err != nil {
						return err
					}
				}
			}
		}
		for _, space := range spaces {
			if !caps.Supports("members") || !hasScope(account.Scopes, "members:read") || !hasScope(space.Scopes, "members:read") {
				if _, err := tx.ExecContext(ctx, `DELETE FROM task_directories WHERE connection_id=? AND account_id=? AND space_id=?`, c.ID, account.AccountID, space.SpaceID); err != nil {
					return err
				}
			}
		}
		if c.FeedEpoch != caps.FeedEpoch {
			if _, err := tx.ExecContext(ctx, `DELETE FROM task_space_cursors WHERE connection_id=?`, c.ID); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE task_connections SET account_id=?,device_id=?,instance_id=?,auth_epoch=?,feed_epoch=?,capabilities_json=?,spaces_json=?,scopes_json=?,state='connected' WHERE connection_id=?`, account.AccountID, account.DeviceID, caps.InstanceID, account.AuthEpoch, caps.FeedEpoch, string(capsJSON), string(spacesJSON), string(scopesJSON), c.ID)
		return err
	})
}
func deleteSpace(ctx context.Context, tx *sql.Tx, c Connection, space taskproviders.Space) error {
	prefix := c.InstanceID + "/" + space.WorkspaceID + "/" + space.SpaceID + "/"
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_snapshots WHERE connection_id=? AND account_id=? AND substr(task_key,1,length(?))=?`, c.ID, c.AccountID, prefix, prefix); err != nil {
		return err
	}
	for _, table := range []string{"task_space_cursors", "task_directories", "task_sync_events", "task_policies", "task_project_bindings"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE connection_id=? AND account_id=? AND space_id=?`, c.ID, c.AccountID, space.SpaceID); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) syncSpace(ctx context.Context, owner string, c Connection, p taskproviders.TaskProvider, space taskproviders.Space) (bool, error) {
	var cursor, epoch, auth, workspace string
	err := s.db.QueryRowContext(ctx, `SELECT cursor,feed_epoch,auth_epoch,workspace_id FROM task_space_cursors WHERE connection_id=? AND account_id=? AND space_id=?`, c.ID, c.AccountID, space.SpaceID).Scan(&cursor, &epoch, &auth, &workspace)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if cursor == "" || epoch != c.FeedEpoch || auth != c.AuthEpoch || workspace != space.WorkspaceID {
		return s.bootstrap(ctx, owner, c, p, space)
	}
	active := false
	for pages := 0; pages < 10000; pages++ {
		changes, err := p.Changes(ctx, space.SpaceID, c.FeedEpoch, cursor)
		if errors.Is(err, taskproviders.ErrCursorExpired) {
			return s.bootstrap(ctx, owner, c, p, space)
		}
		if err != nil {
			return active, err
		}
		if changes.AuthEpoch != c.AuthEpoch {
			return active, taskproviders.ErrAuthorizationChanged
		}
		if changes.FeedEpoch != c.FeedEpoch || changes.Cursor == "" || changes.HasMore && changes.Cursor == cursor {
			return active, errors.New("invalid change continuation")
		}
		members := []taskproviders.Member(nil)
		if changes.MembersChanged && c.Capabilities.Supports("members") && hasScope(c.Scopes, "members:read") && hasScope(space.Scopes, "members:read") {
			members, err = p.Members(ctx, space.SpaceID)
			if err != nil {
				return active, err
			}
		}
		// Snapshot writes, deletion, durable evaluation and cursor advancement
		// have a single commit. A crash can replay the page without skipping work.
		err = s.write(ctx, owner, c, func(tx *sql.Tx) error {
			for _, t := range changes.Tasks {
				if err := s.putTask(ctx, tx, c, space, t); err != nil {
					return err
				}
			}
			for _, key := range changes.Removed {
				if err := validateTask(c, space, taskproviders.Snapshot{Key: key}, false); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM task_snapshots WHERE connection_id=? AND account_id=? AND task_key=?`, c.ID, c.AccountID, key.String()); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM task_sync_events WHERE connection_id=? AND account_id=? AND task_key=? AND event_type='task_evaluate'`, c.ID, c.AccountID, key.String()); err != nil {
					return err
				}
			}
			for i, event := range changes.Events {
				if err := s.event(ctx, tx, c, space.SpaceID, eventID(cursor, changes.Cursor, fmt.Sprint(i)), "", event.Type, event.Data); err != nil {
					return err
				}
			}
			if members != nil {
				if err := s.directory(ctx, tx, c, space, members); err != nil {
					return err
				}
			}
			if changes.NeedsSnapshot {
				_, err := tx.ExecContext(ctx, `DELETE FROM task_space_cursors WHERE connection_id=? AND account_id=? AND space_id=?`, c.ID, c.AccountID, space.SpaceID)
				return err
			}
			return s.cursor(ctx, tx, c, space, changes.Cursor)
		})
		if err != nil {
			return active, err
		}
		if changes.NeedsSnapshot {
			return s.bootstrap(ctx, owner, c, p, space)
		}
		active = active || len(changes.Tasks) > 0 || len(changes.Removed) > 0 || len(changes.Events) > 0
		cursor = changes.Cursor
		if !changes.HasMore {
			return active, nil
		}
	}
	return active, errors.New("change pagination exceeds limit")
}
func (s *Service) bootstrap(ctx context.Context, owner string, c Connection, p taskproviders.TaskProvider, space taskproviders.Space) (bool, error) {
	first, err := p.BeginSnapshot(ctx, space.SpaceID)
	if err != nil {
		return false, err
	}
	if first.SnapshotID == "" || first.Cursor == "" || first.AuthEpoch != c.AuthEpoch || first.FeedEpoch != c.FeedEpoch || !s.now().Before(first.ExpiresAt) || first.TotalTasks > 100000 {
		return false, errors.New("invalid snapshot metadata")
	}
	tasks := []taskproviders.Snapshot{}
	seen := map[string]bool{}
	tokens := map[string]bool{}
	bytes := 0
	page := first
	for pages := 0; ; pages++ {
		if pages >= 10000 || page.SnapshotID != first.SnapshotID || page.Cursor != first.Cursor || page.AuthEpoch != first.AuthEpoch || page.FeedEpoch != first.FeedEpoch || page.TotalTasks != first.TotalTasks || !page.ExpiresAt.Equal(first.ExpiresAt) || !s.now().Before(first.ExpiresAt) {
			return false, errors.New("snapshot pagination changed identity or expired")
		}
		for _, t := range page.Tasks {
			if err = validateTask(c, space, t, true); err != nil {
				return false, err
			}
			if seen[t.Key.String()] {
				return false, errors.New("duplicate task in snapshot")
			}
			seen[t.Key.String()] = true
			raw, e := json.Marshal(t)
			if e != nil {
				return false, e
			}
			bytes += len(raw)
			if bytes > 64<<20 || uint64(len(tasks)+1) > first.TotalTasks {
				return false, errors.New("snapshot exceeds declared size or local limit")
			}
			tasks = append(tasks, t)
		}
		if page.NextPage == "" {
			break
		}
		if tokens[page.NextPage] {
			return false, errors.New("snapshot pagination did not advance")
		}
		tokens[page.NextPage] = true
		page, err = p.SnapshotPage(ctx, first.SnapshotID, page.NextPage)
		if err != nil {
			return false, err
		}
	}
	if uint64(len(tasks)) != first.TotalTasks {
		return false, errors.New("incomplete task snapshot")
	}
	members := []taskproviders.Member(nil)
	if c.Capabilities.Supports("members") && hasScope(c.Scopes, "members:read") && hasScope(space.Scopes, "members:read") {
		members, err = p.Members(ctx, space.SpaceID)
		if err != nil {
			return false, err
		}
	}
	latest, err := p.Me(ctx)
	if err != nil {
		return false, err
	}
	if latest.AccountID != c.AccountID || latest.DeviceID != c.DeviceID || latest.AuthEpoch != c.AuthEpoch {
		return false, taskproviders.ErrAuthorizationChanged
	}
	if !s.now().Before(first.ExpiresAt) {
		return false, taskproviders.ErrCursorExpired
	}
	err = s.write(ctx, owner, c, func(tx *sql.Tx) error {
		// No missing task is removed until every page and directory has passed.
		prefix := c.InstanceID + "/" + space.WorkspaceID + "/" + space.SpaceID + "/"
		if _, err := tx.ExecContext(ctx, `DELETE FROM task_snapshots WHERE connection_id=? AND account_id=? AND substr(task_key,1,length(?))=?`, c.ID, c.AccountID, prefix, prefix); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM task_sync_events WHERE connection_id=? AND account_id=? AND space_id=? AND event_type='task_evaluate'`, c.ID, c.AccountID, space.SpaceID); err != nil {
			return err
		}
		for _, t := range tasks {
			if err := s.putTask(ctx, tx, c, space, t); err != nil {
				return err
			}
		}
		if members != nil {
			if err := s.directory(ctx, tx, c, space, members); err != nil {
				return err
			}
		}
		return s.cursor(ctx, tx, c, space, first.Cursor)
	})
	return len(tasks) > 0, err
}
func validateTask(c Connection, space taskproviders.Space, t taskproviders.Snapshot, full bool) error {
	if err := t.Key.Validate(); err != nil {
		return err
	}
	if t.Key.InstanceID != c.InstanceID || t.Key.WorkspaceID != space.WorkspaceID || t.Key.SpaceID != space.SpaceID {
		return errors.New("task outside synchronization space")
	}
	if full {
		if err := t.Validate(); err != nil {
			return err
		}
		if !t.Permissions.Read || t.Permissions.AuthEpoch != c.AuthEpoch {
			return taskproviders.ErrAuthorizationChanged
		}
	}
	return nil
}
func (s *Service) putTask(ctx context.Context, tx *sql.Tx, c Connection, space taskproviders.Space, t taskproviders.Snapshot) error {
	if err := validateTask(c, space, t, true); err != nil {
		return err
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_snapshots(connection_id,account_id,task_key,snapshot_json,auth_epoch,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(connection_id,account_id,task_key) DO UPDATE SET snapshot_json=excluded.snapshot_json,auth_epoch=excluded.auth_epoch,updated_at=excluded.updated_at`, c.ID, c.AccountID, t.Key.String(), string(raw), c.AuthEpoch, s.now().UnixMilli())
	if err != nil {
		return err
	}
	// Input/assignment/generation, not display-only revisions, identify work.
	return s.event(ctx, tx, c, space.SpaceID, eventID(t.Key.String(), t.InputRevision, t.AssignmentRevision, t.AutomationGeneration), t.Key.String(), "task_evaluate", raw)
}
func eventID(parts ...string) string {
	b, _ := json.Marshal(parts)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (s *Service) event(ctx context.Context, tx *sql.Tx, c Connection, space, id, key, kind string, raw []byte) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO task_sync_events(connection_id,account_id,event_id,space_id,task_key,event_type,payload_json,auth_epoch,created_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(connection_id,account_id,event_id) DO UPDATE SET payload_json=excluded.payload_json,auth_epoch=excluded.auth_epoch`, c.ID, c.AccountID, id, space, key, kind, string(raw), c.AuthEpoch, s.now().UnixMilli())
	return err
}
func (s *Service) directory(ctx context.Context, tx *sql.Tx, c Connection, space taskproviders.Space, members []taskproviders.Member) error {
	raw, err := json.Marshal(members)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_directories(connection_id,account_id,workspace_id,space_id,directory_json,auth_epoch) VALUES(?,?,?,?,?,?) ON CONFLICT(connection_id,account_id,workspace_id,space_id) DO UPDATE SET directory_json=excluded.directory_json,auth_epoch=excluded.auth_epoch`, c.ID, c.AccountID, space.WorkspaceID, space.SpaceID, string(raw), c.AuthEpoch)
	return err
}
func (s *Service) cursor(ctx context.Context, tx *sql.Tx, c Connection, space taskproviders.Space, cursor string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO task_space_cursors(connection_id,account_id,space_id,workspace_id,feed_epoch,auth_epoch,cursor,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(connection_id,account_id,space_id) DO UPDATE SET workspace_id=excluded.workspace_id,feed_epoch=excluded.feed_epoch,auth_epoch=excluded.auth_epoch,cursor=excluded.cursor,updated_at=excluded.updated_at`, c.ID, c.AccountID, space.SpaceID, space.WorkspaceID, c.FeedEpoch, c.AuthEpoch, cursor, s.now().UnixMilli())
	return err
}
func (s *Service) failed(ctx context.Context, owner string, c Connection, cause error, disable func() error) error {
	if ctx.Err() != nil || errors.Is(cause, ErrStale) {
		return cause
	}
	state, code, clear := "connected", "sync_failed", false
	delay := 30 * time.Second * time.Duration(1<<min(c.Failures, 4))
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	delay = time.Duration(float64(delay) * (0.9 + rand.Float64()*0.2))
	if c.State == "authorizing" {
		state = "authorizing"
	}
	var httpErr *tsync.HTTPError
	var oauth *tsync.OAuthError
	if errors.As(cause, &httpErr) && (httpErr.Status == 403 || httpErr.Status == 404) {
		clear = true
		code = "permissions_changed"
	}
	if errors.As(cause, &httpErr) && httpErr.RetryAfter > delay {
		delay = httpErr.RetryAfter
	}
	if errors.As(cause, &oauth) && (oauth.Code == "authorization_pending" || oauth.Code == "slow_down") {
		code = ""
		delay = max(oauth.RetryAfter, time.Second)
	}
	switch {
	case errors.Is(cause, taskproviders.ErrReconnect):
		state, code, clear = "reconnect", "authorization_required", true
		if disable != nil {
			if err := disable(); err != nil {
				cause = errors.Join(cause, err)
			}
		}
	case errors.Is(cause, taskproviders.ErrAuthorizationChanged):
		code, clear, delay = "authorization_changed", true, time.Second
	case errors.Is(cause, taskproviders.ErrUnsupported):
		state, code, clear = "unsupported", "capability_unavailable", true
	}
	err := s.write(ctx, owner, c, func(tx *sql.Tx) error {
		if clear {
			if err := clearConnection(ctx, tx, c.ID); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE task_connections SET state=?,last_error=?,next_sync_at=?,failures=failures+1,updated_at=? WHERE connection_id=?`, state, code, s.now().Add(delay).UnixMilli(), s.now().UnixMilli(), c.ID)
		return err
	})
	return errors.Join(cause, err)
}

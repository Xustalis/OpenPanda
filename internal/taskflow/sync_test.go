// SPDX-License-Identifier: AGPL-3.0-or-later
package taskflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/taskproviders/tsync"
	"github.com/Xustalis/OpenPanda/internal/testdb"
)

type syncSource struct {
	taskproviders.TaskProvider
	account  taskproviders.Account
	caps     taskproviders.Capabilities
	spaces   []taskproviders.Space
	begin    func(string) (taskproviders.SnapshotPage, error)
	page     func(string) (taskproviders.SnapshotPage, error)
	changes  func(string, string) (taskproviders.Changes, error)
	fail     error
	calls    atomic.Int64
	disabled bool
}

func (p *syncSource) Capabilities(context.Context) (taskproviders.Capabilities, error) {
	p.calls.Add(1)
	return p.caps, p.fail
}
func (p *syncSource) Me(context.Context) (taskproviders.Account, error)     { return p.account, nil }
func (p *syncSource) Spaces(context.Context) ([]taskproviders.Space, error) { return p.spaces, nil }
func (p *syncSource) Members(context.Context, string) ([]taskproviders.Member, error) {
	return []taskproviders.Member{{AccountID: "a", Name: "Person"}}, nil
}
func (p *syncSource) BeginSnapshot(_ context.Context, space string) (taskproviders.SnapshotPage, error) {
	return p.begin(space)
}
func (p *syncSource) SnapshotPage(_ context.Context, _ string, token string) (taskproviders.SnapshotPage, error) {
	return p.page(token)
}
func (p *syncSource) Changes(_ context.Context, space, _, cursor string) (taskproviders.Changes, error) {
	return p.changes(space, cursor)
}
func taskFor(space, id string) taskproviders.Snapshot {
	return taskproviders.Snapshot{Key: taskproviders.Key{InstanceID: "i", WorkspaceID: "w", SpaceID: space, TaskID: id}, Title: id, Status: "todo", TaskRevision: "r1", InputRevision: "i1", AssignmentRevision: "a1", AutomationGeneration: "1", Permissions: taskproviders.Permissions{Read: true, AuthEpoch: "1"}}
}
func newSyncTest(t *testing.T) (*Service, *syncSource) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "node.db")
	db := testdb.OpenFile(t, path)
	s := New(db, path)
	p := &syncSource{account: taskproviders.Account{AccountID: "a", DeviceID: "d", AuthEpoch: "1", Scopes: []string{"tasks:read", "members:read"}}, caps: taskproviders.Capabilities{IntegrationVersion: 1, InstanceID: "i", FeedEpoch: "f", Capabilities: []string{"taskRead", "snapshots", "changes", "members"}}, spaces: []taskproviders.Space{{WorkspaceID: "w", SpaceID: "s", Scopes: []string{"tasks:read", "members:read"}}}}
	p.begin = func(space string) (taskproviders.SnapshotPage, error) {
		return taskproviders.SnapshotPage{SnapshotID: "snap", FeedEpoch: "f", AuthEpoch: p.account.AuthEpoch, Cursor: "c1", TotalTasks: 1, ExpiresAt: time.Now().Add(time.Hour), Tasks: []taskproviders.Snapshot{taskFor(space, "t")}}, nil
	}
	p.changes = func(space, cursor string) (taskproviders.Changes, error) {
		return taskproviders.Changes{FeedEpoch: "f", AuthEpoch: p.account.AuthEpoch, Cursor: cursor}, nil
	}
	s.source = func(Connection) (taskproviders.TaskProvider, func() error, error) {
		return p, func() error { p.disabled = true; return nil }, nil
	}
	_, err := db.Exec(`INSERT INTO task_connections(connection_id,provider,root_url,credential_ref,state,updated_at) VALUES('c','tsync','https://example.test/tsync','c.json','connected',0)`)
	if err != nil {
		t.Fatal(err)
	}
	return s, p
}
func scalar(t *testing.T, db *sql.DB, q string, args ...any) string {
	t.Helper()
	var v string
	if err := db.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}
func syncOK(t *testing.T, s *Service) {
	t.Helper()
	if err := s.SyncNow(context.Background(), "c"); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotFailureRetainsCompleteView(t *testing.T) {
	s, p := newSyncTest(t)
	syncOK(t, s)
	_, err := s.db.Exec(`DELETE FROM task_space_cursors`)
	if err != nil {
		t.Fatal(err)
	}
	p.begin = func(string) (taskproviders.SnapshotPage, error) {
		return taskproviders.SnapshotPage{SnapshotID: "new", FeedEpoch: "f", AuthEpoch: "1", Cursor: "c2", TotalTasks: 2, ExpiresAt: time.Now().Add(time.Hour), NextPage: "next", Tasks: []taskproviders.Snapshot{taskFor("s", "replacement")}}, nil
	}
	p.page = func(string) (taskproviders.SnapshotPage, error) {
		return taskproviders.SnapshotPage{}, errors.New("interrupted")
	}
	if err = s.SyncNow(context.Background(), "c"); err == nil {
		t.Fatal("partial snapshot accepted")
	}
	if got := scalar(t, s.db, `SELECT task_key FROM task_snapshots`); got != "i/w/s/t" {
		t.Fatalf("old complete view lost: %s", got)
	}
	if got := scalar(t, s.db, `SELECT count(*) FROM task_space_cursors`); got != "0" {
		t.Fatalf("published partial cursor: %s", got)
	}
}
func TestSnapshotRejectsInvalidPages(t *testing.T) {
	for _, kind := range []string{"duplicate", "count", "epoch", "space", "expired", "continuation"} {
		t.Run(kind, func(t *testing.T) {
			s, p := newSyncTest(t)
			syncOK(t, s)
			_, err := s.db.Exec(`DELETE FROM task_space_cursors`)
			if err != nil {
				t.Fatal(err)
			}
			p.begin = func(string) (taskproviders.SnapshotPage, error) {
				x := taskproviders.SnapshotPage{SnapshotID: "new", FeedEpoch: "f", AuthEpoch: "1", Cursor: "c2", TotalTasks: 1, ExpiresAt: time.Now().Add(time.Hour), Tasks: []taskproviders.Snapshot{taskFor("s", "new")}}
				switch kind {
				case "duplicate":
					x.TotalTasks = 2
					x.Tasks = append(x.Tasks, x.Tasks[0])
				case "count":
					x.TotalTasks = 2
				case "epoch":
					x.AuthEpoch = "2"
				case "space":
					x.Tasks[0].Key.SpaceID = "other"
				case "expired":
					x.ExpiresAt = time.Now().Add(-time.Second)
				case "continuation":
					x.TotalTasks = 2
					x.NextPage = "again"
					p.page = func(string) (taskproviders.SnapshotPage, error) { return x, nil }
				}
				return x, nil
			}
			if err = s.SyncNow(context.Background(), "c"); err == nil {
				t.Fatalf("accepted %s", kind)
			}
			if got := scalar(t, s.db, `SELECT task_key FROM task_snapshots`); got != "i/w/s/t" {
				t.Fatalf("published %s: %s", kind, got)
			}
		})
	}
}
func TestChangePublicationRollsBackTogether(t *testing.T) {
	s, p := newSyncTest(t)
	syncOK(t, s)
	_, err := s.db.Exec(`CREATE TRIGGER reject_event BEFORE INSERT ON task_sync_events BEGIN SELECT RAISE(ABORT,'fault'); END`)
	if err != nil {
		t.Fatal(err)
	}
	p.changes = func(string, string) (taskproviders.Changes, error) {
		return taskproviders.Changes{FeedEpoch: "f", AuthEpoch: "1", Cursor: "c2", Tasks: []taskproviders.Snapshot{taskFor("s", "new")}, Removed: []taskproviders.Key{taskFor("s", "t").Key}}, nil
	}
	if err = s.SyncNow(context.Background(), "c"); err == nil {
		t.Fatal("fault ignored")
	}
	if got := scalar(t, s.db, `SELECT task_key FROM task_snapshots`); got != "i/w/s/t" {
		t.Fatal(got)
	}
	if got := scalar(t, s.db, `SELECT cursor FROM task_space_cursors`); got != "c1" {
		t.Fatal(got)
	}
}
func TestProjectRebootstrapPreservesFactsAndCursorRecovery(t *testing.T) {
	s, p := newSyncTest(t)
	syncOK(t, s)
	p.changes = func(string, string) (taskproviders.Changes, error) {
		return taskproviders.Changes{FeedEpoch: "f", AuthEpoch: "1", Cursor: "c2", NeedsSnapshot: true, Events: []taskproviders.ChangeEvent{{Type: "delivery_updated", Data: json.RawMessage(`{"id":"delivery"}`)}}}, nil
	}
	p.begin = func(string) (taskproviders.SnapshotPage, error) {
		return taskproviders.SnapshotPage{}, errors.New("snapshot offline")
	}
	if err := s.SyncNow(context.Background(), "c"); err == nil {
		t.Fatal("expected bootstrap failure")
	}
	if got := scalar(t, s.db, `SELECT count(*) FROM task_sync_events WHERE event_type='delivery_updated'`); got != "1" {
		t.Fatal("lost fact", got)
	}
	if got := scalar(t, s.db, `SELECT count(*) FROM task_space_cursors`); got != "0" {
		t.Fatal("must retry bootstrap", got)
	}
	p.begin = func(string) (taskproviders.SnapshotPage, error) {
		return taskproviders.SnapshotPage{SnapshotID: "new", FeedEpoch: "f", AuthEpoch: "1", Cursor: "c3", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	syncOK(t, s)
	if got := scalar(t, s.db, `SELECT count(*) FROM task_sync_events WHERE event_type='delivery_updated'`); got != "1" {
		t.Fatal("bootstrap deleted fact", got)
	}
	if got := scalar(t, s.db, `SELECT cursor FROM task_space_cursors`); got != "c3" {
		t.Fatal(got)
	}
}
func TestCursorExpiredPerSpaceAndRevocation(t *testing.T) {
	s, p := newSyncTest(t)
	p.spaces = append(p.spaces, taskproviders.Space{WorkspaceID: "w", SpaceID: "s2", Scopes: []string{"tasks:read"}})
	syncOK(t, s)
	seen := map[string]string{}
	p.changes = func(space, cursor string) (taskproviders.Changes, error) {
		seen[space] = cursor
		if space == "s" {
			return taskproviders.Changes{}, taskproviders.ErrCursorExpired
		}
		return taskproviders.Changes{FeedEpoch: "f", AuthEpoch: "1", Cursor: "space2"}, nil
	}
	syncOK(t, s)
	if len(seen) != 2 {
		t.Fatalf("spaces not independent: %#v", seen)
	}
	if got := scalar(t, s.db, `SELECT cursor FROM task_space_cursors WHERE space_id='s2'`); got != "space2" {
		t.Fatal(got)
	}
	p.spaces = p.spaces[1:]
	syncOK(t, s)
	if got := scalar(t, s.db, `SELECT count(*) FROM task_snapshots WHERE task_key='i/w/s/t'`); got != "0" {
		t.Fatal("revoked space retained", got)
	}
}
func TestReconnectStopsRetriesAndClearsAuthorization(t *testing.T) {
	s, p := newSyncTest(t)
	syncOK(t, s)
	p.fail = &tsync.HTTPError{Status: 401}
	if err := s.SyncNow(context.Background(), "c"); !errors.Is(err, taskproviders.ErrReconnect) {
		t.Fatal(err)
	}
	if !p.disabled {
		t.Fatal("credential still enabled")
	}
	calls := p.calls.Load()
	if err := s.SyncNow(context.Background(), "c"); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	if p.calls.Load() != calls {
		t.Fatal("retried after 401")
	}
	if got := scalar(t, s.db, `SELECT count(*) FROM task_snapshots`); got != "0" {
		t.Fatal(got)
	}
}
func TestLostGenerationAndLeaseCannotPublish(t *testing.T) {
	for _, kind := range []string{"generation", "lease"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := newSyncTest(t)
			err := s.withOwner(context.Background(), false, func(ctx context.Context, owner string) error {
				c, e := s.Get(ctx, "c")
				if e != nil {
					return e
				}
				q := `UPDATE task_connections SET generation=generation+1 WHERE connection_id='c'`
				if kind == "lease" {
					q = `UPDATE task_runtime_leases SET owner_id='successor'`
				}
				if _, e = s.db.Exec(q); e != nil {
					return e
				}
				return s.write(ctx, owner, c, func(tx *sql.Tx) error {
					_, e := tx.Exec(`INSERT INTO task_space_cursors VALUES('c','a','s','w','f','1','late',0)`)
					return e
				})
			})
			if !errors.Is(err, ErrStale) {
				t.Fatal(err)
			}
			if got := scalar(t, s.db, `SELECT count(*) FROM task_space_cursors`); got != "0" {
				t.Fatal("stale write committed")
			}
		})
	}
}
func TestRuntimeContentionAndShutdown(t *testing.T) {
	s, _ := newSyncTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.withOwner(ctx, false, func(ctx context.Context, _ string) error { close(ready); <-ctx.Done(); return ctx.Err() })
	}()
	<-ready
	if err := s.SyncNow(context.Background(), "c"); !errors.Is(err, ErrRuntimeHeld) {
		t.Fatalf("second owner admitted: %v", err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("owner failed to stop")
	}
	if got := scalar(t, s.db, `SELECT count(*) FROM task_runtime_leases`); got != "0" {
		t.Fatal("lease retained", got)
	}
	syncOK(t, s)
}

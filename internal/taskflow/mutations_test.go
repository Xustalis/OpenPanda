// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
)

type writerSource struct {
	*syncSource
	writes int
	mutate func()
}

func (p *writerSource) CreateTask(_ context.Context, in taskproviders.Snapshot, _ string) (taskproviders.Snapshot, error) {
	p.writes++
	if p.mutate != nil {
		p.mutate()
	}
	in.Key.TaskID = "created"
	in.TaskRevision = "new"
	return in, nil
}
func (p *writerSource) PatchTask(_ context.Context, key taskproviders.Key, _ string, _ json.RawMessage, _ string) (taskproviders.Snapshot, error) {
	p.writes++
	if p.mutate != nil {
		p.mutate()
	}
	out := taskFor(key.SpaceID, key.TaskID)
	out.TaskRevision = "new"
	return out, nil
}
func mutationService(t *testing.T) (*Service, *writerSource) {
	t.Helper()
	s, source := newSyncTest(t)
	source.account.Scopes = append(source.account.Scopes, "tasks:write")
	source.spaces[0].Scopes = append(source.spaces[0].Scopes, "tasks:write")
	source.caps.Capabilities = append(source.caps.Capabilities, "taskWrite")
	syncOK(t, s)
	writer := &writerSource{syncSource: source}
	s.source = func(Connection) (taskproviders.TaskProvider, func() error, error) {
		return writer, func() error { return nil }, nil
	}
	return s, writer
}
func TestRemoteMutationsRequestSyncWithoutEditingCache(t *testing.T) {
	s, p := mutationService(t)
	ctx := context.Background()
	original := scalar(t, s.db, `SELECT snapshot_json FROM task_snapshots`)
	if _, err := s.CreateTask(ctx, "c", "s", taskproviders.Snapshot{Title: "new goal", Priority: 2}, "create"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PatchTask(ctx, "c", taskFor("s", "t").Key, "r1", []byte(`{"title":"edited"}`), "edit"); err != nil {
		t.Fatal(err)
	}
	if p.writes != 2 {
		t.Fatal(p.writes)
	}
	if got := scalar(t, s.db, `SELECT snapshot_json FROM task_snapshots`); got != original {
		t.Fatal("mutation response replaced authoritative feed cache")
	}
	if got := scalar(t, s.db, `SELECT requested_at>0 AND next_sync_at=0 FROM task_connections`); got != "1" {
		t.Fatal("sync not requested")
	}
	if got := scalar(t, s.db, `SELECT count(*) FROM todos`); got != "0" {
		t.Fatal("remote task copied to editable todo")
	}
}
func TestRemoteMutationsRecheckSubjectAndSpace(t *testing.T) {
	for _, kind := range []string{"account", "scope", "space", "capability", "concurrent-revoke"} {
		t.Run(kind, func(t *testing.T) {
			s, p := mutationService(t)
			switch kind {
			case "account":
				p.account.AccountID = "other"
			case "scope":
				p.account.Scopes = []string{"tasks:read"}
			case "space":
				p.spaces = nil
			case "capability":
				p.caps.Capabilities = []string{"taskRead"}
			case "concurrent-revoke":
				p.mutate = func() {
					if _, err := s.db.Exec(`UPDATE task_connections SET spaces_json='[]'`); err != nil {
						t.Fatal(err)
					}
				}
			}
			_, err := s.PatchTask(context.Background(), "c", taskFor("s", "t").Key, "r1", []byte(`{"title":"new"}`), "edit")
			if !errors.Is(err, taskproviders.ErrAuthorizationChanged) && !errors.Is(err, taskproviders.ErrUnsupported) {
				t.Fatal(err)
			}
			want := 0
			if kind == "concurrent-revoke" {
				want = 1
			}
			if p.writes != want {
				t.Fatalf("writes=%d want=%d", p.writes, want)
			}
		})
	}
}

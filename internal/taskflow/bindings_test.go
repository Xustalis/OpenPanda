// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"slices"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/taskproviders"
)

func TestExplicitProjectBindingStopsOldAuthority(t *testing.T) {
	x := completionTest(t, taskproviders.BeforeSubmit)
	ctx := context.Background()
	if _, err := projects.NewStore(x.s.db).Create("local-context", "", ""); err != nil {
		t.Fatal(err)
	}
	if b, err := x.s.Bindings(ctx, x.c.ID, "s"); err != nil || len(b) != 0 {
		t.Fatalf("default=%+v %v", b, err)
	}
	if err := x.s.SetBinding(ctx, x.c.ID, "s", ProjectBinding{RemoteProject: "remote", LocalProject: "missing"}); err == nil {
		t.Fatal("missing local project accepted")
	}
	if err := x.s.SetBinding(ctx, x.c.ID, "s", ProjectBinding{RemoteProject: "remote", LocalProject: "local-context"}); err != nil {
		t.Fatal(err)
	}
	b, err := x.s.Bindings(ctx, x.c.ID, "s")
	if err != nil || len(b) != 1 || b[0].LocalProject != "local-context" {
		t.Fatalf("bindings=%+v %v", b, err)
	}
	if state := scalar(t, x.s.db, `SELECT state FROM task_flow_runs`); state != "paused" {
		t.Fatalf("old authority=%s", state)
	}
	if err := x.s.SetBinding(ctx, x.c.ID, "s", b[0]); err != nil {
		t.Fatal(err)
	}
	if err := x.s.SetBinding(ctx, x.c.ID, "s", ProjectBinding{RemoteProject: "remote"}); err != nil {
		t.Fatal(err)
	}
	if b, err = x.s.Bindings(ctx, x.c.ID, "s"); err != nil || len(b) != 0 {
		t.Fatalf("removed=%+v %v", b, err)
	}
	if _, err = x.s.db.Exec(`UPDATE task_connections SET spaces_json='[]'`); err != nil {
		t.Fatal(err)
	}
	if _, err = x.s.Bindings(ctx, x.c.ID, "s"); err == nil {
		t.Fatal("removed space read")
	}
	if err = x.s.SetBinding(ctx, x.c.ID, "s", ProjectBinding{LocalProject: "local-context"}); err == nil {
		t.Fatal("removed space write")
	}
}

func TestBoundProjectMemoryIsExplicitAndAccountScoped(t *testing.T) {
	s, e, c, _, _ := automationTest(t, true)
	ctx := context.Background()
	f := flowRun{ConnectionID: c.ID, AccountID: c.AccountID, Snapshot: taskproviders.Snapshot{Key: taskproviders.Key{InstanceID: "i", WorkspaceID: "w", SpaceID: "s", TaskID: "t"}, ProjectID: "same-name"}}
	var loaded []string
	e.options.ProjectMemory = func(name string) ([]string, error) {
		loaded = append(loaded, name)
		return []string{name + " memory"}, nil
	}
	for _, name := range []string{"same-name", "space-context", "project-context"} {
		if _, err := projects.NewStore(s.db).Create(name, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want string) {
		t.Helper()
		loaded = nil
		entries, err := e.boundProjectMemory(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		if want == "" {
			if len(loaded) != 0 || len(entries) != 0 {
				t.Fatalf("implicit memory: %v %v", loaded, entries)
			}
		} else if !slices.Equal(loaded, []string{want}) || !slices.Equal(entries, []string{want + " memory"}) {
			t.Fatalf("memory: %v %v want %s", loaded, entries, want)
		}
	}
	check("")
	if err := s.SetBinding(ctx, c.ID, "s", ProjectBinding{LocalProject: "space-context"}); err != nil {
		t.Fatal(err)
	}
	check("space-context")
	if err := s.SetBinding(ctx, c.ID, "s", ProjectBinding{RemoteProject: "same-name", LocalProject: "project-context"}); err != nil {
		t.Fatal(err)
	}
	check("project-context")
	f.AccountID = "another-account"
	check("")
	f.AccountID = c.AccountID
	f.Snapshot.ProjectID = "other-project"
	check("space-context")
	f.Snapshot.ProjectID = ""
	check("space-context")
}

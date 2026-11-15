// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/ledger"
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

func TestExecutionEnqueueRetainsBoundProject(t *testing.T) {
	s, e, c, _, wire := automationTest(t, true)
	ctx := context.Background()
	if _, err := projects.NewStore(s.db).Create("execution-context", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBinding(ctx, c.ID, "s", ProjectBinding{LocalProject: "execution-context"}); err != nil {
		t.Fatal(err)
	}
	e.core = core.NewCore(s.db, "owner", ledger.Card{}, 5, nil, config.ModelConfig{})
	e.core.SetWorkDir(t.TempDir())
	f := discoverTestFlow(t, s, e)
	automationServer(t, s, c, wire, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tsync/v1/integration/tasks/t/executions", "/tsync/v1/integration/executions/execution/heartbeat":
			_ = json.NewEncoder(w).Encode(leaseRecord(f))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	runCtx, cancel := context.WithCancel(ctx)
	m := &managedExecution{runtime: e, id: f.ID, owner: "owner", ctx: runCtx, cancel: cancel, ready: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); m.coordinate() }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("coordinator failed to stop")
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var project string
		err := s.db.QueryRowContext(ctx, "SELECT t.project FROM execution_links l JOIN tasks t ON t.task_id=l.task_id WHERE l.request_id=?", f.ID).Scan(&project)
		if err == nil {
			if project != "execution-context" {
				t.Fatalf("persisted execution project=%q", project)
			}
			return
		}
		select {
		case <-done:
			t.Fatalf("coordinator stopped before publication: %v", m.acquireErr)
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatal("bound execution was not published")
}

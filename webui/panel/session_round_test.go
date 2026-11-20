// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

func TestSessionRoundStopScopesWorkToTheGoal(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	sess := sessions.NewStore(t.TempDir())
	if err := store.BeginAskRound(ctx, "goal", "owner", "en"); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginAskRound(ctx, "separate", "owner", "en"); err != nil {
		t.Fatal(err)
	}
	create := func(ctx context.Context, parent string) core.Task {
		t.Helper()
		task, err := store.Create(ctx, parent, "project", "job", "node", []string{"node"})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Queue(ctx, task.TaskID, "node"); err != nil {
			t.Fatal(err)
		}
		return task
	}
	root := create(core.WithAskRound(ctx, "goal"), "")
	child := create(ctx, root.TaskID)
	other := create(core.WithAskRound(ctx, "separate"), "")

	h := New(Deps{Store: store, Sessions: sess, StaticDir: t.TempDir(), Token: testToken})
	request := func(method, path string, status int, body string) {
		t.Helper()
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, authedReq(method, path, nil))
		if rr.Code != status || (body != "" && !strings.Contains(rr.Body.String(), body)) {
			t.Fatalf("%s %s: got %d %s; want %d containing %q", method, path, rr.Code, rr.Body, status, body)
		}
	}
	request(http.MethodGet, "/api/sessions/owner/rounds/goal", http.StatusOK, "running")
	request(http.MethodPost, "/api/sessions/other/rounds/goal/stop", http.StatusNotFound, "")
	request(http.MethodPost, "/api/sessions/owner/rounds/goal/stop", http.StatusOK, "stopped")
	request(http.MethodPost, "/api/sessions/owner/rounds/goal/stop", http.StatusOK, "stopped")
	request(http.MethodGet, "/api/sessions/owner/rounds/goal", http.StatusOK, "interrupted")
	request(http.MethodGet, "/api/sessions/owner/rounds/separate", http.StatusOK, "running")
	for id, want := range map[string]string{
		root.TaskID: core.StateCancelled, child.TaskID: core.StateCancelled, other.TaskID: core.StateQueued,
	} {
		task, err := store.Get(ctx, id)
		if err != nil || task.State != want {
			t.Fatalf("task %s: got %s, %v; want %s", id, task.State, err, want)
		}
	}
	if _, err := store.Create(core.WithAskRound(ctx, "goal"), "", "", "late", "node", []string{"node"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("goal accepted dispatch after being stopped: %v", err)
	}
}

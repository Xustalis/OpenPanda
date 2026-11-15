// SPDX-License-Identifier: AGPL-3.0-or-later

package tsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
)

func TestHTTPPrefixAuthorizationAndRedirectBoundary(t *testing.T) {
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tsync/v1/integration/me" || r.Header.Get("Authorization") != "Bearer synthetic-access" {
			t.Error("wrong request boundary", r.URL.Path)
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	c, err := New(server.URL + "/tsync/")
	if err != nil {
		t.Fatal(err)
	}
	c.HTTP = server.Client() // A supplied client must not restore redirect following.
	c.Token = func(context.Context) (string, error) { return "synthetic-access", nil }
	_, err = c.Me(context.Background())
	var e *HTTPError
	if !errors.As(err, &e) || e.Status != 307 || leaked.Load() != 0 {
		t.Fatalf("redirect boundary: %v, requests %d", err, leaked.Load())
	}
}

func TestHTTPStatusRetryAndPreconditions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tsync/v1/integration/tasks/task" || r.Header.Get("Idempotency-Key") != "mutation" || r.Header.Get("If-Match") != `"r1"` {
			t.Error("lost prefix or write preconditions")
		}
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","description":"never expose echoed credentials"}}`))
	}))
	defer server.Close()
	c, err := New(server.URL + "/tsync")
	if err != nil {
		t.Fatal(err)
	}
	err = c.request(context.Background(), "PATCH", "v1/integration/tasks/task", nil, map[string]string{"title": "new"}, "mutation", "r1", nil)
	var e *HTTPError
	if !errors.As(err, &e) || e.RetryAfter != 7*time.Second || strings.Contains(err.Error(), "credentials") {
		t.Fatal(err)
	}
	for status, sentinel := range map[int]error{401: taskproviders.ErrReconnect, 409: taskproviders.ErrConflict, 410: taskproviders.ErrCursorExpired} {
		if !errors.Is(&HTTPError{Status: status}, sentinel) {
			t.Fatal(status)
		}
	}
}

func TestProviderChangesPreserveLastOccurrenceAndEpoch(t *testing.T) {
	task := fixtureTask()
	task.Status = "todo"
	task.Permissions.Read = true
	upsert, _ := json.Marshal(task)
	tombstone, _ := json.Marshal(task.Key)
	var epoch uint64 = 1
	var reverse bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tsync/v1/integration/spaces/space/changes" {
			t.Error(r.URL.Path)
		}
		data := []Change{{Type: "task_tombstone", Data: tombstone}, {Type: "task_upsert", Data: upsert}}
		if reverse {
			data[0], data[1] = data[1], data[0]
		}
		_ = json.NewEncoder(w).Encode(Changes{AuthEpoch: epoch, Cursor: "next", Changes: data})
	}))
	defer server.Close()
	c, err := New(server.URL + "/tsync")
	if err != nil {
		t.Fatal(err)
	}
	p := NewProvider(c)
	p.caps = Capabilities{IntegrationVersion: 1, InstanceID: "instance", FeedEpoch: "feed", MinimumVersion: "0.1.0", Features: map[string]bool{"changes": true, "taskRead": true}}
	p.me.AuthEpoch = 1
	p.me.Account.ID = "account"
	p.spaces["space"] = Space{SpaceID: "space", WorkspaceID: "workspace"}
	v, err := p.Changes(context.Background(), "space", "feed", "old")
	if err != nil || len(v.Tasks) != 1 || len(v.Removed) != 0 || len(v.Events) != 2 {
		t.Fatalf("recreate: %+v %v", v, err)
	}
	reverse = true
	v, err = p.Changes(context.Background(), "space", "feed", "old")
	if err != nil || len(v.Tasks) != 0 || len(v.Removed) != 1 {
		t.Fatalf("delete: %+v %v", v, err)
	}
	epoch = 2
	if _, err = p.Changes(context.Background(), "space", "feed", "old"); !errors.Is(err, taskproviders.ErrAuthorizationChanged) {
		t.Fatal(err)
	}
}

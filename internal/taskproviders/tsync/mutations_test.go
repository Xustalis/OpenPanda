// SPDX-License-Identifier: AGPL-3.0-or-later

package tsync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
)

func writerTest(t *testing.T, handle http.HandlerFunc) *Provider {
	t.Helper()
	server := httptest.NewServer(handle)
	t.Cleanup(server.Close)
	c, err := New(server.URL + "/tsync/")
	if err != nil {
		t.Fatal(err)
	}
	p := NewProvider(c)
	p.caps = Capabilities{IntegrationVersion: 1, InstanceID: "instance", FeedEpoch: "feed", MinimumVersion: "0.1.0", Features: map[string]bool{"taskWrite": true}}
	sp := Space{SpaceID: "space", WorkspaceID: "workspace", Name: "Space", Scopes: []string{"tasks:read", "tasks:write"}}
	p.spaces[sp.SpaceID] = sp
	p.me.Account.ID = "account"
	p.me.AuthEpoch = 1
	p.me.Scopes = sp.Scopes
	p.me.Spaces = []Space{sp}
	return p
}
func TestTaskWriterExactWireAndReplay(t *testing.T) {
	wire := fixtureTask()
	wire.Permissions.Read = true
	var bodies []string
	p := writerTest(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if r.URL.Path != "/tsync/v1/integration/tasks" && r.URL.Path != "/tsync/v1/integration/tasks/"+wire.Key.TaskID {
			t.Error(r.URL.Path)
		}
		if r.Header.Get("Idempotency-Key") != "command" {
			t.Error("missing idempotency key")
		}
		if r.Method == "PATCH" && r.Header.Get("If-Match") != `"opaque:revision"` {
			t.Error("revision lost")
		}
		_ = json.NewEncoder(w).Encode(wire)
	})
	input := taskproviders.Snapshot{Key: wire.Key, Title: "稿件 <>&", Description: "exact body", Priority: 4, ProjectID: "project", DueAt: &taskproviders.TypedTime{Kind: "floating_datetime", Value: "2026-11-20T10:30:00"}}
	for range 2 {
		got, err := p.CreateTask(context.Background(), input, "command")
		if err != nil || got.Key != wire.Key {
			t.Fatalf("create=%+v %v", got, err)
		}
	}
	if bodies[0] != bodies[1] || !strings.Contains(bodies[0], `"kind":"floating","raw":"2026-11-20T10:30:00","timezone":null`) || !strings.Contains(bodies[0], "稿件 <>&") {
		t.Fatal(bodies)
	}
	raw := json.RawMessage(`{"description":"exact patch","priority":0,"projectId":null,"dueAt":{"kind":"date","value":"2026-11-21"}}`)
	if _, err := p.PatchTask(context.Background(), wire.Key, "opaque:revision", raw, "command"); err != nil {
		t.Fatal(err)
	}
	var patch map[string]json.RawMessage
	if err := json.Unmarshal([]byte(bodies[2]), &patch); err != nil {
		t.Fatal(err)
	}
	if string(patch["body"]) != `"exact patch"` || string(patch["priority"]) != "0" || string(patch["projectId"]) != "null" || patch["description"] != nil {
		t.Fatal(string(bodies[2]))
	}
	if _, err := p.PatchTask(context.Background(), wire.Key, "opaque:revision", []byte(`{"projectId":""}`), "command"); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(bodies[3]), &patch); err != nil || string(patch["projectId"]) != "null" {
		t.Fatalf("project removal=%s err=%v", bodies[3], err)
	}
}
func TestTaskWriterRejectsProtectedFieldsAndLostAuthority(t *testing.T) {
	calls := 0
	p := writerTest(t, func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) })
	k := fixtureTask().Key
	for _, body := range []string{`null`, `[]`, `{}`, `{} {}`, `{"authorized":true}`, `{"status":"done"}`, `{"approvalMode":"after_execution"}`, `{"assignees":["other"]}`, `{"priority":5}`, `{"title":null}`, `{"dueAt":{"kind":"date","value":"2026-11-21","authorize":true}}`} {
		if _, err := p.PatchTask(context.Background(), k, "r", []byte(body), "command"); err == nil {
			t.Fatal("accepted", body)
		}
	}
	for _, tc := range []struct{ command, revision string }{{"", "r"}, {"command", ""}, {"command", "bad\nrevision"}} {
		if _, err := p.PatchTask(context.Background(), k, tc.revision, []byte(`{"title":"x"}`), tc.command); !errors.Is(err, taskproviders.ErrPrecondition) {
			t.Fatal(err)
		}
	}
	p.caps.Features["taskWrite"] = false
	if _, err := p.PatchTask(context.Background(), k, "r", []byte(`{"title":"x"}`), "c"); !errors.Is(err, taskproviders.ErrUnsupported) {
		t.Fatal(err)
	}
	p.caps.Features["taskWrite"] = true
	p.me.Spaces = nil
	if _, err := p.PatchTask(context.Background(), k, "r", []byte(`{"title":"x"}`), "c"); !errors.Is(err, taskproviders.ErrAuthorizationChanged) {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("invalid commands sent %d writes", calls)
	}
}
func TestTaskMutationNeverAcceptsAnotherTask(t *testing.T) {
	wire := fixtureTask()
	wire.Permissions.Read = true
	p := writerTest(t, func(w http.ResponseWriter, r *http.Request) {
		wire.Key.TaskID = "other"
		_ = json.NewEncoder(w).Encode(wire)
	})
	if _, err := p.PatchTask(context.Background(), fixtureTask().Key, "r", []byte(`{"title":"x"}`), "c"); err == nil {
		t.Fatal("accepted another task response")
	}
}

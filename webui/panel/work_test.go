// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/taskhub"
)

func TestWorkMutationHTTPContracts(t *testing.T) {
	db := newTestDB(t)
	store := core.NewTaskStore(db, nil)
	h := New(Deps{Store: store, DB: db, Token: testToken, StaticDir: t.TempDir()})
	do := func(method, path, body, key, revision string) *httptest.ResponseRecorder {
		t.Helper()
		r := authedReq(method, path, strings.NewReader(body))
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		if revision != "" {
			r.Header.Set("If-Match", revision)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := do("POST", "/api/work/tasks", `{"title":"one"}`, "", ""); w.Code != 428 {
		t.Fatalf("missing key: %d %s", w.Code, w.Body)
	}
	for _, body := range []string{`{"title":"one","authorized":true}`, `{"title":"one"} {}`, `[]`} {
		if w := do("POST", "/api/work/tasks", body, "invalid", ""); w.Code != 400 {
			t.Fatalf("invalid JSON: %d %s", w.Code, w.Body)
		}
	}
	first := do("POST", "/api/work/tasks", `{"title":"one"}`, "create-1", "")
	if first.Code != 200 {
		t.Fatalf("create: %d %s", first.Code, first.Body)
	}
	var task taskhub.Task
	if err := json.Unmarshal(first.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	path := "/api/work/tasks/" + url.PathEscape(task.Snapshot.Key.String())
	revision := first.Header().Get("ETag")
	if revision == "" {
		t.Fatal("missing create ETag")
	}
	if w := do("PATCH", path, `{"title":"two"}`, "patch-1", ""); w.Code != 428 {
		t.Fatalf("missing CAS: %d %s", w.Code, w.Body)
	}
	changed := do("PATCH", path, `{"title":"two"}`, "patch-1", revision)
	if changed.Code != 200 || changed.Header().Get("ETag") == revision {
		t.Fatalf("edit: %d %s", changed.Code, changed.Body)
	}
	if w := do("PATCH", path, `{"title":"two"}`, "patch-1", revision); w.Code != 200 || w.Body.String() != changed.Body.String() {
		t.Fatalf("replay: %d %s", w.Code, w.Body)
	}
	if w := do("PATCH", path, `{"title":"other"}`, "patch-1", revision); w.Code != 409 {
		t.Fatalf("changed body: %d %s", w.Code, w.Body)
	}
	if w := do("PATCH", path, `{"title":"stale"}`, "patch-2", revision); w.Code != 409 {
		t.Fatalf("stale CAS: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/work/tasks", `{"title":"one"}`, "create-1", ""); w.Code != 200 || w.Body.String() != first.Body.String() {
		t.Fatalf("creation replay after edit: %d %s", w.Code, w.Body)
	}
	if w := do("GET", path+"/history", "", "", ""); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("empty history: %d %s", w.Code, w.Body)
	}
	if w := do("POST", path+"/run", "{}", "", ""); w.Code != 428 {
		t.Fatalf("run without key: %d %s", w.Code, w.Body)
	}
	if w := do("POST", path+"/run", `{"requestId":"different"}`, "header-key", ""); w.Code != 409 {
		t.Fatalf("run conflicting keys: %d %s", w.Code, w.Body)
	}
}

// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/taskflow"
	"github.com/Xustalis/OpenPanda/internal/taskproviders"
)

func workflowHTTP(t *testing.T) (*sql.DB, http.Handler) {
	t.Helper()
	db := newTestDB(t)
	snapshot, err := json.Marshal(taskproviders.Snapshot{Key: taskproviders.Key{InstanceID: "i", WorkspaceID: "w", SpaceID: "s", TaskID: "t"}, Title: "Remote goal", InputRevision: "input", Permissions: taskproviders.Permissions{Read: true, AuthEpoch: "auth"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO task_connections(connection_id,provider,account_id,instance_id,auth_epoch,state,spaces_json,scopes_json,updated_at) VALUES('c','tsync','a','i','auth','connected','[{"spaceId":"s","workspaceId":"w","scopes":["tasks:read"]}]','["tasks:read"]',1)`,
		`INSERT INTO task_flow_runs(request_id,connection_id,account_id,task_key,trigger_key,auth_epoch,connection_generation,input_revision,assignment_revision,automation_generation,snapshot_json,policy_json,claim_json,claim_id,prepared_json,created_at,updated_at) VALUES('request','c','a','i/w/s/t','trigger','auth',1,'input','assignment','generation','{}','{"mode":"before_submit"}','{}','claim','Private prepared result',1,1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO task_snapshots(connection_id,account_id,task_key,snapshot_json,auth_epoch,updated_at) VALUES('c','a','i/w/s/t',?,'auth',1)`, string(snapshot)); err != nil {
		t.Fatal(err)
	}
	return db, New(Deps{DB: db, Store: core.NewTaskStore(db, nil), TaskFlow: taskflow.New(db, ""), Token: testToken, StaticDir: t.TempDir()})
}

func TestWorkflowHTTPExplicitBindings(t *testing.T) {
	db, h := workflowHTTP(t)
	if _, err := projects.NewStore(db).Create("local-context", "", ""); err != nil {
		t.Fatal(err)
	}
	do := func(method, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, authedReq(method, "/api/work/connections/c/spaces/s/bindings", strings.NewReader(body)))
		return w
	}
	if w := do("GET", ""); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("default=%d %s", w.Code, w.Body)
	}
	for _, raw := range []string{"null", "[]", `{} {}`, `{"authorized":true}`} {
		if w := do("PUT", raw); w.Code != 400 {
			t.Fatalf("strict=%d %s", w.Code, w.Body)
		}
	}
	if w := do("PUT", `{"remoteProjectId":"remote","localProject":"local-context"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"local-context"`) {
		t.Fatalf("set=%d %s", w.Code, w.Body)
	}
	if _, err := db.Exec(`UPDATE task_connections SET spaces_json='[]'`); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "PUT"} {
		if w := do(method, `{"remoteProjectId":"remote","localProject":"local-context"}`); w.Code != 403 || strings.Contains(w.Body.String(), `"local-context"`) {
			t.Fatalf("revoked=%d %s", w.Code, w.Body)
		}
	}
}

func TestWorkflowHTTPRefreshExportAndRevocation(t *testing.T) {
	db, h := workflowHTTP(t)
	do := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, authedReq("GET", path, nil))
		return w
	}
	for _, path := range []string{"/api/work/runs", "/api/work/runs/request", "/api/work/runs/request/export"} {
		w := do(path)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		if path != "/api/work/runs" && !strings.Contains(w.Body.String(), "Private prepared result") {
			t.Fatalf("missing persisted result: %s", w.Body)
		}
	}
	if w := do("/api/work/runs/request/unsupported"); w.Code != 404 {
		t.Fatalf("format: %d", w.Code)
	}
	if _, err := db.Exec(`UPDATE task_connections SET spaces_json='[]'`); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/work/runs/request", "/api/work/runs/request/export", "/api/work/connections/c/spaces/s/policy"} {
		w := do(path)
		if w.Code != 403 || strings.Contains(w.Body.String(), "Private prepared result") {
			t.Fatalf("revoked %s: %d %s", path, w.Code, w.Body)
		}
	}
	if w := do("/api/work/runs"); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("revoked list: %d %s", w.Code, w.Body)
	}
}

func TestWorkflowHTTPStrictDecisionsAndPolicy(t *testing.T) {
	db, h := workflowHTTP(t)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, authedReq(method, path, strings.NewReader(body)))
		return w
	}
	for _, path := range []string{"/api/work/actions/action/decisions", "/api/work/deliveries/delivery/decisions", "/api/work/runs/request/pause"} {
		for _, body := range []string{"null", "[]", "{} {}", `{"authorized":true}`, "{}" + strings.Repeat(" ", 1<<20)} {
			if w := do("POST", path, body); w.Code != 400 {
				t.Fatalf("strict %s %q: %d", path, body[:min(40, len(body))], w.Code)
			}
		}
	}
	policy := "/api/work/connections/c/spaces/s/policy"
	if w := do("GET", policy, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"execute":false`) {
		t.Fatalf("default: %d %s", w.Code, w.Body)
	}
	for _, body := range []string{"null", `{"execute":true}`, `{"shell":true}`} {
		if w := do("PUT", policy, body); w.Code != 400 {
			t.Fatalf("policy: %d %s", w.Code, w.Body)
		}
	}
	if w := do("PUT", policy, `{"research":false,"execute":false,"finalAction":false}`); w.Code != 200 {
		t.Fatalf("disabled policy: %d %s", w.Code, w.Body)
	}
	for range 2 {
		if w := do("POST", "/api/work/runs/request/pause", "{}"); w.Code != 200 {
			t.Fatalf("pause: %d %s", w.Code, w.Body)
		}
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM task_flow_runs WHERE request_id='request'`).Scan(&state); err != nil || state != "paused" {
		t.Fatalf("state=%s err=%v", state, err)
	}
}

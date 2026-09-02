package panel

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/memory"
	projectstore "github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// projectHandler builds a panel handler with the project metadata table attached
// — the console's project surface is only more than a list of names when it is.
func projectHandler(t *testing.T) (http.Handler, *projectstore.Store, *memory.Projects) {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ps := projectstore.NewStore(db)
	mem := memory.NewProjects(t.TempDir())
	h := New(Deps{
		Store:        core.NewTaskStore(db, slog.New(slog.DiscardHandler)),
		DB:           db,
		Projects:     mem,
		ProjectStore: ps,
		StaticDir:    t.TempDir(),
		Token:        testToken,
	})
	return h, ps, mem
}

// TestProjectCRUDAndActive walks the surface the console gained: create with
// metadata, read it back, rename, enter, exit, remove.
func TestProjectCRUDAndActive(t *testing.T) {
	h, ps, _ := projectHandler(t)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/projects",
		strings.NewReader(`{"name":"demo","work_dir":"/tmp/demo","description":"d"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("create = %d, body %s", rr.Code, rr.Body.String())
	}
	var created projectView
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal create: %v", err)
	}
	// Creating enters by default, the way `panda project new` does: a project you
	// are not in is almost never what was meant.
	if !created.Active || created.Name != "demo" || created.Description != "d" {
		t.Fatalf("created = %+v", created)
	}
	if name, _ := ps.Active(); name != "demo" {
		t.Fatalf("active = %q, want demo", name)
	}

	// The listing carries the metadata and says which one is current.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/projects", nil))
	var list struct {
		Projects []string      `json:"projects"`
		Detail   []projectView `json:"detail"`
		Active   string        `json:"active"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if list.Active != "demo" || len(list.Detail) != 1 || !list.Detail[0].Active {
		t.Fatalf("list = %+v", list)
	}

	// Rename moves the row and carries the active pointer with it.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPatch, "/api/projects/demo",
		strings.NewReader(`{"name":"demo2","description":"renamed"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("patch = %d, body %s", rr.Code, rr.Body.String())
	}
	if name, _ := ps.Active(); name != "demo2" {
		t.Fatalf("active after rename = %q, want demo2", name)
	}
	if _, err := ps.Get("demo"); err == nil {
		t.Fatal("old name still resolves after rename")
	}

	// Exit clears the pointer; enter sets it again.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/projects/exit", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("exit = %d", rr.Code)
	}
	if name, _ := ps.Active(); name != "" {
		t.Fatalf("active after exit = %q", name)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/projects/demo2/enter", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("enter = %d, body %s", rr.Code, rr.Body.String())
	}
	if name, _ := ps.Active(); name != "demo2" {
		t.Fatalf("active after enter = %q", name)
	}

	// Remove drops the row and the pointer with it.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodDelete, "/api/projects/demo2", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete = %d, body %s", rr.Code, rr.Body.String())
	}
	if _, err := ps.Get("demo2"); err == nil {
		t.Fatal("project still exists after delete")
	}
	if name, _ := ps.Active(); name != "" {
		t.Fatalf("active still %q after deleting it", name)
	}
}

// TestProjectEndpointsRejectUnknownNames keeps the 404s honest: every path-addressed
// verb must refuse a project that does not exist rather than inventing one.
func TestProjectEndpointsRejectUnknownNames(t *testing.T) {
	h, _, _ := projectHandler(t)
	for _, tc := range []struct {
		method, path string
		body         string
	}{
		{http.MethodGet, "/api/projects/ghost", ""},
		{http.MethodPatch, "/api/projects/ghost", `{"description":"x"}`},
		{http.MethodDelete, "/api/projects/ghost", ""},
	} {
		rr := httptest.NewRecorder()
		var body *strings.Reader
		if tc.body != "" {
			body = strings.NewReader(tc.body)
		}
		if body == nil {
			h.ServeHTTP(rr, authedReq(tc.method, tc.path, nil))
		} else {
			h.ServeHTTP(rr, authedReq(tc.method, tc.path, body))
		}
		if rr.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", tc.method, tc.path, rr.Code)
		}
	}
}

// TestProjectListAdoptsMemoryOnlyProjects: projects existed as bare memory files
// before the metadata table, and a listing that showed the table alone would
// report that work the user can still see does not exist.
func TestProjectListAdoptsMemoryOnlyProjects(t *testing.T) {
	h, _, mem := projectHandler(t)
	if err := mem.Save("legacy", memory.MemFile{Limit: mem.Limit()}); err != nil {
		t.Fatalf("seed legacy memory: %v", err)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/projects", nil))
	var list struct {
		Projects []string `json:"projects"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(list.Projects) != 1 || list.Projects[0] != "legacy" {
		t.Fatalf("projects = %v, want [legacy]", list.Projects)
	}
}

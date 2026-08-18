package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// askBody builds a POST /api/ask request with the given JSON body.
func askBody(body string) *http.Request {
	req := authedReq(http.MethodPost, "/api/ask", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestAskWithoutEngine(t *testing.T) {
	h := New(Deps{Store: newTestStore(t), StaticDir: t.TempDir(), Token: testToken})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, askBody(`{"prompt":"hi"}`))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when no engine is wired", rr.Code)
	}
}

func TestAskRejectsBadInput(t *testing.T) {
	h := New(Deps{Store: newTestStore(t), StaticDir: t.TempDir(), Token: testToken})
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"invalid json", "{not json", http.StatusBadRequest},
		{"empty prompt", `{"prompt":"   "}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, askBody(tc.body))
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d", rr.Code, tc.want)
			}
		})
	}
}

func TestAskUnauthenticated(t *testing.T) {
	h := New(Deps{Store: newTestStore(t), StaticDir: t.TempDir(), Token: testToken})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/ask", strings.NewReader(`{"prompt":"hi"}`)))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestCancelTask(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	task, err := store.Create(ctx, "", "proj", "doomed", "node", []string{"node"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.Queue(ctx, task.TaskID, "node"); err != nil {
		t.Fatalf("queue: %v", err)
	}

	h := New(Deps{Store: store, StaticDir: t.TempDir(), Token: testToken})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks/"+task.TaskID+"/cancel", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rr.Code, rr.Body.String())
	}
	var out struct {
		ID        string `json:"id"`
		Cancelled int    `json:"cancelled"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ID != task.TaskID || out.Cancelled < 1 {
		t.Fatalf("out = %+v, want the task cancelled", out)
	}
}

func TestCancelTaskNotFound(t *testing.T) {
	h := New(Deps{Store: newTestStore(t), StaticDir: t.TempDir(), Token: testToken})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks/nope/cancel", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestTaskLogs(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	task, err := store.Create(ctx, "", "proj", "logged", "node", []string{"node"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.Queue(ctx, task.TaskID, "node"); err != nil {
		t.Fatalf("queue: %v", err)
	}

	h := New(Deps{Store: store, StaticDir: t.TempDir(), Token: testToken})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/tasks/"+task.TaskID+"/logs", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var out struct {
		ID     string      `json:"id"`
		Events []eventJSON `json:"events"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Events) == 0 {
		t.Fatalf("events = empty, want the create/queue timeline")
	}
}

// newTestDB opens an in-memory migrated database for ledger-backed endpoints.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	return db
}

func TestListNodes(t *testing.T) {
	db := newTestDB(t)
	// Register this node in the capability directory the way the daemon does
	// at startup, so /api/nodes has something real to list.
	card := ledger.Card{Device: "mac", ResourceClass: "Standard", Chip: "apple-m1"}
	if err := ledger.Register(db, card, "node-a", 2); err != nil {
		t.Fatalf("register: %v", err)
	}

	h := New(Deps{Store: newTestStore(t), DB: db, StaticDir: t.TempDir(), Token: testToken})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/nodes", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rr.Code, rr.Body.String())
	}
	var nodes []nodeJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &nodes); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "node-a" || nodes[0].Chip != "apple-m1" {
		t.Fatalf("nodes = %+v, want node-a", nodes)
	}
}

func TestListNodesWithoutDB(t *testing.T) {
	h := New(Deps{Store: newTestStore(t), StaticDir: t.TempDir(), Token: testToken})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/nodes", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when no DB is wired", rr.Code)
	}
}

func TestProjects(t *testing.T) {
	projects := memory.NewProjects(t.TempDir())
	h := New(Deps{Store: newTestStore(t), Projects: projects, StaticDir: t.TempDir(), Token: testToken})

	// Empty list starts as [] (not null) so the web client can map over it.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/projects", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d", rr.Code)
	}
	if got := strings.TrimSpace(rr.Body.String()); got != `{"projects":[]}` {
		t.Fatalf("empty list = %s, want []", got)
	}

	// Create via POST, then the project appears in the list.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/projects", strings.NewReader(`{"name":"openpanda"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("create status = %d, body %s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/projects", nil))
	var out struct {
		Projects []string `json:"projects"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Projects) != 1 || out.Projects[0] != "openpanda" {
		t.Fatalf("projects = %+v, want [openpanda]", out.Projects)
	}

	// Re-creating is idempotent; path-escape names are rejected.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/projects", strings.NewReader(`{"name":"openpanda"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("recreate status = %d, want 200 (idempotent)", rr.Code)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/projects", strings.NewReader(`{"name":"../escape"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("escape status = %d, want 400", rr.Code)
	}
}

func TestProjectsWithoutProjects(t *testing.T) {
	h := New(Deps{Store: newTestStore(t), StaticDir: t.TempDir(), Token: testToken})
	for _, target := range []string{"/api/projects"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, authedReq(method, target, nil))
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s %s status = %d, want 503", method, target, rr.Code)
			}
		}
	}
}

// TestEventsStream checks the SSE change feed announces itself and then pushes
// a change when a task appears. httptest.ResponseRecorder implements Flusher,
// but the handler's poll loop only ends when the request context is cancelled,
// so the test cancels it after the first change event arrives.
func TestEventsStream(t *testing.T) {
	store := newTestStore(t)
	h := New(Deps{Store: store, StaticDir: t.TempDir(), Token: testToken})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := authedReq(http.MethodGet, "/api/events", nil).WithContext(ctx)

	events := make(chan string, 4)
	go func() {
		rr := &capturingRecorder{ResponseRecorder: httptest.NewRecorder(), onWrite: func(s string) {
			if strings.Contains(s, "event: change") {
				select {
				case events <- s:
				default:
				}
			}
		}}
		h.ServeHTTP(rr, req)
	}()

	// The init announcement arrives immediately.
	select {
	case <-events:
	case <-time.After(2 * time.Second):
		t.Fatal("no init event")
	}

	// A new task changes the fingerprint; the next poll pushes a change.
	if _, err := store.Create(ctx, "", "proj", "later", "node", []string{"node"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	select {
	case got := <-events:
		if !strings.Contains(got, "tasks") {
			t.Fatalf("event = %q, want a tasks change", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no change event after task create")
	}
	cancel()
}

// capturingRecorder forwards writes to a callback so the SSE test can observe
// flushed events while the handler is still streaming.
type capturingRecorder struct {
	*httptest.ResponseRecorder
	onWrite func(string)
}

func (c *capturingRecorder) Write(b []byte) (int, error) {
	c.onWrite(string(b))
	return c.ResponseRecorder.Write(b)
}

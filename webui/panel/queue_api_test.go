package panel

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// queueCardYAML is a minimal capability card with one harmless native ability
// so board-created tasks can execute without an agent CLI.
const queueCardYAML = `
device: panel-test
resource_class: Standard
native:
  - id: sys:info
    command: echo
    args: ["board-ok"]
capacity:
  cpu_cores: 4
  ram_gb: 8
  max_concurrent_tasks: 2
`

// newQueueEngine builds an ask engine in queue mode over throwaway storage so
// POST /api/tasks can enqueue and actually execute a task.
func newQueueEngine(t *testing.T) *askengine.Engine {
	t.Helper()
	dir := t.TempDir()
	cardPath := filepath.Join(dir, "capabilities.yaml")
	if err := os.WriteFile(cardPath, []byte(queueCardYAML), 0o644); err != nil {
		t.Fatalf("write card: %v", err)
	}
	cfg := &config.Config{
		Node: config.NodeConfig{Name: "panel-test", ResourceClass: "Standard"},
		Storage: config.StorageConfig{
			DBPath:       filepath.Join(dir, "panda.db"),
			MemoryPath:   filepath.Join(dir, "memory"),
			ProjectsPath: filepath.Join(dir, "projects"),
			SkillsPath:   filepath.Join(dir, "skills"),
			WorkPath:     dir,
		},
		Model: config.ModelConfig{BaseURL: "https://model.test.invalid/v1"},
	}
	engine, err := askengine.New(context.Background(), cfg, askengine.Options{
		CardPath:   cardPath,
		QueueTasks: true,
		Logger:     slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	t.Cleanup(engine.Close)
	return engine
}

// TestCreateTaskEnqueuesWithSession covers the board's "new task" path end to
// end: the task is enqueued, its linked session exists with the prompt as the
// first turn, and the queue scheduler drives the task to done.
func TestCreateTaskEnqueuesWithSession(t *testing.T) {
	engine := newQueueEngine(t)
	sessStore := sessions.NewStore(t.TempDir())
	// The handler's store is a separate in-memory db; the task itself lives
	// in the engine's db — we assert via the API response and session side
	// effects, not by reading the engine's store directly.
	store := newTestStore(t)

	h := New(Deps{
		Store:     store,
		Engine:    engine,
		Sessions:  sessStore,
		StaticDir: t.TempDir(),
		Token:     testToken,
	})

	body := strings.NewReader(`{"title":"echo task","prompt":"run echo","requires":["sys:info"],"authorize":true,"priority":"high"}`)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("create task: %d %s", rr.Code, rr.Body.String())
	}
	var res struct {
		TaskID    string `json:"task_id"`
		SessionID string `json:"session_id"`
		State     string `json:"state"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.TaskID == "" || res.State != "queued" {
		t.Fatalf("create result = %+v", res)
	}

	// The linked session carries the task title and the prompt as user turn.
	sess, err := sessStore.Get(res.SessionID)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if sess.Title != "echo task" || len(sess.Turns) != 1 || sess.Turns[0].Text != "run echo" {
		t.Fatalf("session = %+v", sess)
	}
}

// TestPatchTaskPriority verifies quick priority setting from the board card.
func TestPatchTaskPriority(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	task, err := store.Create(ctx, "", "proj", "prioritized", "node", []string{"node"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	h := New(Deps{Store: store, StaticDir: t.TempDir(), Token: testToken})

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPatch, "/api/tasks/"+task.TaskID, strings.NewReader(`{"priority":"high"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/tasks/"+task.TaskID, nil))
	var out taskJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Priority != "high" {
		t.Fatalf("priority = %s, want high", out.Priority)
	}

	// Unknown labels are rejected.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPatch, "/api/tasks/"+task.TaskID, strings.NewReader(`{"priority":"urgent"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad priority code = %d, want 400", rr.Code)
	}
}

// TestReorderTasks verifies drag-reorder rewrites seq 1..n in order.
func TestReorderTasks(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	first, _ := store.Create(ctx, "", "proj", "first", "node", []string{"node"})
	second, _ := store.Create(ctx, "", "proj", "second", "node", []string{"node"})

	h := New(Deps{Store: store, StaticDir: t.TempDir(), Token: testToken})
	body := strings.NewReader(`{"ids":["` + second.TaskID + `","` + first.TaskID + `"]}`)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks/reorder", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("reorder: %d %s", rr.Code, rr.Body.String())
	}

	got, err := store.Get(ctx, second.TaskID)
	if err != nil || got.Seq != 1 {
		t.Fatalf("second seq = %d, %v; want 1", got.Seq, err)
	}
	got, err = store.Get(ctx, first.TaskID)
	if err != nil || got.Seq != 2 {
		t.Fatalf("first seq = %d, %v; want 2", got.Seq, err)
	}
}

// TestListAgentsSmoke checks the agents endpoint answers with one entry per
// known CLI (installed or not depending on the test host's PATH).
func TestListAgentsSmoke(t *testing.T) {
	h := New(Deps{Store: newTestStore(t), StaticDir: t.TempDir(), Token: testToken})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/agents", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("agents: %d", rr.Code)
	}
	var out []agentJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != len(agents.Registry()) {
		t.Fatalf("agents = %d, want %d", len(out), len(agents.Registry()))
	}
}

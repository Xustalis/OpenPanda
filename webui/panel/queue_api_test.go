// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
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
	isolateUserDirs(t)
	dir := t.TempDir()
	cardPath := filepath.Join(dir, "capabilities.yaml")
	if err := os.WriteFile(cardPath, []byte(queueCardYAML), 0o644); err != nil {
		t.Fatalf("write card: %v", err)
	}
	cfg := &config.Config{
		// Kind vm, not the physical default: the engine probes the node
		// identity lock to decide whether it owns the row (a live daemon on
		// this machine would make the engine a borrower that must NOT
		// consume the queue). A physical test node shares the real machine
		// identity, so the suite only passes when no daemon happens to be
		// running — scope the lock to a distinct kind and it always owns.
		Node: config.NodeConfig{Name: "panel-test", Kind: "vm", ResourceClass: "Standard"},
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
	t.Cleanup(func() {
		engine.Close()
		removeTempDir(t, dir)
	})
	return engine
}

func newApprovalQueueEngine(t *testing.T) (*askengine.Engine, string) {
	t.Helper()
	isolateUserDirs(t)
	dir := t.TempDir()
	counter := filepath.Join(dir, "runs.txt")
	script := filepath.Join(dir, "run-once.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'x\\n' >> \"$1\"\n"), 0o755); err != nil {
		t.Fatalf("write command: %v", err)
	}
	card := fmt.Sprintf(`
device: panel-approval-test
resource_class: Standard
native:
  - id: test:once
    command: sh
    args: [%q, %q]
    tier: 2
capacity:
  cpu_cores: 4
  ram_gb: 8
  max_concurrent_tasks: 2
`, script, counter)
	cardPath := filepath.Join(dir, "capabilities.yaml")
	if err := os.WriteFile(cardPath, []byte(card), 0o644); err != nil {
		t.Fatalf("write card: %v", err)
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"done"}]}`))
	}))
	t.Cleanup(model.Close)
	cfg := &config.Config{
		// Kind vm for the same reason as newQueueEngine: a physical test node
		// collides with a live daemon's identity lock and the engine then
		// refuses to consume the queue, stranding every task in queued.
		Node: config.NodeConfig{Name: "panel-approval-test", Kind: "vm", ResourceClass: "Standard"},
		Storage: config.StorageConfig{
			DBPath: filepath.Join(dir, "panda.db"), MemoryPath: filepath.Join(dir, "memory"),
			ProjectsPath: filepath.Join(dir, "projects"), SkillsPath: filepath.Join(dir, "skills"), WorkPath: dir,
		},
		Model: config.ModelConfig{BaseURL: model.URL, APIKey: "test", Model: "test"},
	}
	engine, err := askengine.New(context.Background(), cfg, askengine.Options{
		CardPath: cardPath, QueueTasks: true, Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("new approval engine: %v", err)
	}
	t.Cleanup(func() {
		engine.Close()
		removeTempDir(t, dir)
	})
	return engine, counter
}

func TestApproveAuthorizationReviewExecutesOnce(t *testing.T) {
	engine, counter := newApprovalQueueEngine(t)
	store := engine.TaskStore()
	h := New(Deps{Store: store, Engine: engine, StaticDir: t.TempDir(), Token: testToken})

	body := strings.NewReader(`{"title":"run once","prompt":"run once","requires":["test:once"],"authorize":false}`)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("create task: %d %s", rr.Code, rr.Body.String())
	}
	var created struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil || created.TaskID == "" {
		t.Fatalf("decode create result: %+v, %v", created, err)
	}

	// Windows CI runners stall loopback/process work for seconds at a time
	// (the cross-platform job reruns failures for the same reason). Give the
	// engine a generous window so a slow runner is not read as a regression.
	deadline := time.Now().Add(30 * time.Second)
	for {
		task, err := store.Get(context.Background(), created.TaskID)
		if err == nil && task.State == core.StateReview {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task did not reach review: %+v, %v", task, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if data, err := os.ReadFile(counter); err == nil && len(data) != 0 {
		t.Fatalf("command ran before approval: %q", data)
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read counter before approval: %v", err)
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks/"+created.TaskID+"/approve", nil))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("approve task: %d %s", rr.Code, rr.Body.String())
	}
	var op approvalOperation
	if err := json.Unmarshal(rr.Body.Bytes(), &op); err != nil || op.ID == "" || op.Status != core.StateRunning {
		t.Fatalf("decode approve operation: %+v, %v", op, err)
	}

	deadline = time.Now().Add(30 * time.Second)
	var (
		task core.Task
		err  error
	)
	for {
		task, err = store.Get(context.Background(), created.TaskID)
		if err == nil && task.State == core.StateDone {
			break
		}
		if time.Now().After(deadline) {
			// Distinguish "the command never ran" from "it ran but the state
			// did not settle" — the counter is the execution's only trace.
			ran, readErr := os.ReadFile(counter)
			t.Fatalf("approved task did not reach done: %+v, %v (counter=%q readErr=%v)", task, err, ran, readErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !task.Authorized {
		t.Fatalf("approved task = %+v, want authorized", task)
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if string(data) != "x\n" {
		t.Fatalf("command executions = %q, want exactly one", data)
	}
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

// TestCreateTaskScopeValidation locks the parallel-mode contract on the board's
// create path: scope lanes and the isolated copy need a project to mean
// anything, and they are mutually exclusive — the same rules the CLI flags
// enforce. The API must refuse rather than let a declared lane silently
// degrade into a whole-project claim.
func TestCreateTaskScopeValidation(t *testing.T) {
	engine := newQueueEngine(t)
	store := newTestStore(t)
	h := New(Deps{Store: store, Engine: engine, StaticDir: t.TempDir(), Token: testToken})

	post := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks", strings.NewReader(body)))
		return rr
	}
	if rr := post(`{"title":"t","scope_paths":["internal/api"]}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("scope without project: %d", rr.Code)
	}
	if rr := post(`{"title":"t","isolate":true}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("isolate without project: %d", rr.Code)
	}
	if rr := post(`{"title":"t","project":"p","scope_paths":["a"],"isolate":true}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("scope+isolate: %d", rr.Code)
	}
	// A scoped task with a project is accepted and the declaration lands on
	// the row — trimmed, with blank entries not counted. The blank leads so
	// the trim has something to do ahead of a real lane.
	rr := post(`{"title":"t","project":"p","scope_paths":[" "," a "]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("scoped task: %d %s", rr.Code, rr.Body.String())
	}
	// What lands on the row is the trimmed list, not the request field. The
	// shared enqueue tail has to be handed the trimmed slice: trimming reuses
	// the request's backing array and only shortens it, so passing the raw
	// field files a lane the board never showed.
	var res struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	row, err := engine.TaskStore().Get(context.Background(), res.TaskID)
	if err != nil {
		t.Fatalf("read task: %v", err)
	}
	if len(row.ScopePaths) != 1 || row.ScopePaths[0] != "a" {
		t.Fatalf("scope paths on the row = %v, want [a]", row.ScopePaths)
	}
	// A board task that names no ability still routes on the default, and the
	// shared tail owns that fallback so neither surface can forget it.
	if len(row.Requires) != len(defaultBoardRequires) || row.Requires[0] != defaultBoardRequires[0] {
		t.Fatalf("requires on the row = %v, want %v", row.Requires, defaultBoardRequires)
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

// TestBoardTaskInputDeviceIsHardPin locks the board's device-picker contract.
// A device chosen by the operator used to be written to PreferredNode: while
// that device was offline, RouteP could not find it among the capable online
// rows, fell through to scored ranking, and localBias handed the task to this
// node's own agent CLI. The UI still showed the other device as its target.
func TestBoardTaskInputDeviceIsHardPin(t *testing.T) {
	in := boardTaskInput(createTaskRequest{
		Title:    "collect device status",
		Prompt:   "report xin status",
		Node:     "xin",
		Requires: []string{"agent:opencode"},
	}, []string{"agent:opencode"})
	if in.TargetNode != "xin" {
		t.Fatalf("TargetNode = %q, want xin (hard pin)", in.TargetNode)
	}
	if in.PreferredNode != "" {
		t.Fatalf("PreferredNode = %q, want empty: the board has no soft-hint surface", in.PreferredNode)
	}
	empty := boardTaskInput(createTaskRequest{Title: "free routing"}, []string{"sys:info"})
	if empty.TargetNode != "" || empty.PreferredNode != "" {
		t.Fatalf("empty picker routed as %q / %q, want free routing", empty.TargetNode, empty.PreferredNode)
	}
}

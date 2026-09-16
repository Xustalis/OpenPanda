package askengine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/memory"
)

// anthropicText is one non-streaming Messages-API response carrying text.
func anthropicText(text string) map[string]any {
	return map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
		"usage":   map[string]int{"input_tokens": 8, "output_tokens": 6},
	}
}

// newBoundaryTestEngine wraps newMgmtTestEngine's fixture (a card with the
// codex agent registered) with a fake entry-model server, queue mode on, and
// the memory injector the ask loop dereferences.
func newBoundaryTestEngine(t *testing.T, handler http.HandlerFunc) (*Engine, *httptest.Server) {
	t.Helper()
	e, _ := newMgmtTestEngine(t)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := entry.NewClient(config.ModelConfig{BaseURL: srv.URL, Model: "test-model", APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	e.injector = memory.NewInjector(memory.NewHermes(t.TempDir()), nil)
	e.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	e.client.Store(client)
	e.queueTasks = true // enqueue only: the test asserts routing, not adapter runs
	return e, srv
}

// TestTaskJSONDirectiveQueued reproduces the incident chain: "调度一下 codex
// …" must converge on a task. The text-directive path (task JSON in the
// answer) still lands a real task in the commander queue, and the tool roster
// the entry model sees always carries task_submit so tool-call-oriented
// models have the same channel.
func TestTaskJSONDirectiveQueued(t *testing.T) {
	var mu sync.Mutex
	var toolNames []string
	taskJSON := `{"kind":"task","task":{"title":"用 codex 查看项目","context_type":"command","requires":{"abilities":["agent:codex"]},"spec":{"scope":"","target":"查看项目目录并说明用途","constraints":[],"success_definition":"给出项目说明"},"complexity":0.3,"risk":"low","resource_profile":{"cpu":1,"ram_gb":1,"gpu_vram_gb":0,"duration_hint":"short"}}}`
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		for _, tool := range req.Tools {
			toolNames = append(toolNames, tool.Name)
		}
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		b, _ := json.Marshal(anthropicText(taskJSON))
		_, _ = w.Write(b)
	})

	res, err := e.Ask(context.Background(), "调度一下codex，看一下这个项目目录", false)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if res.Kind != "task" || res.TaskID == "" {
		t.Fatalf("res = %+v, want a queued task", res)
	}
	mu.Lock()
	defer mu.Unlock()
	seenSubmit := false
	for _, name := range toolNames {
		if name == "task_submit" {
			seenSubmit = true
		}
	}
	if !seenSubmit {
		t.Fatalf("task_submit not offered; tools = %v", toolNames)
	}
	// The task actually entered the commander queue (not just a model note).
	st, err := core.NewTaskStore(e.db, nil).ListByState(context.Background(), core.StateQueued)
	if err != nil || len(st) == 0 {
		t.Fatalf("no queued task in the store: %v (%d)", err, len(st))
	}
}

// TestTaskSubmitToolCallQueued covers the tool-call-oriented failure: the
// model never emits the task JSON directive, but it does call tools. A
// task_submit call with explicit abilities must land a real task in the
// queue, and its result must be fed back so the model can report.
func TestTaskSubmitToolCallQueued(t *testing.T) {
	var mu sync.Mutex
	var toolNames []string
	calls := 0
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		for _, tool := range req.Tools {
			toolNames = append(toolNames, tool.Name)
		}
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		var resp map[string]any
		if n == 1 {
			// Native tool_use of the bridge tool, arguments as the model sends them.
			resp = map[string]any{
				"content": []map[string]any{{
					"type": "tool_use", "id": "toolu_1", "name": "task_submit",
					"input": map[string]any{
						"title":     "查看项目目录",
						"target":    "浏览 Document/project/panda 并说明用途",
						"abilities": []string{"agent:codex"},
					},
				}},
				"usage": map[string]int{"input_tokens": 8, "output_tokens": 6},
			}
		} else {
			resp = anthropicText("已派 codex 去查看项目目录，任务执行中。")
		}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	})

	res, err := e.Ask(context.Background(), "让 codex 去看一下 Document/project/panda", false)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	seenSubmit := false
	for _, name := range toolNames {
		if name == "task_submit" {
			seenSubmit = true
		}
	}
	if !seenSubmit {
		t.Fatalf("task_submit not offered; tools = %v", toolNames)
	}
	// The bridge call must have landed a real task carrying the requested ability.
	tasks, terr := core.NewTaskStore(e.db, nil).ListByState(context.Background(), core.StateQueued)
	if terr != nil || len(tasks) == 0 {
		t.Fatalf("no queued task: %v (%d)", terr, len(tasks))
	}
	var dispatched *core.Task
	for i := range tasks {
		if tasks[i].Title == "查看项目目录" {
			dispatched = &tasks[i]
		}
	}
	if dispatched == nil {
		t.Fatalf("task_submit task not found in queue: %+v", tasks)
	}
	if len(dispatched.Requires) != 1 || dispatched.Requires[0] != "agent:codex" {
		t.Errorf("requested ability not applied: %v", dispatched.Requires)
	}
	_ = res
}

func TestTaskSubmitTier2ReturnsApprovalAndPersistsWorkDir(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		var resp map[string]any
		if n == 1 {
			resp = map[string]any{
				"content": []map[string]any{{
					"type": "tool_use", "id": "toolu_tier2", "name": "task_submit",
					"input": map[string]any{"title": "push", "target": "push branch", "abilities": []string{"git:push"}},
				}},
				"usage": map[string]int{"input_tokens": 8, "output_tokens": 6},
			}
		} else {
			resp = anthropicText("needs approval")
		}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	})
	e.queueTasks = false
	workDir := t.TempDir()
	res, err := e.AskTurns(context.Background(), nil, "push this branch", workDir, false, StreamCallbacks{})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if res.Kind != "task" || res.TaskID == "" || res.TaskState != core.StateReview || !res.NeedsApproval || res.Approval == nil {
		t.Fatalf("res = %+v, want typed review approval", res)
	}
	if res.Approval.TaskID != res.TaskID {
		t.Fatalf("approval task = %q, want %q", res.Approval.TaskID, res.TaskID)
	}
	stored, err := e.TaskStore().Get(context.Background(), res.TaskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if stored.WorkDir != workDir {
		t.Fatalf("stored workdir = %q, want %q", stored.WorkDir, workDir)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("model calls = %d, want approval returned before prose convergence", calls)
	}
}

func TestCompletedNativeTaskSubmitPreservesTaskKindAfterConvergence(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		var resp map[string]any
		if n == 1 {
			resp = map[string]any{
				"content": []map[string]any{{
					"type": "tool_use", "id": "toolu_native", "name": "task_submit",
					"input": map[string]any{"title": "system info", "target": "read system info", "abilities": []string{"sys:info"}},
				}},
				"usage": map[string]int{"input_tokens": 8, "output_tokens": 6},
			}
		} else {
			resp = anthropicText("system information read")
		}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	})
	e.queueTasks = false
	res, err := e.Ask(context.Background(), "read system info", false)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if res.Kind != "task" || res.TaskID == "" || res.TaskState != core.StateDone || !res.OK {
		t.Fatalf("res = %+v, want completed typed task", res)
	}
	if res.Answer != "system information read" {
		t.Fatalf("answer = %q", res.Answer)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("model calls = %d, want submit plus convergence", calls)
	}
}

func TestResumeApprovedAcceptsCompletedWorkWithoutScheduler(t *testing.T) {
	e, _ := newMgmtTestEngine(t)
	store := e.TaskStore()
	ctx := context.Background()
	task, err := store.Create(ctx, "", "proj", "review completed work", "test-node", []string{"test-node"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, step := range []func() error{
		func() error { return store.Queue(ctx, task.TaskID, "test-node") },
		func() error { return store.Dispatch(ctx, task.TaskID, "test-node", "test-node") },
		func() error { return store.Accept(ctx, task.TaskID, "test-node") },
	} {
		if err := step(); err != nil {
			t.Fatalf("drive task: %v", err)
		}
	}
	if err := store.PauseWithResult(ctx, task.TaskID, "test-node", map[string]any{
		"task_id": task.TaskID, "state": core.StateReview, "ok": true,
		"stdout": "kept output", "stderr": "kept warning", "exit_code": 7,
		"agent": "codex", "model": "model-x", "injected": true,
	}); err != nil {
		t.Fatalf("pause with result: %v", err)
	}
	e.sched = nil

	res := e.ResumeApproved(ctx, task.TaskID, "", StreamCallbacks{})
	if res.TaskState != core.StateDone || !res.OK {
		t.Fatalf("res = %+v, want accepted done task", res)
	}
	if res.Stdout != "kept output" || res.Stderr != "kept warning" || res.ExitCode != 7 ||
		res.Agent != "codex" || res.Model != "model-x" || !res.Injected {
		t.Fatalf("stored result attribution lost: %+v", res)
	}
}

func TestResumeApprovedAcceptsCompletedWorkWithEmptyResultJSON(t *testing.T) {
	e, _ := newMgmtTestEngine(t)
	store := e.TaskStore()
	ctx := context.Background()
	task, err := store.Create(ctx, "", "proj", "legacy review task", "test-node", []string{"test-node"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, step := range []func() error{
		func() error { return store.Queue(ctx, task.TaskID, "test-node") },
		func() error { return store.Dispatch(ctx, task.TaskID, "test-node", "test-node") },
		func() error { return store.Accept(ctx, task.TaskID, "test-node") },
		func() error {
			return store.PauseWithDisposition(ctx, task.TaskID, "test-node", "manual signoff", core.ApprovalAcceptWork)
		},
	} {
		if err := step(); err != nil {
			t.Fatalf("drive task: %v", err)
		}
	}
	e.sched = nil

	res := e.ResumeApproved(ctx, task.TaskID, "", StreamCallbacks{})
	if res.TaskState != core.StateDone || !res.OK {
		t.Fatalf("res = %+v, want accepted done task with OK=true on empty result", res)
	}
}

// TestQueueAskKeepsQueueTools guards the roster's other side: a queue
// question must still see the taskq_* family — and task_submit stays offered
// there too (no intent gating anywhere).
func TestQueueAskKeepsQueueTools(t *testing.T) {
	var mu sync.Mutex
	var toolNames []string
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		for _, tool := range req.Tools {
			toolNames = append(toolNames, tool.Name)
		}
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		b, _ := json.Marshal(anthropicText("队列里有 1 个排队任务。"))
		_, _ = w.Write(b)
	})

	if _, err := e.Ask(context.Background(), "把任务 01a0773f 的优先级修改成高", false); err != nil {
		t.Fatalf("ask: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	foundPriority, foundSubmit := false, false
	for _, name := range toolNames {
		if name == "taskq_priority" {
			foundPriority = true
		}
		if name == "task_submit" {
			foundSubmit = true
		}
	}
	if !foundPriority {
		t.Fatalf("queue-management ask lost taskq_priority; tools = %v", toolNames)
	}
	if !foundSubmit {
		t.Fatalf("task_submit must be offered on every ask; tools = %v", toolNames)
	}
}

// TestDSMLToolCallRecovered reproduces the protocol-incompatibility half of
// the incident: the endpoint returns the tool call as DSML text inside the
// content field (full-width pipe variant, as observed in the entry cache).
// The ask must execute the call through the registry and converge on the
// follow-up answer instead of showing the markup to the user.
func TestDSMLToolCallRecovered(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		var text string
		if n == 1 {
			text = "我先看一下系统状态。\n<｜｜DSML｜｜tool_calls>\n<｜｜DSML｜｜invoke name=\"system_status\">\n</｜｜DSML｜｜invoke>\n</｜｜DSML｜｜tool_calls>"
		} else {
			text = "系统状态正常，设备网络在线。"
		}
		b, _ := json.Marshal(anthropicText(text))
		_, _ = w.Write(b)
	})

	res, err := e.Ask(context.Background(), "请说明一下当前系统状态", false)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("model calls = %d, want 2 (recovered tool call + report)", calls)
	}
	if res.Kind != "answer" || res.Answer != "系统状态正常，设备网络在线。" {
		t.Fatalf("res = %+v", res)
	}
	if strings.Contains(res.Answer, "DSML") {
		t.Fatal("DSML markup leaked into the answer")
	}
}

// TestDSMLToolFreeRoundStripped covers the incident's final shape: the
// tool-free convergence round answers with DSML markup. The markup must be
// stripped and the prose kept, never shown raw.
func TestDSMLToolFreeRoundStripped(t *testing.T) {
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		// Every round answers with the same DSML markup; the loop must never
		// show it raw, whichever round the budget ends on.
		text := "我现在就调度 codex 去探索这个项目。\n<||DSML||tool_calls>\n<||DSML||invoke name=\"taskq_priority\">\n<||DSML||parameter name=\"priority\" string=\"true\">high</||DSML||parameter>\n</||DSML||invoke>\n</||DSML||tool_calls>"
		b, _ := json.Marshal(anthropicText(text))
		_, _ = w.Write(b)
	})

	// A dispatch ask whose model keeps answering the same DSML text: the
	// recovered taskq_priority call lacks task_id, so it errors, the error is
	// fed back, and the loop re-asks until the rounds budget burns down to the
	// tool-free final call, whose DSML is stripped.
	res, err := e.Ask(context.Background(), "调度一下codex，看一下这个项目目录", false)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if strings.Contains(res.Answer, "DSML") || strings.Contains(res.Answer, "｜｜") {
		t.Fatalf("DSML markup leaked into the result: %q", res.Answer)
	}
	if res.Answer == "" {
		t.Fatal("empty result")
	}
}

func countTasksWithTitlePrefix(t *testing.T, e *Engine, prefix string) int {
	t.Helper()
	var count int
	if err := e.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM tasks WHERE title LIKE ?`, prefix+"%").Scan(&count); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	return count
}

func TestTaskSubmitBudgetRefusesFourthBeforeExecution(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		var resp map[string]any
		if n <= 4 {
			resp = map[string]any{
				"content": []map[string]any{{
					"type": "tool_use", "id": fmt.Sprintf("toolu_budget_%d", n), "name": "task_submit",
					"input": map[string]any{"title": fmt.Sprintf("tool-budget-%d", n), "target": "read system info", "abilities": []string{"sys:info"}},
				}},
				"usage": map[string]int{"input_tokens": 8, "output_tokens": 6},
			}
		} else {
			resp = anthropicText("budget reached")
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	e.queueTasks = false
	res, err := e.Ask(context.Background(), "run several checks", true)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if got := countTasksWithTitlePrefix(t, e, "tool-budget-"); got != 3 {
		t.Fatalf("executed tasks = %d, want exactly 3", got)
	}
	if res.Kind != "task" || res.TaskID == "" {
		t.Fatalf("res = %+v, want latest completed task", res)
	}
}

func TestTaskDirectiveBudgetRefusesFourthBeforeExecution(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("content-type", "application/json")
		if !strings.Contains(string(body), `"tools"`) {
			_ = json.NewEncoder(w).Encode(anthropicText("task summary"))
			return
		}
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n <= 4 {
			taskJSON := fmt.Sprintf(`{"kind":"task","task":{"title":"directive-budget-%d","context_type":"command","requires":{"abilities":["sys:info"]},"spec":{"scope":"","target":"read system info","constraints":[],"success_definition":"done"},"complexity":0.1,"risk":"low","resource_profile":{"cpu":1,"ram_gb":1,"gpu_vram_gb":0,"duration_hint":"short"}}}`, n)
			_ = json.NewEncoder(w).Encode(anthropicText(taskJSON))
			return
		}
		_ = json.NewEncoder(w).Encode(anthropicText("budget reached"))
	})
	e.queueTasks = false
	res, err := e.Ask(context.Background(), "run several checks", true)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if got := countTasksWithTitlePrefix(t, e, "directive-budget-"); got != 3 {
		t.Fatalf("executed tasks = %d, want exactly 3", got)
	}
	if res.Kind != "task" || res.TaskID == "" {
		t.Fatalf("res = %+v, want latest completed task", res)
	}
}

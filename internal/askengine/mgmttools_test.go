package askengine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/reminders"
	"github.com/Xustalis/OpenPanda/internal/storage"
	"gopkg.in/yaml.v3"
)

// newMgmtTestEngine builds an engine over a real (migrated) SQLite store with
// one registered card and tasks in several states — the fixture the
// management tool tests query against.
func newMgmtTestEngine(t *testing.T) (*Engine, *entry.Registry) {
	t.Helper()
	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	card := ledger.Card{
		Device:   "test-node",
		NodeKind: "physical",
		Native: []ledger.NativeAbility{
			{ID: "sys:info", Command: "uname", Args: []string{"-a"}, Tier: 1},
			{ID: "git:push", Command: "git", Args: []string{"push"}, Tier: 2},
		},
		Agents: map[string]ledger.Agent{
			"codex": {Adapter: "codex", Capabilities: []string{"code:modify"}, CostTier: "low"},
		},
		Capacity: ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 2},
	}
	if err := ledger.Register(db, card, "test-node", 1); err != nil {
		t.Fatalf("register card: %v", err)
	}

	cardYAML, err := yaml.Marshal(card)
	if err != nil {
		t.Fatalf("marshal card: %v", err)
	}
	cardPath := filepath.Join(root, "capabilities.yaml")
	if err := os.WriteFile(cardPath, cardYAML, 0o644); err != nil {
		t.Fatalf("write card file: %v", err)
	}

	ctx := context.Background()
	store := core.NewTaskStore(db, nil)
	seed := func(title, to string) core.Task {
		t.Helper()
		task, err := store.Create(ctx, "", "proj", title, "test-node", nil)
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		if err := store.Queue(ctx, task.TaskID, "test-node"); err != nil {
			t.Fatalf("queue %s: %v", title, err)
		}
		if to == "done" {
			if err := store.Dispatch(ctx, task.TaskID, "test-node", "test-node"); err != nil {
				t.Fatalf("dispatch %s: %v", title, err)
			}
			if err := store.Accept(ctx, task.TaskID, "test-node"); err != nil {
				t.Fatalf("accept %s: %v", title, err)
			}
			if err := store.Complete(ctx, task.TaskID, "test-node", map[string]any{
				"ok": true, "result": "构建通过，产物已落盘", "exit_code": 0,
			}); err != nil {
				t.Fatalf("complete %s: %v", title, err)
			}
		}
		if to == "failed" {
			if err := store.Dispatch(ctx, task.TaskID, "test-node", "test-node"); err != nil {
				t.Fatalf("dispatch %s: %v", title, err)
			}
			if err := store.Accept(ctx, task.TaskID, "test-node"); err != nil {
				t.Fatalf("accept %s: %v", title, err)
			}
			if err := store.Fail(ctx, task.TaskID, "test-node", "boom"); err != nil {
				t.Fatalf("fail %s: %v", title, err)
			}
		}
		return task
	}
	queued := seed("还在排队的任务", "queued")
	done := seed("已完成的任务", "done")
	seed("失败的任务", "failed")
	_ = queued
	_ = done

	cfg := &config.Config{}
	cfg.Node.Name = "test-node"
	cfg.Node.Kind = "physical"
	cfg.Model.Model = "test-model"

	sched := core.NewCore(db, "test-node", card, 1, nil, cfg.Model)
	rem := reminders.NewStore(db)

	e := &Engine{
		cfg:      cfg,
		db:       db,
		cardPath: cardPath,
		sched:    sched,
		schedCtx: context.Background(),
		remind:   rem,
	}
	e.registry = buildToolRegistry(e, memory.NewHermes(t.TempDir()), nil, rem)
	return e, e.registry
}

// runMgmtTool executes one registered management tool and returns its output.
func runMgmtTool(t *testing.T, reg *entry.Registry, name string, args map[string]any) string {
	t.Helper()
	tool, ok := reg.Lookup(name)
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	out, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("tool %s failed: %v", name, err)
	}
	return out
}

func TestMgmtToolsRegistered(t *testing.T) {
	_, reg := newMgmtTestEngine(t)
	allTools := []string{
		"system_status", "card_list", "card_show", "taskq_list", "taskq_show",
		"taskq_cancel", "taskq_priority", "taskq_move", "taskq_create",
		"card_native_add", "card_native_remove", "card_agent_add", "card_agent_set", "card_agent_remove",
		"card_manual_add", "card_manual_remove",
		"project_list", "project_create", "project_enter", "project_exit",
		"node_remove", "reminder_delete",
	}
	for _, name := range allTools {
		tool, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("management tool %s not registered", name)
		}
		if tool.Tier != 1 {
			t.Errorf("tool %s tier = %d, want 1 (reversible/unrestricted)", name, tool.Tier)
		}
		if tool.Description == "" {
			t.Errorf("tool %s has no description", name)
		}
	}
}

func TestSystemStatus(t *testing.T) {
	_, reg := newMgmtTestEngine(t)
	out := runMgmtTool(t, reg, "system_status", nil)

	for _, want := range []string{
		"版本：",
		"本节点：test-node",
		"入口模型：test-model",
		"能力卡：已加载",
		"1 台设备，1 在线",
		"排队中 1",
		"已完成 1",
		"失败 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("system_status missing %q;\ngot:\n%s", want, out)
		}
	}
}

func TestCardListAndShow(t *testing.T) {
	_, reg := newMgmtTestEngine(t)

	list := runMgmtTool(t, reg, "card_list", nil)
	for _, want := range []string{"test-node", "sys:info", "agent:codex", "←本机"} {
		if !strings.Contains(list, want) {
			t.Errorf("card_list missing %q;\ngot:\n%s", want, list)
		}
	}

	self := runMgmtTool(t, reg, "card_show", nil)
	for _, want := range []string{"uname -a", "git push", "tier-2 需授权", "codex", "code:modify", "8 核 / 16 GiB"} {
		if !strings.Contains(self, want) {
			t.Errorf("card_show(self) missing %q;\ngot:\n%s", want, self)
		}
	}

	// Unknown device: the error names what is available so the model can retry.
	tool, _ := reg.Lookup("card_show")
	if _, err := tool.Run(context.Background(), map[string]any{"name": "nope"}); err == nil ||
		!strings.Contains(err.Error(), "test-node") {
		t.Errorf("card_show(unknown) err = %v, want it to list test-node", err)
	}
}

func TestTaskqListFilters(t *testing.T) {
	_, reg := newMgmtTestEngine(t)

	active := runMgmtTool(t, reg, "taskq_list", nil)
	if !strings.Contains(active, "还在排队的任务") {
		t.Errorf("default filter missing the queued task;\ngot:\n%s", active)
	}
	if strings.Contains(active, "已完成的任务") || strings.Contains(active, "失败的任务") {
		t.Errorf("default filter must not show history;\ngot:\n%s", active)
	}

	history := runMgmtTool(t, reg, "taskq_list", map[string]any{"filter": "history"})
	for _, want := range []string{"已完成的任务", "失败的任务"} {
		if !strings.Contains(history, want) {
			t.Errorf("history filter missing %q;\ngot:\n%s", want, history)
		}
	}
	if strings.Contains(history, "还在排队的任务") {
		t.Errorf("history filter must not show active tasks;\ngot:\n%s", history)
	}
}

func TestTaskqShow(t *testing.T) {
	_, reg := newMgmtTestEngine(t)

	list := runMgmtTool(t, reg, "taskq_list", map[string]any{"filter": "done"})
	// The full id travels on the row; taskq_show must accept it back.
	taskID := ""
	for _, line := range strings.Split(list, "\n") {
		if strings.Contains(line, "已完成的任务") {
			fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "- "))
			if len(fields) > 0 {
				taskID = fields[0]
			}
		}
	}
	if taskID == "" {
		t.Fatalf("no task id found in list output:\n%s", list)
	}

	detail := runMgmtTool(t, reg, "taskq_show", map[string]any{"task_id": taskID})
	for _, want := range []string{"已完成的任务", "已完成", "构建通过，产物已落盘", "负责节点：test-node", "事件时间线"} {
		if !strings.Contains(detail, want) {
			t.Errorf("taskq_show missing %q;\ngot:\n%s", want, detail)
		}
	}

	tool, _ := reg.Lookup("taskq_show")
	if _, err := tool.Run(context.Background(), map[string]any{"task_id": "task-nope"}); err == nil {
		t.Error("taskq_show(unknown) must error, got nil")
	}
}

func TestTaskqCancel(t *testing.T) {
	_, reg := newMgmtTestEngine(t)
	list := runMgmtTool(t, reg, "taskq_list", map[string]any{"filter": "queued"})
	taskID := ""
	for _, line := range strings.Split(list, "\n") {
		if strings.Contains(line, "还在排队的任务") {
			fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "- "))
			if len(fields) > 0 {
				taskID = fields[0]
			}
		}
	}
	if taskID == "" {
		t.Fatalf("no queued task id found in list:\n%s", list)
	}

	res := runMgmtTool(t, reg, "taskq_cancel", map[string]any{"task_id": taskID})
	if !strings.Contains(res, "已成功取消") {
		t.Fatalf("taskq_cancel unexpected result: %s", res)
	}

	detail := runMgmtTool(t, reg, "taskq_show", map[string]any{"task_id": taskID})
	if !strings.Contains(detail, "已取消") {
		t.Fatalf("task state want 已取消, got:\n%s", detail)
	}

	res2 := runMgmtTool(t, reg, "taskq_cancel", map[string]any{"task_id": taskID})
	if !strings.Contains(res2, "已是终态或已取消") {
		t.Fatalf("repeat taskq_cancel want terminal message, got: %s", res2)
	}
}

func TestTaskqPriorityAndMove(t *testing.T) {
	_, reg := newMgmtTestEngine(t)
	list := runMgmtTool(t, reg, "taskq_list", map[string]any{"filter": "queued"})
	taskID := ""
	for _, line := range strings.Split(list, "\n") {
		if strings.Contains(line, "还在排队的任务") {
			fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "- "))
			if len(fields) > 0 {
				taskID = fields[0]
			}
		}
	}
	if taskID == "" {
		t.Fatalf("no queued task id found")
	}

	resPrio := runMgmtTool(t, reg, "taskq_priority", map[string]any{
		"task_id":  taskID,
		"priority": "high",
	})
	if !strings.Contains(resPrio, "优先级设置为 high") {
		t.Errorf("taskq_priority result: %s", resPrio)
	}

	resMove := runMgmtTool(t, reg, "taskq_move", map[string]any{
		"task_id": taskID,
		"seq":     3,
	})
	if !strings.Contains(resMove, "排队顺序序号设置为 3") {
		t.Errorf("taskq_move result: %s", resMove)
	}
}

func TestTaskqCreate(t *testing.T) {
	_, reg := newMgmtTestEngine(t)
	res := runMgmtTool(t, reg, "taskq_create", map[string]any{
		"title":    "测试新增任务",
		"prompt":   "写一个测试函数",
		"priority": "high",
	})
	if !strings.Contains(res, "新任务已成功入队") || !strings.Contains(res, "测试新增任务") {
		t.Fatalf("taskq_create failed: %s", res)
	}
}

func TestCardMutations(t *testing.T) {
	_, reg := newMgmtTestEngine(t)

	// Native Add & Remove. The command must exist on every host this test
	// runs on — PruneUnavailableNative drops abilities whose binary is not
	// on PATH, and CI runners have no ffmpeg. git ships on every platform
	// this suite gates (ubuntu/macos/windows runners and dev machines).
	outNative := runMgmtTool(t, reg, "card_native_add", map[string]any{
		"id":          "test:gitprobe",
		"description": "探测 Git 版本",
		"command":     "git",
		"args":        []any{"--version"},
	})
	if !strings.Contains(outNative, "已成功添加") {
		t.Fatalf("card_native_add output: %s", outNative)
	}
	show1 := runMgmtTool(t, reg, "card_show", nil)
	if !strings.Contains(show1, "test:gitprobe") {
		t.Fatalf("card_show missing test:gitprobe:\n%s", show1)
	}
	outNativeRm := runMgmtTool(t, reg, "card_native_remove", map[string]any{
		"id": "test:gitprobe",
	})
	if !strings.Contains(outNativeRm, "已成功删除") {
		t.Fatalf("card_native_remove output: %s", outNativeRm)
	}

	// Agent Add, Set & Remove
	outAgent := runMgmtTool(t, reg, "card_agent_add", map[string]any{
		"name":         "test_agent",
		"adapter":      "test_agent.py",
		"capabilities": []any{"code:modify", "code:review"},
		"cost_tier":    "mid",
	})
	if !strings.Contains(outAgent, "已成功注册") {
		t.Fatalf("card_agent_add output: %s", outAgent)
	}
	outAgentSet := runMgmtTool(t, reg, "card_agent_set", map[string]any{
		"name":      "test_agent",
		"cost_tier": "high",
	})
	if !strings.Contains(outAgentSet, "已成功更新") {
		t.Fatalf("card_agent_set output: %s", outAgentSet)
	}
	show2 := runMgmtTool(t, reg, "card_show", nil)
	if !strings.Contains(show2, "test_agent") || !strings.Contains(show2, "成本 high") {
		t.Fatalf("card_show missing updated test_agent:\n%s", show2)
	}
	outAgentRm := runMgmtTool(t, reg, "card_agent_remove", map[string]any{
		"name": "test_agent",
	})
	if !strings.Contains(outAgentRm, "已成功注销") {
		t.Fatalf("card_agent_remove output: %s", outAgentRm)
	}

	// Manual Add & Remove
	outMan := runMgmtTool(t, reg, "card_manual_add", map[string]any{
		"id":     "manual:test",
		"notify": "webhook:https://example.com",
	})
	if !strings.Contains(outMan, "已成功添加") {
		t.Fatalf("card_manual_add output: %s", outMan)
	}
	outManRm := runMgmtTool(t, reg, "card_manual_remove", map[string]any{
		"id": "manual:test",
	})
	if !strings.Contains(outManRm, "已成功删除") {
		t.Fatalf("card_manual_remove output: %s", outManRm)
	}
}

func TestProjectManagement(t *testing.T) {
	_, reg := newMgmtTestEngine(t)

	// Create project
	createOut := runMgmtTool(t, reg, "project_create", map[string]any{
		"name":        "alpha_proj",
		"work_dir":    "/tmp/alpha",
		"description": "Alpha 项目",
		"enter":       true,
	})
	if !strings.Contains(createOut, "创建成功") || !strings.Contains(createOut, "alpha_proj") {
		t.Fatalf("project_create: %s", createOut)
	}

	// List projects
	listOut := runMgmtTool(t, reg, "project_list", nil)
	if !strings.Contains(listOut, "alpha_proj") || !strings.Contains(listOut, "当前处于激活状态的项目为：alpha_proj") {
		t.Fatalf("project_list: %s", listOut)
	}

	// Exit project
	exitOut := runMgmtTool(t, reg, "project_exit", nil)
	if !strings.Contains(exitOut, "全局无项目状态") {
		t.Fatalf("project_exit: %s", exitOut)
	}

	// Enter project again
	enterOut := runMgmtTool(t, reg, "project_enter", map[string]any{
		"name": "alpha_proj",
	})
	if !strings.Contains(enterOut, "已切换进入项目 alpha_proj") {
		t.Fatalf("project_enter: %s", enterOut)
	}
}

func TestNodeRemove(t *testing.T) {
	e, reg := newMgmtTestEngine(t)

	// Removing self node should fail
	tool, _ := reg.Lookup("node_remove")
	if _, err := tool.Run(context.Background(), map[string]any{"node_id": e.selfNodeID()}); err == nil {
		t.Error("node_remove(self) must error, got nil")
	}

	// Add an offline peer node
	peerCard := ledger.Card{Device: "offline-peer"}
	if err := ledger.Register(e.db, peerCard, "peer-node-1", 1); err != nil {
		t.Fatalf("register peer: %v", err)
	}
	// Mark it offline
	if _, err := e.db.Exec(`UPDATE employee_cache SET status = 'offline' WHERE id = 'peer-node-1'`); err != nil {
		t.Fatalf("mark offline: %v", err)
	}

	res := runMgmtTool(t, reg, "node_remove", map[string]any{"node_id": "peer-node-1"})
	if !strings.Contains(res, "已成功从网络拓扑中移除节点") {
		t.Fatalf("node_remove failed: %s", res)
	}
}

func TestReminderDelete(t *testing.T) {
	_, reg := newMgmtTestEngine(t)

	// Set reminder
	resSet := runMgmtTool(t, reg, "reminder_set", map[string]any{
		"message":       "下午开会",
		"after_minutes": float64(10),
	})
	if !strings.Contains(resSet, "已设置提醒 #1") {
		t.Fatalf("reminder_set: %s", resSet)
	}

	// Delete reminder
	resDel := runMgmtTool(t, reg, "reminder_delete", map[string]any{"id": 1})
	if !strings.Contains(resDel, "已成功删除提醒 #1") {
		t.Fatalf("reminder_delete: %s", resDel)
	}

	// Delete again should report not found
	resDel2 := runMgmtTool(t, reg, "reminder_delete", map[string]any{"id": 1})
	if !strings.Contains(resDel2, "未找到 ID 为 #1 的提醒") {
		t.Fatalf("reminder_delete not found: %s", resDel2)
	}
}

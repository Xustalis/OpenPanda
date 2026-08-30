package askengine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/storage"
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

	e := &Engine{
		cfg:      cfg,
		db:       db,
		cardPath: "/cards/capabilities.yaml",
	}
	e.registry = buildToolRegistry(e, memory.NewHermes(t.TempDir()), nil, nil)
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
		t.Fatalf("run %s: %v", name, err)
	}
	return out
}

func TestMgmtToolsRegistered(t *testing.T) {
	_, reg := newMgmtTestEngine(t)
	for _, name := range []string{"system_status", "card_list", "card_show", "taskq_list", "taskq_show"} {
		tool, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("management tool %s not registered", name)
		}
		if tool.Tier != 1 {
			t.Errorf("tool %s tier = %d, want 1 (read-only)", name, tool.Tier)
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

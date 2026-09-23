package askengine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/defense"
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

	// Reads tell the model what is true, and stay Tier 1. Anything that edits a
	// capability card is Tier 2: a card entry carries both a command and its
	// tier, so writing one is editing the authorization basis itself. This list
	// used to assert tier 1 across all twenty-two tools — which is exactly how
	// the hole stayed pinned in place, since widening a card let the model
	// widen the gate it was about to walk through.
	tier1 := []string{
		"system_status", "card_list", "card_show", "taskq_list", "taskq_show",
		"taskq_cancel", "taskq_priority", "taskq_move", "taskq_reject",
		"card_rescan",
		"project_list", "project_create", "project_enter", "project_exit",
		"node_remove", "reminder_delete",
	}
	tier2 := []string{
		"card_native_add", "card_native_remove",
		"card_agent_add", "card_agent_set", "card_agent_remove",
		"card_manual_add", "card_manual_remove",
		// Approving grants the consent the task was parked for; clearing
		// deletes audit history. Both are irreversible-class operations.
		"taskq_approve", "taskq_clear",
	}

	for _, group := range []struct {
		want  int
		names []string
	}{
		{defense.TierReversible, tier1},
		{defense.TierIrreversible, tier2},
	} {
		for _, name := range group.names {
			tool, ok := reg.Lookup(name)
			if !ok {
				t.Fatalf("management tool %s not registered", name)
			}
			if tool.Tier != group.want {
				t.Errorf("tool %s tier = %d, want %d", name, tool.Tier, group.want)
			}
			if tool.Description == "" {
				t.Errorf("tool %s has no description", name)
			}
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

// seedReviewTask drives a fresh task into review with the given approval
// disposition — the fixture row the approval-tool tests act on.
func seedReviewTask(t *testing.T, store *core.TaskStore, title string, disposition core.ApprovalDisposition) core.Task {
	t.Helper()
	ctx := context.Background()
	task, err := store.Create(ctx, "", "proj", title, "test-node", nil)
	if err != nil {
		t.Fatalf("create %s: %v", title, err)
	}
	for _, step := range []func() error{
		func() error { return store.Queue(ctx, task.TaskID, "test-node") },
		func() error { return store.Dispatch(ctx, task.TaskID, "test-node", "test-node") },
		func() error { return store.Accept(ctx, task.TaskID, "test-node") },
	} {
		if err := step(); err != nil {
			t.Fatalf("drive %s: %v", title, err)
		}
	}
	var err2 error
	if disposition == core.ApprovalAcceptWork {
		err2 = store.PauseWithResult(ctx, task.TaskID, "test-node", map[string]any{
			"ok": true, "result": "成果已产出", "exit_code": 0,
		})
	} else {
		err2 = store.PauseWithDisposition(ctx, task.TaskID, "test-node", "parked", disposition)
	}
	if err2 != nil {
		t.Fatalf("pause %s: %v", title, err2)
	}
	return task
}

// TestTaskqApproveAcceptsCompletedWork is the incident's approval half: a
// task that already executed parks in review with accept_work, and the model
// had no way to accept it. taskq_approve must take it to done.
func TestTaskqApproveAcceptsCompletedWork(t *testing.T) {
	e, reg := newMgmtTestEngine(t)
	store := core.NewTaskStore(e.db, nil)
	review := seedReviewTask(t, store, "待验收的已执行任务", core.ApprovalAcceptWork)

	out := runMgmtTool(t, reg, "taskq_approve", map[string]any{"task_id": review.TaskID})
	if !strings.Contains(out, "验收") {
		t.Fatalf("taskq_approve output: %s", out)
	}
	got, err := store.Get(context.Background(), review.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != core.StateDone {
		t.Fatalf("state = %s, want done", got.State)
	}
}

// TestTaskqApproveNeedsChangedInputRefuses guards the other disposition:
// approval alone cannot fix bad input, so the tool must refuse with guidance
// and leave the task parked.
func TestTaskqApproveNeedsChangedInputRefuses(t *testing.T) {
	e, reg := newMgmtTestEngine(t)
	store := core.NewTaskStore(e.db, nil)
	review := seedReviewTask(t, store, "输入有误的待审批任务", core.ApprovalNeedsChangedInput)

	out := runMgmtTool(t, reg, "taskq_approve", map[string]any{"task_id": review.TaskID})
	if !strings.Contains(out, "不能仅靠批准继续") {
		t.Fatalf("taskq_approve output: %s", out)
	}
	got, err := store.Get(context.Background(), review.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != core.StateReview {
		t.Fatalf("state = %s, want still review", got.State)
	}
}

func TestTaskqApproveNonReviewErrors(t *testing.T) {
	_, reg := newMgmtTestEngine(t)
	list := runMgmtTool(t, reg, "taskq_list", map[string]any{"filter": "queued"})
	var queuedID string
	for _, line := range strings.Split(list, "\n") {
		if strings.Contains(line, "还在排队的任务") {
			queuedID = strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "- "))[0]
		}
	}
	if queuedID == "" {
		t.Fatal("no queued task in fixture")
	}
	tool, _ := reg.Lookup("taskq_approve")
	if _, err := tool.Run(context.Background(), map[string]any{"task_id": queuedID}); err == nil ||
		!strings.Contains(err.Error(), "不在待审批") {
		t.Fatalf("taskq_approve(queued) err = %v, want 不在待审批", err)
	}
}

func TestTaskqReject(t *testing.T) {
	e, reg := newMgmtTestEngine(t)
	store := core.NewTaskStore(e.db, nil)
	review := seedReviewTask(t, store, "成果不合格的待审批任务", core.ApprovalAcceptWork)

	out := runMgmtTool(t, reg, "taskq_reject", map[string]any{
		"task_id": review.TaskID, "reason": "成果不合格",
	})
	if !strings.Contains(out, "已拒绝") || !strings.Contains(out, "成果不合格") {
		t.Fatalf("taskq_reject output: %s", out)
	}
	got, err := store.Get(context.Background(), review.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != core.StateFailed {
		t.Fatalf("state = %s, want failed", got.State)
	}
}

// TestTaskqListShowsDisposition is the visibility half of the incident:
// "待审批" alone hid that approving would ACCEPT finished work, so the model
// cancelled it instead. Review rows must carry the disposition label.
func TestTaskqListShowsDisposition(t *testing.T) {
	e, reg := newMgmtTestEngine(t)
	store := core.NewTaskStore(e.db, nil)
	seedReviewTask(t, store, "待验收的已执行任务", core.ApprovalAcceptWork)

	list := runMgmtTool(t, reg, "taskq_list", map[string]any{"filter": "review"})
	if !strings.Contains(list, "批准=验收已有成果→完成") {
		t.Fatalf("review list missing disposition label:\n%s", list)
	}
	show := runMgmtTool(t, reg, "taskq_show", map[string]any{"task_id": storeTaskID(t, store, "待验收的已执行任务")})
	if !strings.Contains(show, "审批处置：批准=验收已有成果→完成") {
		t.Fatalf("taskq_show missing disposition line:\n%s", show)
	}
}

func storeTaskID(t *testing.T, store *core.TaskStore, title string) string {
	t.Helper()
	ts, err := store.ListByState(context.Background(), core.StateReview)
	if err != nil {
		t.Fatalf("list review: %v", err)
	}
	for _, task := range ts {
		if task.Title == title {
			return task.TaskID
		}
	}
	t.Fatalf("no review task titled %q", title)
	return ""
}

// TestTaskqCancelBatch covers the cleanup shape that burned the round budget:
// one call cancelling several ids must cancel them all.
func TestTaskqCancelBatch(t *testing.T) {
	e, reg := newMgmtTestEngine(t)
	store := core.NewTaskStore(e.db, nil)
	ctx := context.Background()
	var ids []string
	for _, title := range []string{"批量取消甲", "批量取消乙"} {
		task, err := store.Create(ctx, "", "proj", title, "test-node", nil)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := store.Queue(ctx, task.TaskID, "test-node"); err != nil {
			t.Fatalf("queue: %v", err)
		}
		ids = append(ids, task.TaskID)
	}
	out := runMgmtTool(t, reg, "taskq_cancel", map[string]any{
		"task_ids": []any{ids[0], ids[1], "task-nonexistent"},
	})
	if !strings.Contains(out, "2 个成功取消") || !strings.Contains(out, "解析失败") {
		t.Fatalf("batch cancel output: %s", out)
	}
	for _, id := range ids {
		got, err := store.Get(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if got.State != core.StateCancelled {
			t.Fatalf("task %s state = %s, want cancelled", id, got.State)
		}
	}
}

// TestTaskqMoveRejectsNonQueued guards the reorder tools: mutating seq on a
// finished task used to succeed silently.
func TestTaskqMoveRejectsNonQueued(t *testing.T) {
	_, reg := newMgmtTestEngine(t)
	list := runMgmtTool(t, reg, "taskq_list", map[string]any{"filter": "done"})
	var doneID string
	for _, line := range strings.Split(list, "\n") {
		if strings.Contains(line, "已完成的任务") {
			doneID = strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "- "))[0]
		}
	}
	if doneID == "" {
		t.Fatal("no done task in fixture")
	}
	for _, name := range []string{"taskq_move", "taskq_priority"} {
		tool, _ := reg.Lookup(name)
		args := map[string]any{"task_id": doneID}
		if name == "taskq_move" {
			args["seq"] = 1
		} else {
			args["priority"] = "high"
		}
		if _, err := tool.Run(context.Background(), args); err == nil ||
			!strings.Contains(err.Error(), "只有排队中的任务") {
			t.Fatalf("%s(done task) err = %v, want reorder guard", name, err)
		}
	}
}

// TestTaskqClear covers all three scopes: history deletes only terminal rows,
// review cancels then deletes parked rows, and an invalid scope errors.
func TestTaskqClear(t *testing.T) {
	e, reg := newMgmtTestEngine(t)
	store := core.NewTaskStore(e.db, nil)
	ctx := context.Background()

	out := runMgmtTool(t, reg, "taskq_clear", map[string]any{"scope": "history"})
	if !strings.Contains(out, "已删除 2 条终态任务记录") {
		t.Fatalf("clear history output: %s", out)
	}
	// The queued row survives a history clear.
	if n := countTasksWithTitlePrefix(t, e, "还在排队的任务"); n != 1 {
		t.Fatalf("queued task lost to history clear")
	}

	review := seedReviewTask(t, store, "待审批清理对象", core.ApprovalAcceptWork)
	out = runMgmtTool(t, reg, "taskq_clear", map[string]any{"scope": "review"})
	if !strings.Contains(out, "已清理待审批队列") {
		t.Fatalf("clear review output: %s", out)
	}
	if _, err := store.Get(ctx, review.TaskID); err == nil {
		t.Fatal("review row survived clear review")
	}

	tool, _ := reg.Lookup("taskq_clear")
	if _, err := tool.Run(context.Background(), map[string]any{"scope": "bogus"}); err == nil {
		t.Fatal("taskq_clear(bogus scope) must error")
	}

	out = runMgmtTool(t, reg, "taskq_clear", map[string]any{"scope": "all"})
	if !strings.Contains(out, "已清空队列") {
		t.Fatalf("clear all output: %s", out)
	}
	remaining, err := store.ListByState(ctx, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("clear all left %d rows", len(remaining))
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
		"adapter":      "opencode.py",
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

func TestCardAgentAddAutoInit(t *testing.T) {
	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	cfg := &config.Config{}
	cfg.Node.Name = "test-auto-node"
	cfg.Node.Kind = "physical"
	cfg.Node.CardPath = filepath.Join(root, "auto", "capabilities.yaml")
	cfg.Model.Model = "test-model"

	rem := reminders.NewStore(db)
	e := &Engine{
		cfg:      cfg,
		db:       db,
		cardPath: cfg.Node.CardPath,
		schedCtx: context.Background(),
		remind:   rem,
	}
	e.registry = buildToolRegistry(e, memory.NewHermes(t.TempDir()), nil, rem)

	// Ensure the file does not exist initially
	if _, err := os.Stat(cfg.Node.CardPath); !os.IsNotExist(err) {
		t.Fatalf("card file should not exist yet")
	}

	// Calling card_agent_add with no pre-existing card should auto-initialize and succeed
	outAgent := runMgmtTool(t, e.registry, "card_agent_add", map[string]any{
		"name":         "brand_new_agent",
		"adapter":      "opencode.py",
		"capabilities": []any{"coding", "cli", "shell"},
		"cost_tier":    "high",
	})
	if !strings.Contains(outAgent, "已成功注册") {
		t.Fatalf("card_agent_add new agent failed: %s", outAgent)
	}

	// Calling card_agent_add again with the same name should update without error
	outAgentUpdate := runMgmtTool(t, e.registry, "card_agent_add", map[string]any{
		"name":         "brand_new_agent",
		"adapter":      "opencode.py",
		"capabilities": []any{"coding", "cli", "shell", "review"},
		"cost_tier":    "low",
	})
	if !strings.Contains(outAgentUpdate, "已更新配置") {
		t.Fatalf("card_agent_add update failed: %s", outAgentUpdate)
	}

	// card_show should now successfully show brand_new_agent and the node
	showOut := runMgmtTool(t, e.registry, "card_show", nil)
	if !strings.Contains(showOut, "brand_new_agent") {
		t.Fatalf("card_show output missing brand_new_agent: %s", showOut)
	}

	// Verify scheduler was dynamically initialized and holds the agent
	if e.sched == nil {
		t.Fatalf("expected scheduler to be dynamically initialized after card_agent_add")
	}
	if _, ok := e.sched.Card().Agents["brand_new_agent"]; !ok {
		t.Fatalf("brand_new_agent not found in scheduler card: %v", e.sched.Card().Agents)
	}

	// Verify tryAutoInitScheduler works if scheduler is cleared
	e.sched = nil
	e.tryAutoInitScheduler()
	if e.sched == nil {
		t.Fatalf("expected tryAutoInitScheduler to recreate scheduler")
	}
	if _, ok := e.sched.Card().Agents["brand_new_agent"]; !ok {
		t.Fatalf("brand_new_agent missing after tryAutoInitScheduler: %v", e.sched.Card().Agents)
	}
}

// TestRegistryResolvesAgentAdapter is the regression for the failure that made
// "配置 dsh" impossible: asked to register a CLI it had no facts about, the
// entry model invented an adapter filename and wrote a card entry pointing at a
// file that does not exist, then reported success. The registry holds the real
// mapping (binary dsh → agent deepseek_harness → adapter deepseek_harness.py) and
// is now consulted.
func TestRegistryResolvesAgentAdapter(t *testing.T) {
	name := "dsh"
	ag := ledger.Agent{Adapter: "dsh.py", Capabilities: []string{"coding"}}
	if err := resolveAgentFromRegistry(&name, &ag); err != nil {
		t.Fatalf("resolveAgentFromRegistry(dsh) = %v, want nil", err)
	}
	if name != "deepseek_harness" {
		t.Errorf("name = %q, want deepseek_harness (the registry key routing and `panda agents` agree on)", name)
	}
	if ag.Adapter != "deepseek_harness.py" {
		t.Errorf("adapter = %q, want deepseek_harness.py (the invented dsh.py must not survive)", ag.Adapter)
	}
	if ag.Tier != agents.TierAutoApproved {
		t.Errorf("tier = %d, want %d", ag.Tier, agents.TierAutoApproved)
	}
	if len(ag.Capabilities) == 0 {
		t.Error("capabilities should be carried over from the registry")
	}
	// The filler must not overwrite a deliberate choice.
	name2, ag2 := "dsh", ledger.Agent{Capabilities: []string{"only-this"}}
	if err := resolveAgentFromRegistry(&name2, &ag2); err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if len(ag2.Capabilities) != 1 || ag2.Capabilities[0] != "only-this" {
		t.Errorf("caller-supplied capabilities were overwritten: %v", ag2.Capabilities)
	}
}

// TestUnknownAgentNeedsARealAdapter guards the other half: an agent the registry
// does not know is only accepted with an adapter that actually exists.
func TestUnknownAgentNeedsARealAdapter(t *testing.T) {
	name := "mystery_cli"
	ag := ledger.Agent{}
	if err := resolveAgentFromRegistry(&name, &ag); err == nil {
		t.Fatal("an unknown agent with no adapter must be rejected")
	} else if !strings.Contains(err.Error(), "内置注册表") {
		t.Errorf("the rejection should point at the registry: %v", err)
	}

	ag = ledger.Agent{Adapter: "opencode.py"}
	if err := resolveAgentFromRegistry(&name, &ag); err != nil {
		t.Fatalf("a real adapter must be accepted: %v", err)
	}
}

// TestCheckAdapterExistsRejectsMissingFile pins why the card write is guarded:
// an entry pointing at a missing script advertises an agent that can never
// start, so routing sends work to it and the task dies at spawn time.
func TestCheckAdapterExistsRejectsMissingFile(t *testing.T) {
	if err := checkAdapterExists("dsh.py"); err == nil {
		t.Fatal("a non-existent adapter must be rejected")
	} else if !strings.Contains(err.Error(), "不存在") {
		t.Errorf("error should say the adapter is missing: %v", err)
	}
	if err := checkAdapterExists("opencode.py"); err != nil {
		t.Errorf("a shipped adapter must be accepted: %v", err)
	}
	if err := checkAdapterExists(""); err == nil {
		t.Error("an empty adapter must be rejected")
	}
}

// TestCardRescanRegistersInstalledAgents covers the tool that gives
// "帮我配置 dsh" a correct action: detect what is installed and add only what the
// card is missing, leaving existing entries (and their tier) untouched.
func TestCardRescanRegistersInstalledAgents(t *testing.T) {
	dir := t.TempDir()
	cardPath := filepath.Join(dir, "capabilities.yaml")
	// Seed a card that already declares one agent, with a tier the scan must not
	// widen.
	if err := os.WriteFile(cardPath, []byte("device: test-node\nagents:\n    opencode:\n        adapter: opencode.py\n        tier: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Provide a fake "claude" binary on PATH so the rescan detects at least one
	// new agent even on CI runners where no real agent CLIs are installed.
	fakeBin := t.TempDir()
	stubName := "claude"
	if runtime.GOOS == "windows" {
		stubName = "claude.exe"
	}
	if err := os.WriteFile(filepath.Join(fakeBin, stubName), []byte("#!/bin/sh\necho stub\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+os.Getenv("PATH"))
	// The rescan hot-reloads the card, so the engine needs the store the
	// scheduler is built from — the same setup TestCardAgentAddAutoInit uses.
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg := &config.Config{}
	cfg.Node.Name = "test-rescan-node"
	cfg.Node.Kind = "physical"
	cfg.Node.CardPath = cardPath
	cfg.Model.Model = "test-model"
	rem := reminders.NewStore(db)
	e := &Engine{
		cfg:      cfg,
		db:       db,
		cardPath: cardPath,
		schedCtx: context.Background(),
		remind:   rem,
	}
	e.registry = buildToolRegistry(e, memory.NewHermes(t.TempDir()), nil, rem)

	out, err := e.cardRescan(context.Background())
	if err != nil {
		t.Fatalf("cardRescan: %v", err)
	}
	card, err := ledger.LoadCard(cardPath)
	if err != nil {
		t.Fatalf("load card: %v", err)
	}
	if len(card.Agents) <= 1 {
		t.Fatalf("rescan added nothing (%q); installed agents were not registered", out)
	}
	if ag, ok := card.Agents["opencode"]; !ok || ag.Tier != 2 {
		t.Errorf("an existing entry must keep its tier: %+v", ag)
	}
	for _, ag := range card.Agents {
		if _, err := os.Stat(filepath.Join(commander.AdapterDir(), ag.Adapter)); err != nil {
			t.Errorf("rescan wrote an entry whose adapter is missing: %q", ag.Adapter)
		}
	}

	// Idempotent: a second scan has nothing left to add.
	out2, err := e.cardRescan(context.Background())
	if err != nil {
		t.Fatalf("second cardRescan: %v", err)
	}
	if !strings.Contains(out2, "没有新增") {
		t.Errorf("a second rescan should be a no-op, got %q", out2)
	}
}

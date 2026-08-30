package askengine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/version"
)

// registerMgmtTools adds the management tool family v1 — the read half of
// "openpanda 调用 openpanda": every surface the user can reach (panda status,
// the capability-card directory, the queue board) the entry model can reach
// too, so a plain-language "现在什么情况" resolves to live data instead of
// guesses. All five tools are queries and Tier 1 by construction; anything
// that mutates node/card/task state stays behind its tier-2 gate.
//
// The tools take the Engine and dereference it lazily inside Run: New builds
// the registry before the scheduler exists (and an engine without CardPath
// never gets one), so the engine's fields are read at call time, never at
// registration time.
func registerMgmtTools(reg *entry.Registry, e *Engine) {
	reg.Register(entry.Tool{
		Name:        "system_status",
		Description: "查看 OpenPanda 整体运行状态：版本、本节点身份、入口模型、能力卡加载情况、设备网络在线状况、各状态任务数量。回答“现在什么情况/几台设备在线/队列里有多少任务”等问题的第一步。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return e.systemStatus(ctx)
		},
	})

	reg.Register(entry.Tool{
		Name:        "card_list",
		Description: "列出设备网络里所有节点及其能力卡概要（设备名、类型、在线状态、能力清单）。需要为任务挑选合适设备时先看这里。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return e.cardList(ctx)
		},
	})

	reg.Register(entry.Tool{
		Name:        "card_show",
		Description: "查看一个节点的能力卡详情：原生能力（含执行命令）、agent 配置（能力/擅长/不擅长/成本档）、人工能力、容量与资源档案。name 填设备名或节点 ID，留空看本机。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "设备名或节点 ID（留空 = 本机）"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			return e.cardShow(ctx, strings.TrimSpace(name))
		},
	})

	reg.Register(entry.Tool{
		Name:        "taskq_list",
		Description: "查看任务队列。filter 可选：active（排队/已派发/运行中，默认）、review（待审批）、history（已完成/失败/已取消）、all（全部），或直接填某个状态名（如 running）。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"filter": map[string]any{"type": "string", "description": "active / review / history / all / 状态名"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			filter, _ := args["filter"].(string)
			return e.taskqList(ctx, strings.TrimSpace(filter))
		},
	})

	reg.Register(entry.Tool{
		Name:        "taskq_show",
		Description: "查看一个任务的详情：标题、状态、负责节点、意图、能力要求、结果摘要、事件时间线。task_id 填任务 ID。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "任务 ID"},
			},
			"required": []string{"task_id"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			id, _ := args["task_id"].(string)
			return e.taskqShow(ctx, strings.TrimSpace(id))
		},
	})
}

// systemStatus renders the one-screen overview behind system_status: the
// same facts `panda status` shows, folded into text the model can quote.
func (e *Engine) systemStatus(ctx context.Context) (string, error) {
	nodes, err := ledger.Query(e.db, "", "")
	if err != nil {
		return "", fmt.Errorf("查询设备网络：%w", err)
	}
	online := 0
	for _, n := range nodes {
		if n.Status == "online" {
			online++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "OpenPanda 状态\n版本：%s", version.Version)
	fmt.Fprintf(&b, "\n本节点：%s（%s）", e.cfg.Node.Name, e.cfg.Node.Kind)
	if e.cfg.Model.Model != "" {
		fmt.Fprintf(&b, "\n入口模型：%s", e.cfg.Model.Model)
	}
	if e.cardPath != "" {
		fmt.Fprintf(&b, "\n能力卡：已加载（%s）", e.cardPath)
	} else {
		b.WriteString("\n能力卡：未加载（本会话无法派发任务）")
	}
	fmt.Fprintf(&b, "\n设备网络：%d 台设备，%d 在线 / %d 离线", len(nodes), online, len(nodes)-online)

	store := core.NewTaskStore(e.db, nil)
	var counts []string
	for _, st := range []string{
		core.StateQueued, core.StateDispatched, core.StateWaitingCtx, core.StateRunning,
		core.StateReview, core.StateDone, core.StateFailed,
	} {
		ts, err := store.ListByState(ctx, st)
		if err != nil {
			return "", fmt.Errorf("统计任务：%w", err)
		}
		if len(ts) > 0 {
			counts = append(counts, fmt.Sprintf("%s %d", zhTaskState(st), len(ts)))
		}
	}
	if len(counts) == 0 {
		b.WriteString("\n任务队列：空")
	} else {
		fmt.Fprintf(&b, "\n任务队列：%s", strings.Join(counts, "、"))
	}
	return b.String(), nil
}

// cardList renders the fleet's capability cards — every ledger row with its
// ability summary, the device list the model routes against.
func (e *Engine) cardList(ctx context.Context) (string, error) {
	nodes, err := ledger.Query(e.db, "", "")
	if err != nil {
		return "", fmt.Errorf("查询设备网络：%w", err)
	}
	if len(nodes) == 0 {
		return "设备网络为空：还没有任何节点注册能力卡。", nil
	}
	selfID := e.selfNodeID()
	var b strings.Builder
	fmt.Fprintf(&b, "能力卡目录（%d 台设备）", len(nodes))
	for _, n := range nodes {
		mark := ""
		if n.ID == selfID {
			mark = " ←本机"
		}
		fmt.Fprintf(&b, "\n- %s（%s）[%s]%s", n.Name, n.NodeKind, n.Status, mark)
		if abs := n.Abilities(); len(abs) > 0 {
			fmt.Fprintf(&b, "\n  能力：%s", strings.Join(abs, "、"))
		}
	}
	return b.String(), nil
}

// cardShow renders one node's card in full: the executable detail the
// summary lists elide — commands behind native abilities, adapter and cost
// behind agents — plus capacity and the declared hardware profile.
func (e *Engine) cardShow(ctx context.Context, name string) (string, error) {
	nodes, err := ledger.Query(e.db, "", "")
	if err != nil {
		return "", fmt.Errorf("查询设备网络：%w", err)
	}
	var target ledger.Node
	if name == "" {
		selfID := e.selfNodeID()
		for _, n := range nodes {
			if n.ID == selfID {
				target = n
				break
			}
		}
		if target.ID == "" {
			return "", fmt.Errorf("本机能力卡未注册（未加载能力卡或守护进程未运行）")
		}
	} else {
		for _, n := range nodes {
			if n.ID == name || strings.EqualFold(n.Name, name) {
				target = n
				break
			}
		}
		if target.ID == "" {
			names := make([]string, 0, len(nodes))
			for _, n := range nodes {
				names = append(names, n.Name)
			}
			return "", fmt.Errorf("未找到设备 %q；可用设备：%s", name, strings.Join(names, "、"))
		}
	}

	n := target
	var b strings.Builder
	fmt.Fprintf(&b, "能力卡：%s（%s）[%s]", n.Name, n.NodeKind, n.Status)
	if n.Chip != "" {
		fmt.Fprintf(&b, "\n芯片：%s", n.Chip)
	}
	if n.NodeIdentity != "" {
		fmt.Fprintf(&b, "\n节点标识：%s", n.NodeIdentity)
	}
	if len(n.Native) > 0 {
		b.WriteString("\n原生能力：")
		for _, a := range n.Native {
			cmd := a.Command
			if len(a.Args) > 0 {
				cmd += " " + strings.Join(a.Args, " ")
			}
			tier := ""
			if a.Tier == 2 {
				tier = "（tier-2 需授权）"
			}
			fmt.Fprintf(&b, "\n- %s：%s%s", a.ID, cmd, tier)
		}
	}
	if len(n.Agents) > 0 {
		b.WriteString("\nAgent：")
		names := make([]string, 0, len(n.Agents))
		for name := range n.Agents {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			ag := n.Agents[name]
			fmt.Fprintf(&b, "\n- %s（%s）", name, ag.Adapter)
			if len(ag.Capabilities) > 0 {
				fmt.Fprintf(&b, "：能力 %s", strings.Join(ag.Capabilities, "、"))
			}
			if len(ag.BestAt) > 0 {
				fmt.Fprintf(&b, "；擅长 %s", strings.Join(ag.BestAt, "、"))
			}
			if len(ag.NotFor) > 0 {
				fmt.Fprintf(&b, "；不适合 %s", strings.Join(ag.NotFor, "、"))
			}
			if ag.CostTier != "" {
				fmt.Fprintf(&b, "；成本 %s", ag.CostTier)
			}
			if ag.Tier == 2 {
				b.WriteString("（tier-2 需授权）")
			}
		}
	}
	if len(n.Manual) > 0 {
		b.WriteString("\n人工能力：")
		for _, m := range n.Manual {
			fmt.Fprintf(&b, "\n- %s（通知：%s）", m.ID, m.Notify)
		}
	}
	fmt.Fprintf(&b, "\n容量：%d 核 / %d GiB RAM，最多 %d 个并发任务（当前 %d 个）",
		n.Capacity.CPUCores, n.Capacity.RAMGB, n.Capacity.MaxConcurrent, n.Capacity.CurrentTasks)
	if n.ResourceProfile.Declared() {
		p := n.ResourceProfile
		var parts []string
		if p.CPU > 0 {
			parts = append(parts, fmt.Sprintf("CPU %d 核", p.CPU))
		}
		if p.RAMGB > 0 {
			parts = append(parts, fmt.Sprintf("RAM %d GiB", p.RAMGB))
		}
		if p.GPUVRAMGB > 0 {
			parts = append(parts, fmt.Sprintf("GPU %d GiB", p.GPUVRAMGB))
		}
		if p.GPUVRAMGB == ledger.GPUVRAMUnknown {
			parts = append(parts, "GPU 显存未知")
		}
		if p.DurationHint != "" {
			parts = append(parts, "时长 "+p.DurationHint)
		}
		fmt.Fprintf(&b, "\n资源档案：%s", strings.Join(parts, " / "))
	}
	return b.String(), nil
}

// taskqList renders the queue board: tasks grouped by the caller's filter,
// oldest first — the same order the panel's board shows them in.
func (e *Engine) taskqList(ctx context.Context, filter string) (string, error) {
	states, label := taskqStates(filter)
	store := core.NewTaskStore(e.db, nil)
	var tasks []core.Task
	for _, st := range states {
		ts, err := store.ListByState(ctx, st)
		if err != nil {
			return "", fmt.Errorf("查询任务：%w", err)
		}
		tasks = append(tasks, ts...)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt < tasks[j].CreatedAt })
	if len(tasks) == 0 {
		return fmt.Sprintf("任务队列（%s）：空。", label), nil
	}
	const maxRows = 30
	rows := min(maxRows, len(tasks))
	var b strings.Builder
	fmt.Fprintf(&b, "任务队列（%s）共 %d 个", label, len(tasks))
	if len(tasks) > rows {
		fmt.Fprintf(&b, "，仅显示最早的 %d 个", rows)
	}
	for _, t := range tasks[:rows] {
		// The full id travels on the row: the model must be able to quote it
		// back into taskq_show, and short prefixes collide within one batch
		// (the id's leading segment is a timestamp).
		fmt.Fprintf(&b, "\n- %s %s — %s（负责节点 %s）",
			t.TaskID, t.Title, zhTaskState(t.State), t.OwnerNode)
	}
	return b.String(), nil
}

// taskqShow renders one task's detail: the fields the panel's task drawer
// shows, plus the event timeline the audit chain recorded.
func (e *Engine) taskqShow(ctx context.Context, taskID string) (string, error) {
	if taskID == "" {
		return "", fmt.Errorf("task_id 不能为空")
	}
	store := core.NewTaskStore(e.db, nil)
	taskID, err := e.resolveTaskID(ctx, store, taskID)
	if err != nil {
		return "", fmt.Errorf("读取任务 %s：%w", taskID, err)
	}
	t, err := store.Get(ctx, taskID)
	if err != nil {
		return "", fmt.Errorf("读取任务 %s：%w", taskID, err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "任务 %s\n标题：%s\n状态：%s", t.TaskID, t.Title, zhTaskState(t.State))
	fmt.Fprintf(&b, "\n负责节点：%s", t.OwnerNode)
	fmt.Fprintf(&b, "\n创建：%s / 更新：%s", fmtTime(t.CreatedAt), fmtTime(t.UpdatedAt))
	if t.Intent != "" {
		fmt.Fprintf(&b, "\n意图：%s", t.Intent)
	}
	if len(t.Requires) > 0 {
		fmt.Fprintf(&b, "\n能力要求：%s", strings.Join(t.Requires, "、"))
	}
	if t.Risk != "" {
		fmt.Fprintf(&b, "\n风险等级：%s", t.Risk)
	}
	if t.Authorized {
		b.WriteString("\n授权：已获用户授权（tier-2）")
	}
	if t.ParentID != "" {
		fmt.Fprintf(&b, "\n父任务：%s", t.ParentID)
	}
	if t.PlanID != "" {
		fmt.Fprintf(&b, "\n所属计划：%s（阶段 %s）", t.PlanID, t.StageID)
	}
	if text := resultText(t.ResultJSON); text != "" {
		fmt.Fprintf(&b, "\n结果摘要：\n%s", text)
	}

	evs, err := store.Events(ctx, taskID)
	if err != nil {
		e.logger.Warn("mgmt tool: task events", "task", taskID, "err", err)
		evs = nil
	}
	if len(evs) > 0 {
		const maxEvents = 50
		shown := min(maxEvents, len(evs))
		fmt.Fprintf(&b, "\n事件时间线（%d 条%s）", len(evs), truncSuffix(len(evs), shown))
		for _, ev := range evs[:shown] {
			line := fmt.Sprintf("\n- %s [%s]", fmtTime(ev.TS), ev.Type)
			if d := compactJSON(ev.DataJSON); d != "" {
				line += " " + d
			}
			b.WriteString(line)
		}
	}
	return b.String(), nil
}

// resolveTaskID accepts a full task id or the short prefix taskq_list prints:
// the model quotes what it saw on the row, and the row carries the truncated
// form. A unique prefix resolves; an ambiguous one errors with the
// candidates; no match falls through to the caller's not-found handling.
func (e *Engine) resolveTaskID(ctx context.Context, store *core.TaskStore, id string) (string, error) {
	if _, err := store.Get(ctx, id); err == nil {
		return id, nil
	}
	if len(id) < 4 {
		return "", fmt.Errorf("not found")
	}
	states, _ := taskqStates("all")
	var matches []string
	for _, st := range states {
		ts, err := store.ListByState(ctx, st)
		if err != nil {
			continue
		}
		for _, t := range ts {
			if strings.HasPrefix(t.TaskID, id) {
				matches = append(matches, t.TaskID)
			}
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("not found")
	case 1:
		return matches[0], nil
	default:
		shorts := make([]string, 0, len(matches))
		for _, m := range matches {
			shorts = append(shorts, shortID(m))
		}
		return "", fmt.Errorf("前缀 %s 匹配到 %d 个任务（%s），请用更长的前缀", id, len(matches), strings.Join(shorts, "、"))
	}
}

// selfNodeID is the stable runtime id this node registers its card under —
// the row card_show finds when no name is given. It is derived the same way
// New and the daemon derive it, so all three agree on the row.
func (e *Engine) selfNodeID() string {
	return core.RuntimeNodeID(e.cfg.Node.Name, e.cfg.Node.Kind, e.cfg.Node.EffectiveIdentity())
}

// taskqStates maps the caller's filter word to the task states it selects.
// An unknown filter is treated as a state name verbatim: the store then
// returns nothing and the empty list tells the model the filter was wrong.
func taskqStates(filter string) ([]string, string) {
	switch strings.ToLower(strings.TrimSpace(filter)) {
	case "", "active":
		return []string{core.StateQueued, core.StateDispatched, core.StateWaitingCtx, core.StateRunning}, "进行中"
	case "review":
		return []string{core.StateReview}, "待审批"
	case "history":
		return []string{core.StateDone, core.StateFailed, core.StateCancelled, core.StateExpired}, "历史"
	case "all":
		return []string{
			core.StateSubmitted, core.StateQueued, core.StateDispatched, core.StateWaitingCtx,
			core.StateRunning, core.StateReview, core.StateDone, core.StateFailed,
			core.StateCancelled, core.StateExpired,
		}, "全部"
	default:
		return []string{filter}, filter
	}
}

// zhTaskState renders a wire state as the Chinese label the panels use.
func zhTaskState(state string) string {
	switch state {
	case core.StateSubmitted:
		return "已提交"
	case core.StateQueued:
		return "排队中"
	case core.StateDispatched:
		return "已派发"
	case core.StateWaitingCtx:
		return "等待上下文"
	case core.StateRunning:
		return "运行中"
	case core.StateReview:
		return "待审批"
	case core.StateDone:
		return "已完成"
	case core.StateFailed:
		return "失败"
	case core.StateCancelled:
		return "已取消"
	case core.StateExpired:
		return "已过期"
	}
	return state
}

// resultText pulls the readable part out of a task's stored result payload —
// the "result" string the adapter protocol carries, else stderr, else stdout
// — capped at a digest length. Mirrors the panel's extractResultText.
func resultText(resultJSON string) string {
	if resultJSON == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(resultJSON), &payload); err != nil {
		return ""
	}
	for _, key := range []string{"result", "stderr", "stdout"} {
		if text, _ := payload[key].(string); strings.TrimSpace(text) != "" {
			return excerpt(strings.TrimSpace(text), 800)
		}
	}
	return ""
}

// compactJSON flattens an event's data JSON to one short line: the excerpt
// keeps the head of the payload, which is where the identifying fields live.
func compactJSON(dataJSON string) string {
	s := strings.TrimSpace(dataJSON)
	if s == "" || s == "{}" {
		return ""
	}
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// fmtTime renders a unix timestamp for the local terminal.
func fmtTime(ts int64) string {
	if ts == 0 {
		return "—"
	}
	return time.Unix(ts, 0).Format("01-02 15:04:05")
}

// shortID shortens a task id to its distinguishing prefix for list rows.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// truncSuffix renders the "，仅显示前 N 条" tail of a truncated count.
func truncSuffix(total, shown int) string {
	if total <= shown {
		return ""
	}
	return fmt.Sprintf("，仅显示前 %d 条", shown)
}

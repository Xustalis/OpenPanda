package askengine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/carddetect"
	"github.com/Xustalis/OpenPanda/internal/cardmut"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	projectstore "github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/version"
)

// registerMgmtTools adds the management tool family — "openpanda 调用
// openpanda": every surface the user can reach (panda status, the
// capability-card directory, the queue board) the entry model can reach too,
// so a plain-language "现在什么情况" resolves to live data instead of guesses.
//
// Tier here is a security boundary, not bookkeeping. Reading — status, the
// list/show pairs, rescan, the queue operations — stays Tier 1. Anything that
// edits a capability card is Tier 2, because a card edit *is* an edit to the
// authorization basis: a card entry carries both a command and its tier, so a
// Tier-1 write tool let the model widen the gate it was about to walk through.
// (This comment used to claim "all five tools are queries"; the family grew to
// twenty-one and the invariant went with it. When adding a tool, set the tier
// for what the tool can cause, not for what it mostly does.)
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

	reg.Register(entry.Tool{
		Name: "taskq_cancel",
		Description: "取消任务及其所有子任务树，立即中止执行。task_id 填单个任务 ID 或前缀；task_ids 填 ID 数组可一次取消多个（清理队列时优先用它，比逐个调用省轮次）。" +
			"当任务死循环、卡反爬、超时或不再需要时调用。注意：处于待审批（review）状态的任务会被拒绝——那是留给用户的决定。",
		Tier: defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id":  map[string]any{"type": "string", "description": "要取消的任务 ID 或前缀"},
				"task_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "批量取消的任务 ID 列表（与 task_id 二选一）"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			ids := toStringSlice(args["task_ids"])
			if len(ids) > 0 {
				return e.taskqCancelBatch(ctx, ids)
			}
			id, _ := args["task_id"].(string)
			return e.taskqCancel(ctx, strings.TrimSpace(id))
		},
	})

	reg.Register(entry.Tool{
		Name:        "taskq_priority",
		Description: "修改任务的排队优先级。priority 可选：high（高）、normal（中/普通）、low（低）。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id":  map[string]any{"type": "string", "description": "任务 ID 或前缀"},
				"priority": map[string]any{"type": "string", "description": "优先级：high / normal / low"},
			},
			"required": []string{"task_id", "priority"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			id, _ := args["task_id"].(string)
			prio, _ := args["priority"].(string)
			return e.taskqPriority(ctx, strings.TrimSpace(id), strings.TrimSpace(prio))
		},
	})

	reg.Register(entry.Tool{
		Name:        "taskq_move",
		Description: "调整任务在排队队列中的顺序序号（seq）。调度器按序号升序优先调度。seq 必须为正整数（>= 1）。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "任务 ID 或前缀"},
				"seq":     map[string]any{"type": "integer", "description": "新的顺序序号（正整数）"},
			},
			"required": []string{"task_id", "seq"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			id, _ := args["task_id"].(string)
			var seq int64
			switch v := args["seq"].(type) {
			case float64:
				seq = int64(v)
			case int64:
				seq = v
			case int:
				seq = int64(v)
			}
			return e.taskqMove(ctx, strings.TrimSpace(id), seq)
		},
	})

	reg.Register(entry.Tool{
		Name: "taskq_approve",
		Description: "查询一个待审批（review）任务并提示用户去前台批准。批准是用户本人的决定：模型不得代替批准或拒绝——" +
			"任何方向（approve/reject/cancel/clear）的处置都只能在用户可见的前台界面完成（TUI 的 /approve、/reject，" +
			"或 Web 面板的批准/拒绝按钮）。本工具只返回该任务状态与需要用户操作的提示，不会改动任务。task_id 填任务 ID 或前缀。",
		// Tier 2 keeps the call itself behind consent, but the decisive guard is
		// semantic: the Run below never transitions the task, no matter what
		// authorization the session carries.
		Tier: defense.TierIrreversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "待审批任务 ID 或前缀"},
			},
			"required": []string{"task_id"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			id, _ := args["task_id"].(string)
			return e.taskqApprove(ctx, strings.TrimSpace(id))
		},
	})

	reg.Register(entry.Tool{
		Name: "taskq_reject",
		Description: "查询一个待审批（review）任务并提示用户去前台拒绝。与批准一样，拒绝只能由用户本人在前台完成——" +
			"模型不得静默处置待审批任务。本工具只返回该任务状态与需要用户操作的提示，不会改动任务。task_id 填任务 ID 或前缀。",
		Tier: defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "待审批任务 ID 或前缀"},
				"reason":  map[string]any{"type": "string", "description": "忽略——模型不执行拒绝操作"},
			},
			"required": []string{"task_id"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			id, _ := args["task_id"].(string)
			reason, _ := args["reason"].(string)
			return e.taskqReject(ctx, strings.TrimSpace(id), strings.TrimSpace(reason))
		},
	})

	reg.Register(entry.Tool{
		Name: "taskq_clear",
		Description: "清理任务队列记录。scope 必填：history=删除所有终态任务记录（已完成/失败/已取消/已过期）；" +
			"all=取消所有未完成任务并删除全部任务记录——但存在待审批（review）任务时会拒绝执行，待审批只能由用户逐个决定。" +
			"删除不可恢复，任务事件时间线一并移除。",
		// Tier 2: this deletes audit-visible history, not just moves states.
		Tier: defense.TierIrreversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"scope": map[string]any{"type": "string", "enum": []string{"history", "review", "all"}, "description": "清理范围：history / review / all"},
			},
			"required": []string{"scope"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			scope, _ := args["scope"].(string)
			return e.taskqClear(ctx, strings.ToLower(strings.TrimSpace(scope)))
		},
	})

	reg.Register(entry.Tool{
		Name:        "card_native_add",
		Description: "为本机能力卡（capabilities.yaml）添加一项原生命令行能力，变更后自动热重载。",
		// Tier 2 by necessity: this tool decides what the node is allowed to
		// run. Leaving it at Tier 1 let the model write its own authorization
		// basis — add a card entry (with its own `tier`) for the command it
		// wants, then run it through the very gate it just widened.
		Tier: defense.TierIrreversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":          map[string]any{"type": "string", "description": "能力 ID，如 ffmpeg:transcode"},
				"description": map[string]any{"type": "string", "description": "能力说明"},
				"command":     map[string]any{"type": "string", "description": "执行命令"},
				"args":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "默认参数列表"},
				"tier":        map[string]any{"type": "integer", "description": "操作等级（1=可逆/只读，2=不可逆/需授权，默认 1）"},
			},
			"required": []string{"id", "description", "command"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			id, _ := args["id"].(string)
			desc, _ := args["description"].(string)
			cmd, _ := args["command"].(string)
			tier := 1
			if tVal, ok := args["tier"]; ok {
				switch t := tVal.(type) {
				case float64:
					tier = int(t)
				case int:
					tier = t
				}
			}
			return e.cardNativeAdd(ctx, ledger.NativeAbility{
				ID:          strings.TrimSpace(id),
				Description: strings.TrimSpace(desc),
				Command:     strings.TrimSpace(cmd),
				Args:        toStringSlice(args["args"]),
				Tier:        tier,
			})
		},
	})

	reg.Register(entry.Tool{
		Name:        "card_native_remove",
		Description: "从本机能力卡中删除指定 ID 的原生能力，变更后自动热重载。",
		Tier:        defense.TierIrreversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "要删除的原生能力 ID"},
			},
			"required": []string{"id"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			id, _ := args["id"].(string)
			return e.cardNativeRemove(ctx, strings.TrimSpace(id))
		},
	})

	reg.Register(entry.Tool{
		Name:        "card_agent_add",
		Description: "为本机能力卡注册一个新的 Agent CLI，变更后自动热重载。adapter 可留空——按 name 从内置注册表（panda agents）解析，dsh 这类已知 CLI 会自动映射到正确适配器。",
		// Tier 2 for the same reason as card_native_add: registering an agent
		// is adding a way to run code on this node.
		Tier: defense.TierIrreversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":         map[string]any{"type": "string", "description": "Agent 名称或 CLI 命令名，如 claude_code / dsh"},
				"adapter":      map[string]any{"type": "string", "description": "适配器文件名，如 claude_code.py；已知 CLI 可留空由注册表解析"},
				"capabilities": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "能力标识列表"},
				"best_at":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "擅长领域"},
				"not_for":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "不适合领域"},
				"cost_tier":    map[string]any{"type": "string", "description": "成本档位：low / mid / high"},
				"tier":         map[string]any{"type": "integer", "description": "安全等级（1=可逆，默认 1；2=不可逆、每次需人工授权）"},
			},
			"required": []string{"name"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			adapter, _ := args["adapter"].(string)
			costTier, _ := args["cost_tier"].(string)
			tier := agents.TierAutoApproved
			if tVal, ok := args["tier"]; ok {
				switch t := tVal.(type) {
				case float64:
					tier = int(t)
				case int:
					tier = t
				}
			}
			return e.cardAgentAdd(ctx, strings.TrimSpace(name), ledger.Agent{
				Adapter:      strings.TrimSpace(adapter),
				Capabilities: toStringSlice(args["capabilities"]),
				BestAt:       toStringSlice(args["best_at"]),
				NotFor:       toStringSlice(args["not_for"]),
				CostTier:     strings.TrimSpace(costTier),
				Tier:         tier,
			})
		},
	})

	reg.Register(entry.Tool{
		Name:        "card_rescan",
		Description: "扫描本机已安装的 Agent CLI，把尚未注册的补进能力卡（panda card rescan 的等价操作）。用户要求“配置/注册某个 CLI”“让它以后能接任务”时用这个，不要手工猜适配器文件名。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return e.cardRescan(ctx)
		},
	})

	reg.Register(entry.Tool{
		Name:        "card_agent_set",
		Description: "修改本机能力卡中已有的 Agent CLI 配置属性，变更后自动热重载。",
		Tier:        defense.TierIrreversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":         map[string]any{"type": "string", "description": "Agent 名称"},
				"adapter":      map[string]any{"type": "string", "description": "适配器文件名"},
				"capabilities": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "能力标识列表"},
				"best_at":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "擅长领域"},
				"not_for":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "不适合领域"},
				"cost_tier":    map[string]any{"type": "string", "description": "成本档位：low / mid / high"},
				"tier":         map[string]any{"type": "integer", "description": "安全等级（1=可逆，2=不可逆）"},
			},
			"required": []string{"name"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			var upd cardmut.AgentUpdate
			if a, ok := args["adapter"].(string); ok && a != "" {
				upd.Adapter = &a
			}
			if caps := toStringSlice(args["capabilities"]); caps != nil {
				upd.Capabilities = &caps
			}
			if best := toStringSlice(args["best_at"]); best != nil {
				upd.BestAt = &best
			}
			if notFor := toStringSlice(args["not_for"]); notFor != nil {
				upd.NotFor = &notFor
			}
			if ct, ok := args["cost_tier"].(string); ok && ct != "" {
				upd.CostTier = &ct
			}
			if tVal, ok := args["tier"]; ok {
				switch t := tVal.(type) {
				case float64:
					it := int(t)
					upd.Tier = &it
				case int:
					upd.Tier = &t
				}
			}
			return e.cardAgentSet(ctx, strings.TrimSpace(name), upd)
		},
	})

	reg.Register(entry.Tool{
		Name:        "card_agent_remove",
		Description: "从本机能力卡中注销指定的 Agent CLI，变更后自动热重载。",
		Tier:        defense.TierIrreversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "Agent 名称"},
			},
			"required": []string{"name"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			return e.cardAgentRemove(ctx, strings.TrimSpace(name))
		},
	})

	reg.Register(entry.Tool{
		Name:        "card_manual_add",
		Description: "为本机能力卡添加一项人工协同能力，变更后自动热重载。",
		Tier:        defense.TierIrreversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":     map[string]any{"type": "string", "description": "能力 ID"},
				"notify": map[string]any{"type": "string", "description": "通知渠道/提示"},
			},
			"required": []string{"id"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			id, _ := args["id"].(string)
			notify, _ := args["notify"].(string)
			return e.cardManualAdd(ctx, ledger.ManualAbility{
				ID:     strings.TrimSpace(id),
				Notify: strings.TrimSpace(notify),
			})
		},
	})

	reg.Register(entry.Tool{
		Name:        "card_manual_remove",
		Description: "从本机能力卡中删除指定的人工协同能力，变更后自动热重载。",
		Tier:        defense.TierIrreversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "人工能力 ID"},
			},
			"required": []string{"id"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			id, _ := args["id"].(string)
			return e.cardManualRemove(ctx, strings.TrimSpace(id))
		},
	})

	reg.Register(entry.Tool{
		Name:        "project_list",
		Description: "查看系统中所有项目列表，以及当前会话正处于哪个激活项目中。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return e.projectList(ctx)
		},
	})

	reg.Register(entry.Tool{
		Name:        "project_create",
		Description: "创建一个新项目。name 填项目名称，work_dir 为工作目录（可选），description 为项目描述（可选），enter 为是否立即切换进入该项目（默认 true）。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "项目名称"},
				"work_dir":    map[string]any{"type": "string", "description": "工作目录（可选）"},
				"description": map[string]any{"type": "string", "description": "项目描述（可选）"},
				"enter":       map[string]any{"type": "boolean", "description": "是否立即切换进入该项目（默认 true）"},
			},
			"required": []string{"name"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			workDir, _ := args["work_dir"].(string)
			desc, _ := args["description"].(string)
			enter := true
			if eVal, ok := args["enter"].(bool); ok {
				enter = eVal
			}
			return e.projectCreate(ctx, strings.TrimSpace(name), strings.TrimSpace(workDir), strings.TrimSpace(desc), enter)
		},
	})

	reg.Register(entry.Tool{
		Name:        "project_enter",
		Description: "切换当前会话所处的目标项目。切换后新建任务默认归属于该项目。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "要进入的项目名称"},
			},
			"required": []string{"name"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			return e.projectEnter(ctx, strings.TrimSpace(name))
		},
	})

	reg.Register(entry.Tool{
		Name:        "project_exit",
		Description: "退出当前激活的项目环境，回到全局无项目状态。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return e.projectExit(ctx)
		},
	})

	reg.Register(entry.Tool{
		Name:        "node_remove",
		Description: "从设备网络目录中移除一个离线或废弃的节点记录。不能删除本机或在线节点。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"node_id": map[string]any{"type": "string", "description": "要移除的节点 ID"},
			},
			"required": []string{"node_id"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			id, _ := args["node_id"].(string)
			return e.nodeRemove(ctx, strings.TrimSpace(id))
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
			if path, err := e.checkCardPath(); err == nil {
				_ = e.ReloadCard(path)
				if reloadedNodes, rErr := ledger.Query(e.db, "", ""); rErr == nil {
					for _, n := range reloadedNodes {
						if n.ID == selfID {
							target = n
							break
						}
					}
				}
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
		if t.State == core.StateReview {
			// "待审批" alone hides what approving would DO — the model kept
			// cancelling executed work it could have accepted. Label the
			// disposition so the row itself tells the right action.
			fmt.Fprintf(&b, "［%s］", zhDisposition(store, ctx, t.TaskID))
		}
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
	if t.State == core.StateReview {
		fmt.Fprintf(&b, "\n审批处置：%s", zhDisposition(store, ctx, t.TaskID))
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

// zhDisposition renders what approving a review task would DO — accept the
// work it already produced, resume an execution that never ran, or refuse
// until the input changes. Plain "待审批" hid all three behind one word.
func zhDisposition(store *core.TaskStore, ctx context.Context, taskID string) string {
	d, err := store.ApprovalDisposition(ctx, taskID)
	if err != nil {
		return "审批处置未知"
	}
	switch d {
	case core.ApprovalAcceptWork:
		return "批准=验收已有成果→完成"
	case core.ApprovalResumeExecution:
		return "批准=授权并重新执行"
	case core.ApprovalNeedsChangedInput:
		return "需修改输入，不能直接批准"
	}
	return "待人工处理"
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

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func toStringSlice(val any) []string {
	if val == nil {
		return nil
	}
	switch v := val.(type) {
	case []string:
		return v
	case []any:
		res := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				res = append(res, strings.TrimSpace(s))
			}
		}
		return res
	default:
		return nil
	}
}

func parseTaskPriority(s string) (int, string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "high", "高":
		return core.PriorityHigh, "high (高)", true
	case "normal", "mid", "medium", "中", "普通", "":
		return core.PriorityNormal, "normal (普通)", true
	case "low", "低":
		return core.PriorityLow, "low (低)", true
	default:
		return 0, "", false
	}
}

func (e *Engine) taskqCancel(ctx context.Context, taskID string) (string, error) {
	if taskID == "" {
		return "", fmt.Errorf("task_id 不能为空")
	}
	store := core.NewTaskStore(e.db, e.logger)
	resolved, err := e.resolveTaskID(ctx, store, taskID)
	if err != nil {
		return "", fmt.Errorf("解析任务 %s：%w", taskID, err)
	}
	if err := e.guardReviewForModel(ctx, store, resolved); err != nil {
		return "", err
	}
	ids, err := e.CancelTask(ctx, resolved)
	if err != nil {
		return "", fmt.Errorf("取消任务 %s：%w", resolved, err)
	}
	if len(ids) == 0 {
		return fmt.Sprintf("任务 %s 已是终态或已取消", resolved), nil
	}
	return fmt.Sprintf("任务 %s 已成功取消（级联中止 %d 个任务：%s）", resolved, len(ids), strings.Join(ids, "、")), nil
}

func (e *Engine) taskqPriority(ctx context.Context, taskID, priority string) (string, error) {
	if taskID == "" {
		return "", fmt.Errorf("task_id 不能为空")
	}
	prio, prioName, ok := parseTaskPriority(priority)
	if !ok {
		return "", fmt.Errorf("无效的优先级 %q，必须为 high、normal 或 low", priority)
	}
	store := core.NewTaskStore(e.db, e.logger)
	resolved, err := e.resolveTaskID(ctx, store, taskID)
	if err != nil {
		return "", fmt.Errorf("解析任务 %s：%w", taskID, err)
	}
	if err := e.guardReorderable(ctx, store, resolved); err != nil {
		return "", err
	}
	if err := store.SetPriority(ctx, resolved, prio); err != nil {
		return "", fmt.Errorf("设置任务优先级：%w", err)
	}
	return fmt.Sprintf("已将任务 %s 的排队优先级设置为 %s", resolved, prioName), nil
}

func (e *Engine) taskqMove(ctx context.Context, taskID string, seq int64) (string, error) {
	if taskID == "" {
		return "", fmt.Errorf("task_id 不能为空")
	}
	if seq < 1 {
		return "", fmt.Errorf("seq 必须为正整数（>= 1）")
	}
	store := core.NewTaskStore(e.db, e.logger)
	resolved, err := e.resolveTaskID(ctx, store, taskID)
	if err != nil {
		return "", fmt.Errorf("解析任务 %s：%w", taskID, err)
	}
	if err := e.guardReorderable(ctx, store, resolved); err != nil {
		return "", err
	}
	if err := store.SetSeq(ctx, resolved, seq); err != nil {
		return "", fmt.Errorf("设置排队序号：%w", err)
	}
	return fmt.Sprintf("已将任务 %s 的排队顺序序号设置为 %d", resolved, seq), nil
}

// guardReorderable confines queue-order edits to tasks the scheduler can
// still order. Setting seq/priority on a running or finished row used to
// succeed silently and misreported the operation as meaningful.
func (e *Engine) guardReorderable(ctx context.Context, store *core.TaskStore, taskID string) error {
	t, err := store.Get(ctx, taskID)
	if err != nil {
		return fmt.Errorf("读取任务 %s：%w", taskID, err)
	}
	switch t.State {
	case core.StateSubmitted, core.StateQueued:
		return nil
	default:
		return fmt.Errorf("任务 %s 当前状态为「%s」，只有排队中的任务才能调整顺序/优先级", taskID, zhTaskState(t.State))
	}
}

// taskqCancelBatch cancels each id through the engine path (remote executors
// are notified, not just the local row). A batch cleanup reports per-id
// outcomes instead of aborting on the first failure.
func (e *Engine) taskqCancelBatch(ctx context.Context, ids []string) (string, error) {
	store := core.NewTaskStore(e.db, e.logger)
	var b strings.Builder
	fmt.Fprintf(&b, "批量取消 %d 个任务：", len(ids))
	ok := 0
	for _, raw := range ids {
		resolved, err := e.resolveTaskID(ctx, store, strings.TrimSpace(raw))
		if err != nil {
			fmt.Fprintf(&b, "\n- %s：解析失败（%v）", raw, err)
			continue
		}
		if err := e.guardReviewForModel(ctx, store, resolved); err != nil {
			fmt.Fprintf(&b, "\n- %s：%v", resolved, err)
			continue
		}
		cancelled, err := e.CancelTask(ctx, resolved)
		if err != nil {
			fmt.Fprintf(&b, "\n- %s：取消失败（%v）", resolved, err)
			continue
		}
		if len(cancelled) == 0 {
			fmt.Fprintf(&b, "\n- %s：已是终态", resolved)
			continue
		}
		ok++
		fmt.Fprintf(&b, "\n- %s：已取消（级联 %d 个）", resolved, len(cancelled))
	}
	fmt.Fprintf(&b, "\n合计：%d 个成功取消", ok)
	return b.String(), nil
}

// reviewDecisionHint is the message every model-facing queue tool returns when
// its target is a task parked for human approval. A review task's outcome is a
// human decision in either direction: approve, reject, cancel and clear all
// dispose of the human's pending choice, so none of them may run from a tool
// call — not even under a session-level tier-2 grant, which would let the
// model silently decide the very question the gate exists to ask.
const reviewDecisionHint = "只能由用户本人在前台决定（TUI 输入 /approve 或 /reject，Web 面板的队列/详情页点「批准」或「拒绝」）。请把该任务需要审批一事告知用户，不要代替用户处置。"

// taskqApprove reports a review-parked task and tells the model to surface it
// to the user. It never transitions the task: model-side approval would let a
// session grant silently approve the exact tier-2 work the refusal was parked
// for, making the human gate decorative.
func (e *Engine) taskqApprove(ctx context.Context, taskID string) (string, error) {
	if taskID == "" {
		return "", fmt.Errorf("task_id 不能为空")
	}
	store := core.NewTaskStore(e.db, e.logger)
	resolved, err := e.resolveTaskID(ctx, store, taskID)
	if err != nil {
		return "", fmt.Errorf("解析任务 %s：%w", taskID, err)
	}
	t, err := store.Get(ctx, resolved)
	if err != nil {
		return "", fmt.Errorf("读取任务 %s：%w", resolved, err)
	}
	if t.State != core.StateReview {
		return "", fmt.Errorf("任务 %s 当前状态为「%s」，不在待审批中", resolved, zhTaskState(t.State))
	}
	return fmt.Sprintf("任务 %s（%s）正在等待用户审批。%s", resolved, t.Title, reviewDecisionHint), nil
}

// taskqReject mirrors taskqApprove for the reject direction: rejecting is
// still disposing of the human's pending decision, so it is refused too.
func (e *Engine) taskqReject(ctx context.Context, taskID, reason string) (string, error) {
	return e.taskqApprove(ctx, taskID)
}

// guardReviewForModel refuses a model-initiated operation on a task parked in
// review: the pending state is the human's decision to make, so no tool call —
// cancel included — may silently dispose of it.
func (e *Engine) guardReviewForModel(ctx context.Context, store *core.TaskStore, taskID string) error {
	t, err := store.Get(ctx, taskID)
	if err != nil {
		return fmt.Errorf("读取任务 %s：%w", taskID, err)
	}
	if t.State == core.StateReview {
		return fmt.Errorf("任务 %s 正在等待用户审批，%s", taskID, reviewDecisionHint)
	}
	return nil
}

// taskqClear wipes queue records by scope. "history" deletes only terminal
// rows; "review" and "all" would cancel-and-delete pending human approvals, so
// they are refused while any review task exists — the reviewer must decide
// each one in the foreground first.
func (e *Engine) taskqClear(ctx context.Context, scope string) (string, error) {
	store := core.NewTaskStore(e.db, e.logger)
	switch scope {
	case "review":
		return "", fmt.Errorf("待审批任务需要用户逐个决定，%s", reviewDecisionHint)
	case "all":
		ts, err := store.ListByState(ctx, core.StateReview)
		if err != nil {
			return "", fmt.Errorf("查询任务：%w", err)
		}
		if len(ts) > 0 {
			return "", fmt.Errorf("仍有 %d 个任务等待用户审批，%s", len(ts), reviewDecisionHint)
		}
		ts, err = store.ListByState(ctx, "")
		if err != nil {
			return "", fmt.Errorf("查询任务：%w", err)
		}
		if len(ts) == 0 {
			return "任务队列已为空。", nil
		}
		for _, t := range ts {
			if !core.Terminal(t.State) {
				_, _ = e.CancelTask(ctx, t.TaskID)
			}
		}
		cancelled, deleted, err := store.ClearQueue(ctx)
		if err != nil {
			return "", fmt.Errorf("清空队列：%w", err)
		}
		return fmt.Sprintf("已清空队列：取消 %d 个，删除 %d 条记录。", cancelled, deleted), nil
	case "history":
		deleted := 0
		for _, st := range []string{core.StateDone, core.StateFailed, core.StateCancelled, core.StateExpired} {
			ts, err := store.ListByState(ctx, st)
			if err != nil {
				return "", fmt.Errorf("查询任务：%w", err)
			}
			for _, t := range ts {
				n, err := store.Delete(ctx, t.TaskID)
				if err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						continue // subtree row already removed with its parent
					}
					return "", fmt.Errorf("删除任务 %s：%w", t.TaskID, err)
				}
				deleted += n
			}
		}
		return fmt.Sprintf("已删除 %d 条终态任务记录。", deleted), nil
	default:
		return "", fmt.Errorf("无效的 scope %q，必须为 history、review 或 all", scope)
	}
}

func (e *Engine) checkCardPath() (string, error) {
	if e.cardPath != "" {
		return e.cardPath, nil
	}
	if e.cfg != nil && e.cfg.EffectiveCardPath() != "" {
		e.cardPath = e.cfg.EffectiveCardPath()
		return e.cardPath, nil
	}
	target := config.DefaultCardTarget("")
	if target != "" {
		e.cardPath = target
		return e.cardPath, nil
	}
	return "", fmt.Errorf("当前会话未加载能力卡文件且无法推导默认路径")
}

func (e *Engine) cardNativeAdd(ctx context.Context, ab ledger.NativeAbility) (string, error) {
	path, err := e.checkCardPath()
	if err != nil {
		return "", err
	}
	if ab.ID == "" {
		return "", fmt.Errorf("能力 id 不能为空")
	}
	if ab.Command == "" {
		return "", fmt.Errorf("执行 command 不能为空")
	}
	if err := cardmut.NativeAdd(path, ab); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			_ = e.ReloadCard(path)
			return fmt.Sprintf("原生能力 %s 已存在并生效", ab.ID), nil
		}
		return "", fmt.Errorf("添加原生能力：%w", err)
	}
	_ = e.ReloadCard(path)
	return fmt.Sprintf("原生能力 %s 已成功添加并热重载生效", ab.ID), nil
}

func (e *Engine) cardNativeRemove(ctx context.Context, id string) (string, error) {
	path, err := e.checkCardPath()
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("能力 id 不能为空")
	}
	if err := cardmut.NativeRemove(path, id); err != nil {
		return "", fmt.Errorf("删除原生能力：%w", err)
	}
	_ = e.ReloadCard(path)
	return fmt.Sprintf("原生能力 %s 已成功删除并热重载生效", id), nil
}

func (e *Engine) cardAgentAdd(ctx context.Context, name string, ag ledger.Agent) (string, error) {
	path, err := e.checkCardPath()
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", fmt.Errorf("Agent 名称不能为空")
	}
	if err := resolveAgentFromRegistry(&name, &ag); err != nil {
		return "", err
	}
	if len(ag.Capabilities) == 0 {
		return "", fmt.Errorf("Agent capabilities 不能为空")
	}
	if err := checkAdapterExists(ag.Adapter); err != nil {
		return "", err
	}
	if err := cardmut.AgentAdd(path, name, ag); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			upd := cardmut.AgentUpdate{
				Adapter:      &ag.Adapter,
				Capabilities: &ag.Capabilities,
			}
			if ag.InstallCheck != "" {
				upd.InstallCheck = &ag.InstallCheck
			}
			if len(ag.BestAt) > 0 {
				upd.BestAt = &ag.BestAt
			}
			if len(ag.NotFor) > 0 {
				upd.NotFor = &ag.NotFor
			}
			if ag.CostTier != "" {
				upd.CostTier = &ag.CostTier
			}
			if ag.Tier != 0 {
				upd.Tier = &ag.Tier
			}
			if setErr := cardmut.AgentSet(path, name, upd); setErr == nil {
				_ = e.ReloadCard(path)
				return fmt.Sprintf("Agent %s 已存在，已更新配置并热重载生效", name), nil
			}
		}
		return "", fmt.Errorf("注册 Agent：%w", err)
	}
	_ = e.ReloadCard(path)
	return fmt.Sprintf("Agent %s 已成功注册并热重载生效", name), nil
}

// resolveAgentFromRegistry fills an agent entry from the built-in registry
// (internal/agents) when the caller names something the registry already knows.
//
// This is the fix for the failure mode that made "配置 dsh" unreachable: the
// model was asked to register a CLI it had no facts about, so it invented an
// adapter filename (`dsh.py`) and wrote a card entry pointing at a file that
// does not exist — then reported success. The registry knew the whole mapping
// (name deepseek_harness, binary dsh, adapter deepseek_harness.py) and was never
// consulted, because card_agent_add took `adapter` as free text.
//
// A caller-supplied adapter is never silently replaced: it wins when it names a
// real file. The registry only fills gaps, and may rename the entry to the
// registry key so routing and `panda agents` agree on one name.
func resolveAgentFromRegistry(name *string, ag *ledger.Agent) error {
	if *name == "" {
		return fmt.Errorf("Agent 名称不能为空")
	}
	known, ok := lookupKnownAgent(*name)
	if !ok {
		if strings.TrimSpace(ag.Adapter) == "" {
			return fmt.Errorf("未知的 Agent %q：内置注册表里没有它，请通过 adapter 明确指定适配器文件名（可参考 %s）", *name, knownAgentList())
		}
		return nil
	}
	// The registry key is the canonical name: an operator (or model) asking for
	// "dsh" must end up with the same entry `panda card rescan` would write.
	*name = known.Name
	if strings.TrimSpace(ag.Adapter) == "" || !adapterFileExists(strings.TrimSpace(ag.Adapter)) {
		if known.Adapter != "" {
			ag.Adapter = known.Adapter
		}
	}
	if len(ag.Capabilities) == 0 {
		ag.Capabilities = append([]string(nil), known.DefaultCapabilities...)
	}
	if len(ag.BestAt) == 0 {
		ag.BestAt = append([]string(nil), known.DefaultBestAt...)
	}
	if len(ag.NotFor) == 0 {
		ag.NotFor = []string{"hardware_io", "realtime_control"}
	}
	if ag.CostTier == "" {
		ag.CostTier = known.DefaultCostTier
	}
	if ag.Tier == 0 {
		ag.Tier = known.DefaultTier
	}
	if ag.InstallCheck == "" {
		for _, bin := range known.Binaries {
			if p, err := exec.LookPath(bin); err == nil {
				ag.InstallCheck = p + " --version"
				break
			}
		}
	}
	return nil
}

// lookupKnownAgent matches a registry entry by its key, by one of the CLI binary
// names it is probed by (so "dsh" finds "deepseek_harness"), or by adapter file.
func lookupKnownAgent(name string) (agents.Known, bool) {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, k := range agents.Registry() {
		if strings.ToLower(k.Name) == want || strings.ToLower(k.Adapter) == want {
			return k, true
		}
		for _, bin := range k.Binaries {
			if strings.ToLower(bin) == want {
				return k, true
			}
		}
	}
	return agents.Known{}, false
}

// knownAgentList renders the registry's installable agents as one line, so a
// rejected add tells the caller exactly which names are valid.
func knownAgentList() string {
	reg := agents.Registry()
	parts := make([]string, 0, len(reg))
	for _, k := range reg {
		if k.Adapter == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s→%s", k.Name, k.Adapter))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// adapterFileExists reports whether the adapter script is present in the
// adapters directory the commander will spawn from.
func adapterFileExists(adapter string) bool {
	if adapter == "" {
		return false
	}
	dir := commander.AdapterDir()
	if dir == "" {
		return false
	}
	st, err := os.Stat(filepath.Join(dir, adapter))
	return err == nil && !st.IsDir()
}

// checkAdapterExists refuses to register an agent whose adapter script is not on
// disk. An entry that points at a missing file is worse than a rejected write:
// the card advertises the agent, routing sends work to it, and the task fails at
// spawn time on a node that looks correctly configured.
func checkAdapterExists(adapter string) error {
	adapter = strings.TrimSpace(adapter)
	if adapter == "" {
		return fmt.Errorf("Agent adapter 不能为空：请给出适配器文件名，或使用注册表里已有的名字（%s）", knownAgentList())
	}
	if adapterFileExists(adapter) {
		return nil
	}
	return fmt.Errorf("适配器 %q 不存在于 %s，拒绝写入能力卡（否则路由会把任务派给一个无法启动的 Agent）。可用适配器：%s",
		adapter, commander.AdapterDir(), adapterList())
}

// adapterList lists the adapter scripts actually present on disk.
func adapterList() string {
	entries, err := os.ReadDir(commander.AdapterDir())
	if err != nil {
		return "（无法读取适配器目录）"
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".py") && !strings.HasPrefix(e.Name(), "_") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// cardRescan detects the agent CLIs installed on this host and adds the ones the
// card does not declare yet, reusing the same registry + detection layer as
// `panda card rescan`. It exists so "帮我配置 dsh / 把这个 CLI 接进来" has a
// correct action to take: without it the only path was a hand-written
// card_agent_add, which is what produced the invented `dsh.py` entry.
//
// The merge is additive on purpose — an existing entry is left exactly as it is,
// tier included, because that field is the operator's authorization decision and
// a scan must never silently widen it.
func (e *Engine) cardRescan(ctx context.Context) (string, error) {
	path, err := e.checkCardPath()
	if err != nil {
		return "", err
	}
	existing := map[string]bool{}
	if card, err := ledger.LoadCard(path); err == nil {
		for name := range card.Agents {
			existing[name] = true
		}
	}
	detected := carddetect.CardAgents()
	var added []string
	for _, name := range sortedAgentNames(detected) {
		if existing[name] {
			continue
		}
		if err := cardmut.AgentAdd(path, name, detected[name]); err != nil {
			return "", fmt.Errorf("注册已安装的 %s：%w", name, err)
		}
		added = append(added, name)
	}
	if len(added) > 0 {
		_ = e.ReloadCard(path)
		return fmt.Sprintf("已检测本机安装的 Agent CLI 并注册：%s（共 %d 个，已热重载生效）",
			strings.Join(added, ", "), len(added)), nil
	}
	return fmt.Sprintf("本机已安装的 Agent CLI 都已在能力卡中（检测到 %d 个），没有新增。", len(detected)), nil
}

// sortedAgentNames keeps the rescan output and write order stable between runs.
func sortedAgentNames(m map[string]ledger.Agent) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (e *Engine) cardAgentSet(ctx context.Context, name string, upd cardmut.AgentUpdate) (string, error) {
	path, err := e.checkCardPath()
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", fmt.Errorf("Agent 名称不能为空")
	}
	// Same invariant as the add path: an entry must never point at a script that
	// is not on disk, whether it got there by add or by set.
	if upd.Adapter != nil {
		if err := checkAdapterExists(*upd.Adapter); err != nil {
			return "", err
		}
	}
	if err := cardmut.AgentSet(path, name, upd); err != nil {
		return "", fmt.Errorf("修改 Agent：%w", err)
	}
	_ = e.ReloadCard(path)
	return fmt.Sprintf("Agent %s 已成功更新并热重载生效", name), nil
}

func (e *Engine) cardAgentRemove(ctx context.Context, name string) (string, error) {
	path, err := e.checkCardPath()
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", fmt.Errorf("Agent 名称不能为空")
	}
	if err := cardmut.AgentRemove(path, name); err != nil {
		return "", fmt.Errorf("删除 Agent：%w", err)
	}
	_ = e.ReloadCard(path)
	return fmt.Sprintf("Agent %s 已成功注销并热重载生效", name), nil
}

func (e *Engine) cardManualAdd(ctx context.Context, ab ledger.ManualAbility) (string, error) {
	path, err := e.checkCardPath()
	if err != nil {
		return "", err
	}
	if ab.ID == "" {
		return "", fmt.Errorf("人工能力 id 不能为空")
	}
	if err := cardmut.ManualAdd(path, ab); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			_ = e.ReloadCard(path)
			return fmt.Sprintf("人工能力 %s 已存在并生效", ab.ID), nil
		}
		return "", fmt.Errorf("添加人工能力：%w", err)
	}
	_ = e.ReloadCard(path)
	return fmt.Sprintf("人工能力 %s 已成功添加并热重载生效", ab.ID), nil
}

func (e *Engine) cardManualRemove(ctx context.Context, id string) (string, error) {
	path, err := e.checkCardPath()
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("人工能力 id 不能为空")
	}
	if err := cardmut.ManualRemove(path, id); err != nil {
		return "", fmt.Errorf("删除人工能力：%w", err)
	}
	_ = e.ReloadCard(path)
	return fmt.Sprintf("人工能力 %s 已成功删除并热重载生效", id), nil
}

func (e *Engine) projectList(ctx context.Context) (string, error) {
	pstore := projectstore.NewStore(e.db)
	ps, err := pstore.List()
	if err != nil {
		return "", fmt.Errorf("查询项目列表：%w", err)
	}
	active, _ := pstore.Active()
	if len(ps) == 0 {
		return "系统中当前暂无项目记录。", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "系统共 %d 个项目：", len(ps))
	for _, p := range ps {
		marker := "  "
		if p.Name == active {
			marker = "* "
		}
		desc := ""
		if p.Description != "" {
			desc = " — " + p.Description
		}
		workDir := "默认工作目录"
		if p.WorkDir != "" {
			workDir = p.WorkDir
		}
		fmt.Fprintf(&b, "\n%s%s（%s）%s", marker, p.Name, workDir, desc)
	}
	if active != "" {
		fmt.Fprintf(&b, "\n\n当前处于激活状态的项目为：%s", active)
	} else {
		b.WriteString("\n\n当前处于全局无项目状态。")
	}
	return b.String(), nil
}

func (e *Engine) projectCreate(ctx context.Context, name, workDir, description string, enter bool) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("项目名称不能为空")
	}
	pstore := projectstore.NewStore(e.db)
	p, err := pstore.Create(name, workDir, description)
	if err != nil {
		return "", fmt.Errorf("创建项目：%w", err)
	}
	if enter {
		if err := pstore.SetActive(p.Name); err != nil {
			return "", fmt.Errorf("进入项目：%w", err)
		}
		e.SetProject(p.Name, p.WorkDir)
		return fmt.Sprintf("项目 %s 创建成功，并已切换进入该项目（工作目录：%s）", p.Name, orDash(p.WorkDir)), nil
	}
	return fmt.Sprintf("项目 %s 创建成功（工作目录：%s）", p.Name, orDash(p.WorkDir)), nil
}

func (e *Engine) projectEnter(ctx context.Context, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("项目名称不能为空")
	}
	pstore := projectstore.NewStore(e.db)
	p, err := pstore.Get(name)
	if err != nil {
		return "", fmt.Errorf("读取项目 %s：%w", name, err)
	}
	if err := pstore.SetActive(p.Name); err != nil {
		return "", fmt.Errorf("进入项目：%w", err)
	}
	e.SetProject(p.Name, p.WorkDir)
	return fmt.Sprintf("已切换进入项目 %s（工作目录：%s）", p.Name, orDash(p.WorkDir)), nil
}

func (e *Engine) projectExit(ctx context.Context) (string, error) {
	pstore := projectstore.NewStore(e.db)
	if err := pstore.ClearActive(); err != nil {
		return "", fmt.Errorf("退出项目：%w", err)
	}
	e.SetProject("", "")
	return "已退出项目，当前处于全局无项目状态。", nil
}

func (e *Engine) nodeRemove(ctx context.Context, nodeID string) (string, error) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return "", fmt.Errorf("node_id 不能为空")
	}
	if e.cfg != nil && nodeID == e.selfNodeID() {
		return "", fmt.Errorf("不能删除本机节点的记录")
	}
	nodes, err := ledger.Query(e.db, "", "")
	if err != nil {
		return "", fmt.Errorf("查询节点：%w", err)
	}
	found := false
	for _, n := range nodes {
		if n.ID == nodeID {
			found = true
			if n.Status == "online" {
				return "", fmt.Errorf("节点 %s 当前在线，请先停止该节点再移除", nodeID)
			}
			break
		}
	}
	if !found {
		return "", fmt.Errorf("未找到 ID 为 %s 的节点", nodeID)
	}
	if _, err := ledger.Remove(e.db, nodeID); err != nil {
		return "", fmt.Errorf("移除节点：%w", err)
	}
	return fmt.Sprintf("已成功从网络拓扑中移除节点 %s", nodeID), nil
}

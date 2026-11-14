// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/mcp"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/reminders"
)

// buildToolRegistry wires the memory, system-data (time/weather), reminder,
// management, and MCP tools into the entry-model tool registry. The schemas
// live here (the assembly layer) rather than in the prompt or in the memory
// package, so entry and memory stay decoupled: entry owns the registry,
// memory owns the executor, and this package glues them together. The engine
// reference is held, not read: the management tools dereference it inside
// Run, after New has (maybe) attached the scheduler.
func buildToolRegistry(e *Engine, hermes *memory.Hermes, projects *memory.Projects, rem *reminders.Store) *entry.Registry {
	mem := memory.NewTool(hermes, projects)
	reg := entry.NewRegistry()

	targetEnum := map[string]any{"type": "string", "enum": []string{"user", "memory", "project"}}
	projectArg := map[string]any{"type": "string", "description": "project name (used when target=project)"}

	reg.Register(entry.Tool{
		Name:        memory.ToolRead,
		ReadOnly:    true,
		Description: "List memory entries (read before merge/delete) / 列出当前记忆条目（合并/删除前先读）。target: user / memory / project。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"target": targetEnum, "project": projectArg},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return mem.ExecuteCtx(ctx, memory.ToolRead, args)
		},
	})

	reg.Register(entry.Tool{
		Name:        memory.ToolAdd,
		Description: "Remember only confirmed, reusable long-term facts; upsert by stable key. No task outcomes, temporary requests or guesses. user: identity/preference/personal; memory: environment/convention; project: project knowledge. Read before correcting legacy entries.",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target":   targetEnum,
				"entry":    map[string]any{"type": "string", "description": "Content to remember / 要记住的内容"},
				"project":  projectArg,
				"category": map[string]any{"type": "string", "enum": []string{"identity", "preference", "personal", "environment", "convention", "project"}},
				"key":      map[string]any{"type": "string", "description": "Stable attribute key, independent of value; reuse it for changes (user.name, preference.language, build.command). Required together with category for facts that cannot be inferred."},
			},
			"required": []string{"target", "entry"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return mem.ExecuteCtx(ctx, memory.ToolAdd, args)
		},
	})

	reg.Register(entry.Tool{
		Name:        memory.ToolReplace,
		Description: "Replace an existing memory / 替换一条已有记忆。old: unique substring matching entry / 能唯一匹配待替换条目的子串。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target":   targetEnum,
				"old":      map[string]any{"type": "string", "description": "Unique substring to match / 能唯一匹配待替换条目的子串"},
				"new":      map[string]any{"type": "string", "description": "Replacement content / 替换后的内容"},
				"project":  projectArg,
				"category": map[string]any{"type": "string", "enum": []string{"identity", "preference", "personal", "environment", "convention", "project"}},
				"key":      map[string]any{"type": "string", "description": "Stable attribute key; preserve the existing key when correcting its value."},
			},
			"required": []string{"target", "old", "new"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return mem.ExecuteCtx(ctx, memory.ToolReplace, args)
		},
	})

	reg.Register(entry.Tool{
		Name:        memory.ToolRemove,
		Description: "Delete a memory entry / 删除一条记忆。old: unique substring matching entry / 能唯一匹配待删除条目的子串。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target":  targetEnum,
				"old":     map[string]any{"type": "string", "description": "Unique substring to match / 能唯一匹配待删除条目的子串"},
				"project": projectArg,
			},
			"required": []string{"target", "old"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return mem.ExecuteCtx(ctx, memory.ToolRemove, args)
		},
	})

	// System-data tools (§7.3): the model has no clock or senses of its own.
	registerTimeTool(reg)
	registerWeatherTool(reg)

	// Web tools: search + fetch are the model's window past its training
	// cutoff. Provider/auth resolution happens inside the call so a missing
	// config surfaces as guidance the model relays, not a silent dead end.
	registerWebTools(reg, e.cfg)

	// Management tools (v1): the read half of openpanda 调用 openpanda.
	registerMgmtTools(reg, e)

	// Procedural skills tools: AI autonomous discovery, installation, and inspection of skills.
	registerSkillTools(reg, e)

	if rem != nil {
		registerReminderTools(reg, rem)
	}

	return reg
}

// registerReminderTools adds reminder_set / reminder_list — the design's
// P1-28 "提醒我 5 分钟后开会" surface. The scanner that actually fires them
// lives in the daemon / web panel (reminders.Scanner); the CLI ask process
// is short-lived and only writes them. Tool names use underscores: the
// Anthropic tools API restricts names to ^[a-zA-Z0-9_-]+$ (no dots), and
// strict providers (e.g. DeepSeek's /anthropic endpoint) reject the request
// outright with a 400 otherwise.
func registerReminderTools(reg *entry.Registry, rem *reminders.Store) {
	reg.Register(entry.Tool{
		Name:        "reminder_set",
		Description: "Set a scheduled reminder / 设置一个定时提醒。after_minutes: minutes from now / 多少分钟后提醒; at: absolute time RFC3339 / 绝对时间。Choose one / 二选一。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message":       map[string]any{"type": "string", "description": "Reminder content / 提醒内容"},
				"after_minutes": map[string]any{"type": "number", "description": "Minutes from now (choose one with at) / 多少分钟后提醒"},
				"at":            map[string]any{"type": "string", "description": "Absolute time RFC3339 or '2006-01-02 15:04' / 提醒的绝对时间"},
			},
			"required": []string{"message"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			message, _ := args["message"].(string)
			message = strings.TrimSpace(message)
			if message == "" {
				return "", fmt.Errorf("message 不能为空")
			}
			due, err := reminderDueTime(args)
			if err != nil {
				return "", err
			}
			r, err := rem.Add(ctx, message, due, "tool")
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("已设置提醒 #%d：%s，将于 %s 触发（面板或守护进程会通知）",
				r.ID, message, due.Format("2006-01-02 15:04")), nil
		},
	})

	reg.Register(entry.Tool{
		Name:        "reminder_list",
		ReadOnly:    true,
		Description: "List all active pending reminders / 列出当前所有未触发的提醒。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			list, err := rem.List(ctx, false)
			if err != nil {
				return "", err
			}
			if len(list) == 0 {
				return "当前没有未触发的提醒。", nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "共 %d 条未触发提醒：", len(list))
			for _, r := range list {
				fmt.Fprintf(&b, "\n#%d %s — %s",
					r.ID, time.Unix(r.DueAt, 0).Format("2006-01-02 15:04"), r.Message)
			}
			return b.String(), nil
		},
	})

	reg.Register(entry.Tool{
		Name:        "reminder_delete",
		Description: "Delete a scheduled reminder by ID / 删除一条已设置的定时提醒。id: integer reminder ID / 提醒 ID（整数）。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "integer", "description": "Reminder ID / 提醒 ID"},
			},
			"required": []string{"id"},
		},

		Run: func(ctx context.Context, args map[string]any) (string, error) {
			rawID, ok := args["id"]
			if !ok {
				return "", fmt.Errorf("id 不能为空")
			}
			var id int64
			switch v := rawID.(type) {
			case float64:
				id = int64(v)
			case int64:
				id = v
			case int:
				id = int64(v)
			case string:
				parsed, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
				if err != nil {
					return "", fmt.Errorf("无效的提醒 ID: %w", err)
				}
				id = parsed
			default:
				return "", fmt.Errorf("无效的提醒 ID 类型")
			}
			ok, err := rem.Delete(ctx, id)
			if err != nil {
				return "", fmt.Errorf("删除提醒失败：%w", err)
			}
			if !ok {
				return fmt.Sprintf("未找到 ID 为 #%d 的提醒", id), nil
			}
			return fmt.Sprintf("已成功删除提醒 #%d", id), nil
		},
	})
}

// reminderDueTime resolves the after_minutes / at arguments of
// reminder.set into an absolute time. Exactly one of the two must be given.
func reminderDueTime(args map[string]any) (time.Time, error) {
	var afterMin float64
	var atRaw string
	if v, ok := args["after_minutes"].(float64); ok {
		afterMin = v
	}
	if v, ok := args["at"].(string); ok {
		atRaw = strings.TrimSpace(v)
	}
	if afterMin == 0 && atRaw == "" {
		return time.Time{}, fmt.Errorf("after_minutes 与 at 必须提供其中一个")
	}
	if afterMin != 0 && atRaw != "" {
		return time.Time{}, fmt.Errorf("after_minutes 与 at 只能提供其中一个")
	}
	if afterMin != 0 {
		if afterMin < 0 {
			return time.Time{}, fmt.Errorf("after_minutes 不能为负数")
		}
		return time.Now().Add(time.Duration(afterMin * float64(time.Minute))), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, atRaw, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析时间 %q，请用 RFC3339（如 2026-08-18T15:00:00+08:00）或 \"2006-01-02 15:04\"", atRaw)
}

// toolProgressDetail names the subject of one tool call for a progress line:
// the path it reads, the pattern it searches, the task it inspects. Full
// arguments can be large or carry text that belongs nowhere near a status
// line (a memory body, a note), so one short subject is all a progress note
// gets.
func toolProgressDetail(call *entry.ToolCall) string {
	if call == nil {
		return ""
	}
	for _, key := range []string{"pattern", "path", "task_id", "query", "project", "target", "name"} {
		v, ok := call.Arguments[key].(string)
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if r := []rune(v); len(r) > 80 {
			v = string(r[:80]) + "…"
		}
		return v
	}
	return ""
}

// readOnlyEntryToolsKey is request-scoped, not a mutable engine setting: a
// read-only thread must not silently grant a concurrent ordinary ask its tools.
type readOnlyEntryToolsKey struct{}

func withReadOnlyEntryTools(ctx context.Context) context.Context {
	return context.WithValue(ctx, readOnlyEntryToolsKey{}, true)
}

// executeTool runs a tool call through the registry and returns a user-facing
// result (a tool failure is folded into the result, not a hard exit, so the
// model can consolidate and retry). authorized is the ask's effective tier-2
// consent: a Tier-2 tool without it is refused before Run, and the refusal —
// with the consent instructions for the current surface — becomes the
// tool_result the model relays to the user (design §16: the same fail-closed
// gate native and agent plans pass through in commander.Router.Execute).
// denied reports a remembered "deny" for the scope: the refusal then names the
// standing decision instead of asking for a grant the user already declined.
func executeTool(ctx context.Context, reg *entry.Registry, call *entry.ToolCall, authorized, denied bool, approvalMode string, loc ...i18n.Locale) string {
	result, _ := executeToolChecked(ctx, reg, call, authorized, denied, approvalMode, loc...)
	return result
}

// toolPanicError carries a recovered panic out of a tool's Run. A single
// misbehaving adapter (or a nil map inside a built-in tool) must not take down
// the ask — let alone the whole daemon process — so the guard lives at the
// tool boundary rather than in each tool implementation.
type toolPanicError struct {
	tool  string
	value any
}

func (e *toolPanicError) Error() string {
	return fmt.Sprintf("tool %s panicked: %v", e.tool, e.value)
}

func runToolGuarded(ctx context.Context, t entry.Tool, args map[string]any) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			result, err = "", &toolPanicError{tool: t.Name, value: r}
		}
	}()
	return t.Run(ctx, args)
}

// toolFailureMessage keeps a tool failure inside the conversation as a
// tool_result the model can react to. A recovered panic gets its own wording:
// echoing a raw panic value at the user (or into the next prompt) leaks
// internals and invites the model to "fix" a crash it cannot see.
func toolFailureMessage(err error, loc i18n.Locale) string {
	var pe *toolPanicError
	if errors.As(err, &pe) {
		if loc == i18n.English {
			return "Tool execution failed: " + pe.tool + " raised an internal error (contained; the conversation is intact)"
		}
		return "工具执行失败：" + pe.tool + " 发生内部异常（已隔离，会话未中断）"
	}
	if loc == i18n.English {
		return "Tool execution failed: " + err.Error()
	}
	return "工具执行失败：" + err.Error()
}

// executeToolChecked is executeTool with the underlying run error exposed. The
// ask loop uses it to log failures (including recovered panics) instead of
// folding them into a string that vanishes into the transcript; the string
// result is still exactly what the model sees.
func executeToolChecked(ctx context.Context, reg *entry.Registry, call *entry.ToolCall, authorized, denied bool, approvalMode string, loc ...i18n.Locale) (string, error) {
	targetLoc := i18n.ChineseSimp
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	}
	if call == nil {
		err := errors.New("nil tool call")
		return toolFailureMessage(err, targetLoc), err
	}
	if reg == nil {
		err := errors.New("tool registry unavailable")
		return toolFailureMessage(err, targetLoc), err
	}
	t, ok := reg.Lookup(call.Tool)
	if !ok {
		if targetLoc == i18n.English {
			return "Tool execution failed: unknown tool " + call.Tool, nil
		}
		return "工具执行失败：未知工具 " + call.Tool, nil
	}
	// A tier is a reversibility classification, not a read-only promise.
	// Unknown/MCP tools (and the many Tier-1 mutation tools) fail closed.
	if ctx.Value(readOnlyEntryToolsKey{}) == true && !t.ReadOnly &&
		call.Tool != entry.ToolNameTaskSubmit && call.Tool != entry.ToolNameTaskBudget {
		if targetLoc == i18n.English {
			return "Tool execution refused: this conversation is read-only (plan mode).", nil
		}
		return "工具执行被拒：当前对话为只读（plan 模式）。", nil
	}
	// A remembered "deny" answers the tier-2 gate for this scope, so it may
	// only withhold the operations that gate guards. Applying it to every
	// tool — as this check once did — stripped the model of its read-only
	// observation tools (system_status, card_list) for the rest of the
	// project's life, and the refusal named a decision the user could not
	// connect to "the assistant suddenly stopped working".
	if denied && t.Tier != defense.TierReversible {
		if targetLoc == i18n.English {
			return "Tool execution refused: the user previously denied tier-2 operations for this scope. Ask the user to clear the remembered denial before retrying.", nil
		}
		return "工具执行被拒：该范围内已有需授权操作的拒绝记录。请先让用户清除已记住的拒绝再重试。", nil
	}
	if err := defense.Authorize(t.Tier, authorized); err != nil {
		if targetLoc == i18n.English {
			return "Tool execution refused (tier-2 requires authorization): " + toolConsentHint(t, approvalMode, targetLoc), nil
		}
		return "工具执行被拒（tier-2 需授权）：" + toolConsentHint(t, approvalMode, targetLoc), nil
	}
	result, err := runToolGuarded(ctx, t, call.Arguments)
	if err != nil {
		return toolFailureMessage(err, targetLoc), err
	}
	return result, nil
}

// taskDispatchCapture carries typed task results beside task_submit's string
// tool_results — one slot per dispatched task, since a single model response
// may emit several task_submit calls. One capture belongs to one synchronous
// AskTurns invocation.
type taskDispatchCapture struct {
	results []*Result
}

func (c *taskDispatchCapture) add(result *Result) { c.results = append(c.results, result) }

func (c *taskDispatchCapture) takeAll() []*Result {
	results := c.results
	c.results = nil
	return results
}

// dispatchTaskTool builds task_submit — the dispatch bridge for entry models
// behind compatible endpoints that drive everything through tool calls and
// never emit the task JSON directive (the observed failure: six rounds of
// taskq_list poking, zero tasks). It is registered on a per-ask copy of the
// registry because Run closes over the ask's prompt, workDir, consent and
// progress callbacks; submission goes through the same submitTask path
// (scheduler, approval gate) as a KindTask directive, and an inline-mode
// completion folds its output into the tool result so the model can report it.
func (e *Engine) dispatchTaskTool(prompt string, scope AskScope, authorize bool, cb StreamCallbacks, capture *taskDispatchCapture, loc ...i18n.Locale) entry.Tool {
	targetLoc := i18n.ChineseSimp
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	}
	desc := "把任务派发给 agent 执行。当用户要求调度某个 agent 干活时必须调用它（或输出 task JSON），而不是只口头答应。title 填任务标题，target 填要完成的目标，abilities 按 Connected Devices 摘要填能力 ID（如 agent:codex）。"
	props := map[string]any{
		"title":              map[string]any{"type": "string", "description": "任务标题（必填）"},
		"target":             map[string]any{"type": "string", "description": "要达成的目标（必填）"},
		"abilities":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "能力 ID 列表，如 agent:codex（必填）"},
		"node":               map[string]any{"type": "string", "description": "目标节点（可选，硬约束：用户指定设备时必填；任务只在该节点执行，节点离线则排队等其上线，绝不留到别的机器。留空才由调度器选）"},
		"scope":              map[string]any{"type": "string", "description": "允许修改的相对路径，逗号分隔（可选）"},
		"constraints":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "约束/禁令（可选）"},
		"success_definition": map[string]any{"type": "string", "description": "如何验证完成（可选）"},
		"action_spec": map[string]any{"type": "object", "description": "驱动 hardware:* 物理/桌面执行器时必填（可选）",
			"properties": map[string]any{
				"target_actuator": map[string]any{"type": "string", "description": "设备列表中的 hardware:* 能力 ID"},
				"action":          map[string]any{"type": "string", "description": "动作动词，如 rotate|record|snap|notify|capture|click|type|key"},
				"parameters":      map[string]any{"type": "object", "description": "标量参数，如 {\"angle\": 90}；gui_input 用单个 args 字符串（click→\"x,y\"，type→文本，key→\"ctrl+c\"）"},
			},
			"required": []string{"target_actuator", "action"}},
	}
	if targetLoc == i18n.English {
		desc = "Dispatch a task to an agent for execution. Must be called when the user asks to schedule an agent to do work (or output a task JSON directive). title: task title, target: goal to achieve, abilities: capability IDs from Connected Devices (e.g. agent:codex)."
		props = map[string]any{
			"title":              map[string]any{"type": "string", "description": "Task title (required)"},
			"target":             map[string]any{"type": "string", "description": "Goal to achieve (required)"},
			"abilities":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Capability IDs, e.g. agent:codex (required)"},
			"node":               map[string]any{"type": "string", "description": "Target node (optional, HARD pin: required when the user names a device — the task runs only there, waits for the link if offline, never silently runs elsewhere. Leave empty only when the scheduler should choose)"},
			"scope":              map[string]any{"type": "string", "description": "Allowed relative paths to modify, comma-separated (optional)"},
			"constraints":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Constraints or prohibited actions (optional)"},
			"success_definition": map[string]any{"type": "string", "description": "How to verify completion (optional)"},
			"action_spec": map[string]any{"type": "object", "description": "Required when driving a hardware:* physical/desktop actuator (optional)",
				"properties": map[string]any{
					"target_actuator": map[string]any{"type": "string", "description": "hardware:* ability ID from the device list"},
					"action":          map[string]any{"type": "string", "description": "Action verb, e.g. rotate|record|snap|notify|capture|click|type|key"},
					"parameters":      map[string]any{"type": "object", "description": "Scalar parameters, e.g. {\"angle\": 90}; gui_input takes one args string (click→\"x,y\", type→text, key→\"ctrl+c\")"},
				},
				"required": []string{"target_actuator", "action"}},
		}
	}
	props["task_kind"] = map[string]any{
		"type": "string", "enum": []string{"code_question", "investigation", "edit", "operations"},
		"description": "Delivery type: investigation/code_question require source evidence; edit requires relevant verification; operations requires an observed postcondition. Does not grant permissions.",
	}
	return entry.Tool{
		Name:        entry.ToolNameTaskSubmit,
		Description: desc,
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type":       "object",
			"properties": props,
			"required":   []string{"title", "target", "abilities"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			title, _ := args["title"].(string)
			target, _ := args["target"].(string)
			if strings.TrimSpace(title) == "" || strings.TrimSpace(target) == "" {
				if targetLoc == i18n.English {
					return "", fmt.Errorf("title and target must not be empty")
				}
				return "", fmt.Errorf("title 和 target 不能为空")
			}
			abilities := toStringSlice(args["abilities"])
			if len(abilities) == 0 {
				if targetLoc == i18n.English {
					return "", fmt.Errorf("abilities must not be empty: specify capability IDs like agent:codex from Connected Devices")
				}
				return "", fmt.Errorf("abilities 不能为空：按 Connected Devices 能力摘要填写，如 agent:codex")
			}
			node, _ := args["node"].(string)
			taskScope, _ := args["scope"].(string)
			successDef, _ := args["success_definition"].(string)
			kind, _ := args["task_kind"].(string)
			var actionSpec *ledger.ActionSpec
			if raw, ok := args["action_spec"]; ok && raw != nil {
				// The argument arrives as untyped JSON; round-tripping into the
				// canonical struct rejects malformed shapes while keeping the
				// model's parameters verbatim for ValidateTaskSpec to vet.
				blob, merr := json.Marshal(raw)
				if merr != nil || json.Unmarshal(blob, &actionSpec) != nil {
					if targetLoc == i18n.English {
						return "", fmt.Errorf("action_spec is malformed: expected {\"target_actuator\",\"action\",\"parameters\"}")
					}
					return "", fmt.Errorf("action_spec 格式错误：应为 {\"target_actuator\",\"action\",\"parameters\"}")
				}
			}
			contextType := "command"
			if actionSpec != nil {
				// An actuator dispatch is a hardware action, not a shell task:
				// the context type drives queue filtering and worktree
				// decisions, and a servo has no checkout to carry.
				contextType = "hardware"
			}
			spec := &entry.TaskSpec{
				Title:       strings.TrimSpace(title),
				ContextType: contextType,
				Requires:    entry.Requires{Abilities: abilities},
				Spec: entry.TaskSpecDetail{
					Kind:              entry.TaskKind(kind),
					Target:            strings.TrimSpace(target),
					Node:              strings.TrimSpace(node),
					Scope:             strings.TrimSpace(taskScope),
					Constraints:       toStringSlice(args["constraints"]),
					SuccessDefinition: strings.TrimSpace(successDef),
					ActionSpec:        actionSpec,
				},
				Complexity: 0.5,
				Risk:       "low",
			}
			if err := entry.ValidateTaskSpec(spec); err != nil {
				return "", err
			}
			if e.sched.Load() == nil {
				e.tryAutoInitScheduler()
			}
			if e.sched.Load() == nil {
				if targetLoc == i18n.English {
					return "", fmt.Errorf("no capability cards loaded, unable to dispatch task")
				}
				return "", fmt.Errorf("未加载能力卡片，无法派发任务")
			}
			cb.progress(Progress{Kind: ProgressTask, Name: spec.Title})
			// The tool is a dispatch, not the turn's outcome: in queue mode it
			// returns the queued receipt (awaitQueued=false) so several calls
			// in one round — e.g. tasks for different projects — all land in
			// the scheduler at once and run in parallel instead of serializing
			// inside the tool loop.
			res := e.submitTask(ctx, spec, prompt, authorize, scope, "", cb, false)
			capture.add(res)
			switch {
			case res.NeedsApproval:
				if targetLoc == i18n.English {
					return fmt.Sprintf("Task \"%s\" created (ID %s), waiting for user approval before execution (type /approve in REPL, or run panda task approve %s).", spec.Title, res.TaskID, res.TaskID), nil
				}
				return fmt.Sprintf("任务「%s」已创建（ID %s），等待用户批准后执行（REPL 输入 /approve，或 panda task approve %s）。", spec.Title, res.TaskID, res.TaskID), nil
			case res.Denied:
				if targetLoc == i18n.English {
					return "", fmt.Errorf("task dispatch refused: %s", strings.TrimSpace(res.Stderr))
				}
				return "", fmt.Errorf("任务派发被拒：%s", strings.TrimSpace(res.Stderr))
			case res.TaskID == "":
				if targetLoc == i18n.English {
					return "", fmt.Errorf("task dispatch failed: %s", strings.TrimSpace(res.Stderr))
				}
				return "", fmt.Errorf("任务派发失败：%s", strings.TrimSpace(res.Stderr))
			}
			var msg string
			if targetLoc == i18n.English {
				msg = fmt.Sprintf("Task \"%s\" dispatched: ID %s, status %s.", spec.Title, res.TaskID, res.TaskState)
				if out := strings.TrimSpace(res.Stdout); out != "" {
					msg += "\nExecution result:\n" + excerpt(out, 2000)
				}
				if res.TaskState == core.StateFailed && strings.TrimSpace(res.Stderr) != "" {
					msg += "\nFailure details:\n" + excerpt(strings.TrimSpace(res.Stderr), 1000)
				}
			} else {
				msg = fmt.Sprintf("任务「%s」已派发：ID %s，状态 %s。", spec.Title, res.TaskID, zhTaskState(res.TaskState))
				if out := strings.TrimSpace(res.Stdout); out != "" {
					msg += "\n执行结果：\n" + excerpt(out, 2000)
				}
				if res.TaskState == core.StateFailed && strings.TrimSpace(res.Stderr) != "" {
					msg += "\n失败信息：\n" + excerpt(strings.TrimSpace(res.Stderr), 1000)
				}
			}
			return msg, nil
		},
	}
}

// taskBudgetTool builds task_budget — the model's own way to widen the round's
// dispatch limit. The limit is derived (defaultTaskBudget, or the roster's
// size; see budget.go), so a fan-out nobody enumerated — "check these five
// modules", with no team picked — would otherwise be cut at the floor with the
// model unable to do anything about it. Declaring the count here is auditable
// (one tool call, one ceiling) where parsing a number out of prose is a guess
// in five languages.
//
// Tier 1: it mutates nothing outside this ask's counter.
func (e *Engine) taskBudgetTool(budget *taskBudget, loc ...i18n.Locale) entry.Tool {
	targetLoc := i18n.ChineseSimp
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	}
	desc := "提高本轮对话的子任务派发上限。上限默认 3，会话选定的 agent 名单会按人数自动放宽；当你确实要派发的任务多于当前上限时，先调用它申报本轮需要的任务总数（最高 " + strconv.Itoa(taskCeiling) + "），再逐个派发。不要为了「以防万一」预先调高。"
	props := map[string]any{
		"count":  map[string]any{"type": "integer", "description": "本轮需要的子任务总数（含已派发的）"},
		"reason": map[string]any{"type": "string", "description": "为什么需要这么多（一句话，会记录在案）"},
	}
	if targetLoc == i18n.English {
		desc = "Raise this round's subagent dispatch limit. The limit defaults to 3 and a conversation roster raises it to the number of agents the user picked; when you genuinely need to dispatch more than the current limit, call this with the total number of tasks the round needs (ceiling " + strconv.Itoa(taskCeiling) + ") before dispatching them. Do not raise it pre-emptively."
		props = map[string]any{
			"count":  map[string]any{"type": "integer", "description": "Total subagent tasks this round needs, including ones already dispatched"},
			"reason": map[string]any{"type": "string", "description": "Why that many (one sentence; recorded)"},
		}
	}
	return entry.Tool{
		Name:        entry.ToolNameTaskBudget,
		Description: desc,
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type":       "object",
			"properties": props,
			"required":   []string{"count"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			want := intArg(args, "count")
			if want <= 0 {
				if targetLoc == i18n.English {
					return "", fmt.Errorf("count must be a positive integer")
				}
				return "", fmt.Errorf("count 必须是正整数")
			}
			limit := budget.request(want)
			// The model gets the number in force, not the number it asked
			// for: the ceiling can answer differently, and a model that
			// believes it has ten slots when it has eight will lose two
			// tasks to the very refusal this tool exists to prevent.
			clamped := ""
			if want > limit {
				if targetLoc == i18n.English {
					clamped = fmt.Sprintf(" (asked for %d; the ceiling is %d)", want, limit)
				} else {
					clamped = fmt.Sprintf("（申报 %d，上限为 %d）", want, limit)
				}
			}
			if targetLoc == i18n.English {
				return fmt.Sprintf("Round dispatch limit is now %d; %d used, %d remaining%s.", limit, budget.used, budget.remaining(), clamped), nil
			}
			return fmt.Sprintf("本轮子任务上限已提高到 %d：已用 %d，剩余 %d%s。", limit, budget.used, budget.remaining(), clamped), nil
		},
	}
}

// intArg reads a JSON number argument. Tool arguments arrive as decoded JSON,
// so an integer the model emitted is a float64 — and a string ("4") is what
// some compatible endpoints send instead.
func intArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

// toolConsentHint tells the model — so it can tell the user — how to grant the
// consent a refused tool needs: one standing grant per surface (the /authorize
// toggle in the REPL, --authorize for one-shot asks, the authorize checkbox in
// the web console). It is phrased as data for the model, not as an instruction.
func toolConsentHint(t entry.Tool, approvalMode string, loc ...i18n.Locale) string {
	targetLoc := i18n.ChineseSimp
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	}
	if approvalMode == config.ApprovalModeAlways {
		if targetLoc == i18n.English {
			return fmt.Sprintf("Tool %s is a tier-2 (irreversible) operation and approval.mode is 'always': a session grant cannot pre-consent — each irreversible action must be approved by the user in the foreground. Tell the user the operation needs their explicit decision (or to switch approval.mode to on-request).", t.Name)
		}
		return fmt.Sprintf("工具 %s 属 tier-2（不可逆）操作，且当前审批模式为 always：会话授权不能预先放行，每个不可逆操作都需用户在前台逐个批准。请将该操作需要用户明确决定一事告知用户（或将 approval.mode 调整为 on-request）。", t.Name)
	}
	if targetLoc == i18n.English {
		return fmt.Sprintf("Tool %s is a tier-2 (irreversible) operation, which was not authorized for this session. Please ask user to grant authorization and retry (/authorize in REPL, --authorize flag, or check 'Authorize' in web panel).", t.Name)
	}
	return fmt.Sprintf("工具 %s 属 tier-2（不可逆）操作，本次会话未开启授权。请让用户开启授权后重试（REPL 输入 /authorize，一次性调用加 --authorize，Web 面板勾选“授权”）。", t.Name)
}

// resultAt reads one tool result and bounds it before it enters the replayed
// transcript. Calls and results are only equal in length on the happy path: a
// round that aborts part-way stops dispatching while the call list is already
// complete, so indexing past the end would panic inside the very loop that
// exists to keep one bad tool from taking the ask down. A whole-file tool dump
// is bounded for the same reason — the full text still reaches the user
// through the progress lines and the interruption digest.
func resultAt(results []string, i int) string {
	if i < 0 || i >= len(results) {
		return ""
	}
	return boundToolResult(results[i], toolResultHardCap)
}

// appendToolCalls records one round of tool calls — every call the model
// emitted, its accompanying note (text emitted alongside the calls), and each
// result — in the conversation. Calls carrying a tool_use id are replayed as a
// single assistant turn of tool_use blocks followed by one user turn holding
// every matching tool_result (the Anthropic Messages API contract requires all
// tool_uses of an assistant turn to be answered in the next user message);
// id-less calls (DSML / text-JSON fallback) are carried as prose pairs,
// preserving the pre-tool_use behavior.
func appendToolCalls(turns []entry.Turn, calls []*entry.ToolCall, note string, results []string, loc ...i18n.Locale) []entry.Turn {
	targetLoc := i18n.ChineseSimp
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	}
	blocks := false
	for _, call := range calls {
		if call != nil && call.ID != "" {
			blocks = true
			break
		}
	}
	if blocks {
		assistant := entry.Turn{Role: "assistant"}
		if note != "" {
			assistant.Blocks = append(assistant.Blocks, entry.ContentBlock{Type: "text", Text: note})
		}
		user := entry.Turn{Role: "user"}
		for i, call := range calls {
			if call == nil || call.ID == "" {
				continue
			}
			assistant.Blocks = append(assistant.Blocks, entry.ContentBlock{Type: "tool_use", ID: call.ID, Name: call.Tool, Input: call.Arguments})
			user.Blocks = append(user.Blocks, entry.ContentBlock{Type: "tool_result", ToolUseID: call.ID, Content: resultAt(results, i)})
		}
		turns = append(turns, assistant, user)
		note = ""
	}
	prefix := "工具结果："
	if targetLoc == i18n.English {
		prefix = "Tool result: "
	}
	for i, call := range calls {
		if call == nil || call.ID != "" {
			continue
		}
		blob, _ := json.Marshal(call)
		msg := "tool_call: " + string(blob)
		if note != "" {
			msg = note + "\n" + msg
			note = ""
		}
		turns = append(turns,
			entry.Turn{Role: "assistant", Content: msg},
			entry.Turn{Role: "user", Content: prefix + resultAt(results, i)},
		)
	}
	return turns
}

// The interruption digest is a last-resort reply, not a transcript. A long
// exploration can execute dozens of tools; printing even 300 runes of each
// buries the one useful fact ("your question was not answered") under a wall
// of file dumps. Keep a handful of one-line entries and fold the rest.
const (
	digestMaxLines  = 8
	digestLineRunes = 120
)

// digestExcerpt is a plain, user-facing truncation. excerpt() must not be
// reused here: it inserts prompt.subagent.excerpt_omitted, an instruction
// written for the model ("the full result was already shown to the user, no
// need to re-read"). Leaking that marker into the reply is what made the
// interruption look like a raw work log pasted from a prompt.
func digestExcerpt(s string, limit int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}

// toolResultsDigest renders the "tool: result" lines collected across the
// rounds when the model fails mid-loop or on the final convergence call: the
// operations already happened, so the honest answer is what they did, plus a
// note that the final summary never came back. Empty when nothing ran.
func toolResultsDigest(digest []string, loc i18n.Locale) string {
	if len(digest) == 0 {
		return ""
	}
	var b strings.Builder
	if loc == i18n.English {
		fmt.Fprintf(&b, "Incomplete reply: the model stopped responding after %d tool operation(s). Operations that did run (excerpted):\n", len(digest))
	} else {
		fmt.Fprintf(&b, "本次回答未完成：模型在完成 %d 项工具操作后停止响应。已执行的操作（节选）：\n", len(digest))
	}
	shown := 0
	for _, line := range digest {
		if shown >= digestMaxLines {
			break
		}
		b.WriteString("- " + digestExcerpt(line, digestLineRunes) + "\n")
		shown++
	}
	if len(digest) > shown {
		if loc == i18n.English {
			fmt.Fprintf(&b, "- ... and %d more operation(s) not listed individually.\n", len(digest)-shown)
		} else {
			fmt.Fprintf(&b, "- ……另有 %d 项操作未逐条列出。\n", len(digest)-shown)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// interruptionNote is the compact stand-in for the raw digest when a TASK
// already carries the outcome: the model was cut off mid-loop, the task block
// is the real result, and the tool transcript belongs behind `task show`
// rather than dumped into the reply.
func interruptionNote(n int, taskID string, loc i18n.Locale) string {
	if loc == i18n.English {
		return fmt.Sprintf("The model stopped after %d tool operation(s) without a final summary. The task result above is what actually ran; full output: `panda task show %s`.", n, taskID)
	}
	return fmt.Sprintf("模型在完成 %d 项工具操作后中断，未生成最终总结。上方任务结果即为实际执行内容；完整输出见 `panda task show %s`。", n, taskID)
}

// registerMCPTools lists the tools a stdio MCP server advertises and registers
// each as an entry-model tool whose Run delegates to the server. The server is
// spawned and owned by the Engine; this only imports its tool surface.
func registerMCPTools(ctx context.Context, reg *entry.Registry, client *mcp.Client) error {
	tools, err := client.ListTools(ctx)
	if err != nil {
		return fmt.Errorf("mcp tools/list: %w", err)
	}
	for _, t := range tools {
		t := t
		reg.Register(entry.Tool{
			Name:        t.Name,
			Description: t.Description,
			// An MCP server is user-configured opt-in (the user wrote its
			// command into config.yaml), which is the standing consent a
			// Tier-1 grading needs; the server's own tools police their own
			// side effects.
			Tier:   defense.TierReversible,
			Schema: t.InputSchema,
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				return client.CallTool(ctx, t.Name, args)
			},
		})
	}
	return nil
}

// splitCommand splits a command string into argv, honoring single and double
// quotes so a path with spaces survives (e.g. `prog /path/with space` or
// `prog "/path/with space"`). It is a minimal shell-word splitter, not a full
// shell parser.
func splitCommand(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	inField := false
	flush := func() {
		if inField {
			out = append(out, cur.String())
			cur.Reset()
			inField = false
		}
	}
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
			inField = true
		case r == '\'' || r == '"':
			quote = r
			inField = true
		case r == ' ' || r == '\t' || r == '\n':
			flush()
		default:
			cur.WriteRune(r)
			inField = true
		}
	}
	flush()
	return out
}

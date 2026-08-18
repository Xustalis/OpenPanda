package askengine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/xenith/openpanda/internal/entry"
	"github.com/xenith/openpanda/internal/mcp"
	"github.com/xenith/openpanda/internal/memory"
	"github.com/xenith/openpanda/internal/reminders"
)

// buildToolRegistry wires the memory, system-data (time/weather), reminder,
// and MCP tools into the entry-model tool registry. The schemas live here
// (the assembly layer) rather than in the prompt or in the memory package,
// so entry and memory stay decoupled: entry owns the registry, memory owns
// the executor, and this package glues them together.
func buildToolRegistry(hermes *memory.Hermes, projects *memory.Projects, rem *reminders.Store) *entry.Registry {
	mem := memory.NewTool(hermes, projects)
	reg := entry.NewRegistry()

	targetEnum := map[string]any{"type": "string", "enum": []string{"user", "memory", "project"}}
	projectArg := map[string]any{"type": "string", "description": "project name (used when target=project)"}

	reg.Register(entry.Tool{
		Name:        memory.ToolRead,
		Description: "列出当前记忆条目（合并/删除前先读）。target 可选：user / memory / project。",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"target": targetEnum, "project": projectArg},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return mem.Execute(memory.ToolRead, args)
		},
	})

	reg.Register(entry.Tool{
		Name:        memory.ToolAdd,
		Description: "记住一条新记忆。target：user（用户偏好/沟通风格）、memory（环境事实/全局约定/纠正）、project（项目约定）。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target":  targetEnum,
				"entry":   map[string]any{"type": "string", "description": "要记住的内容"},
				"project": projectArg,
			},
			"required": []string{"target", "entry"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return mem.Execute(memory.ToolAdd, args)
		},
	})

	reg.Register(entry.Tool{
		Name:        memory.ToolReplace,
		Description: "替换一条已有记忆。old 用能唯一匹配该条目的子串（匹配到多条会报错，需给更具体子串）。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target":  targetEnum,
				"old":     map[string]any{"type": "string", "description": "能唯一匹配待替换条目的子串"},
				"new":     map[string]any{"type": "string", "description": "替换后的内容"},
				"project": projectArg,
			},
			"required": []string{"target", "old", "new"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return mem.Execute(memory.ToolReplace, args)
		},
	})

	reg.Register(entry.Tool{
		Name:        memory.ToolRemove,
		Description: "删除一条记忆。old 用能唯一匹配该条目的子串。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target":  targetEnum,
				"old":     map[string]any{"type": "string", "description": "能唯一匹配待删除条目的子串"},
				"project": projectArg,
			},
			"required": []string{"target", "old"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return mem.Execute(memory.ToolRemove, args)
		},
	})

	// System-data tools (§7.3): the model has no clock or senses of its own.
	registerTimeTool(reg)
	registerWeatherTool(reg)

	if rem != nil {
		registerReminderTools(reg, rem)
	}

	return reg
}

// registerReminderTools adds reminder.set / reminder.list — the design's
// P1-28 "提醒我 5 分钟后开会" surface. The scanner that actually fires them
// lives in the daemon / web panel (reminders.Scanner); the CLI ask process
// is short-lived and only writes them.
func registerReminderTools(reg *entry.Registry, rem *reminders.Store) {
	reg.Register(entry.Tool{
		Name:        "reminder.set",
		Description: "设置一个定时提醒。after_minutes 填“多少分钟后提醒”；at 填绝对时间（RFC3339，如 2026-08-18T15:00:00+08:00，或不带时区则按本地时间）。两个参数二选一。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message":       map[string]any{"type": "string", "description": "提醒内容，如“开会”"},
				"after_minutes": map[string]any{"type": "number", "description": "多少分钟后提醒（与 at 二选一）"},
				"at":            map[string]any{"type": "string", "description": "提醒的绝对时间，RFC3339 或 \"2006-01-02 15:04\"（与 after_minutes 二选一）"},
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
		Name:        "reminder.list",
		Description: "列出当前所有未触发的提醒。",
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

// executeTool runs a tool call through the registry and returns a user-facing
// result (a tool failure is folded into the result, not a hard exit, so the
// model can consolidate and retry).
func executeTool(ctx context.Context, reg *entry.Registry, call *entry.ToolCall) string {
	t, ok := reg.Lookup(call.Tool)
	if !ok {
		return "工具执行失败：未知工具 " + call.Tool
	}
	result, err := t.Run(ctx, call.Arguments)
	if err != nil {
		return "工具执行失败：" + err.Error()
	}
	return result
}

// appendToolTurns records a tool call, its accompanying note (text the model
// emitted alongside the call or extra tool_use the executor skipped), and its
// result in the conversation. A native tool_use call is replayed as tool_use +
// tool_result blocks (the Anthropic Messages API contract); the text-JSON
// fallback (no tool_use id) is carried as prose, preserving the pre-tool_use
// behavior.
func appendToolTurns(turns []entry.Turn, call *entry.ToolCall, note, result string) []entry.Turn {
	if call.ID != "" {
		assistant := entry.Turn{Role: "assistant"}
		if note != "" {
			assistant.Blocks = append(assistant.Blocks, entry.ContentBlock{Type: "text", Text: note})
		}
		assistant.Blocks = append(assistant.Blocks, entry.ContentBlock{Type: "tool_use", ID: call.ID, Name: call.Tool, Input: call.Arguments})
		return append(turns,
			assistant,
			entry.Turn{Role: "user", Blocks: []entry.ContentBlock{
				{Type: "tool_result", ToolUseID: call.ID, Content: result},
			}},
		)
	}
	assistant, _ := json.Marshal(call)
	msg := "tool_call: " + string(assistant)
	if note != "" {
		msg = note + "\n" + msg
	}
	return append(turns,
		entry.Turn{Role: "assistant", Content: msg},
		entry.Turn{Role: "user", Content: "工具结果：" + result},
	)
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
			Schema:      t.InputSchema,
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

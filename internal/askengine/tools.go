package askengine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xenith/openpanda/internal/entry"
	"github.com/xenith/openpanda/internal/mcp"
	"github.com/xenith/openpanda/internal/memory"
)

// buildToolRegistry wires the memory tools into the entry-model tool registry.
// The schemas live here (the assembly layer) rather than in the prompt or in
// the memory package, so entry and memory stay decoupled: entry owns the
// registry, memory owns the executor, and this package glues them together.
func buildToolRegistry(hermes *memory.Hermes, projects *memory.Projects) *entry.Registry {
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

	return reg
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

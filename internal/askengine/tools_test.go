package askengine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/xenith/openpanda/internal/entry"
)

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"prog arg1 arg2", []string{"prog", "arg1", "arg2"}},
		{`prog "/path with space" arg`, []string{"prog", "/path with space", "arg"}},
		{`prog 'single quoted'`, []string{"prog", "single quoted"}},
		{`  prog   a  b  `, []string{"prog", "a", "b"}},
		{"prog", []string{"prog"}},
		{"", nil},
	}
	for _, tc := range cases {
		if got := splitCommand(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitCommand(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func newTestRegistry() *entry.Registry {
	reg := entry.NewRegistry()
	reg.Register(entry.Tool{
		Name:        "echo",
		Description: "echo the x argument",
		Schema:      map[string]any{"type": "object"},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return fmt.Sprintf("got %v", args["x"]), nil
		},
	})
	reg.Register(entry.Tool{
		Name:        "failing",
		Description: "always fails",
		Schema:      map[string]any{"type": "object"},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return "", errors.New("boom")
		},
	})
	return reg
}

func TestExecuteTool(t *testing.T) {
	reg := newTestRegistry()
	ctx := context.Background()

	if got := executeTool(ctx, reg, &entry.ToolCall{Tool: "echo", Arguments: map[string]any{"x": 1}}); got != "got 1" {
		t.Errorf("executeTool = %q, want success result", got)
	}
	// A tool failure is folded into the result, never a hard exit.
	if got := executeTool(ctx, reg, &entry.ToolCall{Tool: "failing"}); !strings.Contains(got, "boom") {
		t.Errorf("executeTool failure = %q, want it to carry the error", got)
	}
	if got := executeTool(ctx, reg, &entry.ToolCall{Tool: "nope"}); !strings.Contains(got, "未知工具") {
		t.Errorf("executeTool unknown = %q, want unknown-tool message", got)
	}
}

func TestAppendToolTurnsNative(t *testing.T) {
	turns := []entry.Turn{{Role: "user", Content: "记住我偏好暗色主题"}}
	call := &entry.ToolCall{ID: "toolu_1", Tool: "memory_add", Arguments: map[string]any{"entry": "x"}}
	turns = appendToolTurns(turns, call, "", "已记住")

	if len(turns) != 3 {
		t.Fatalf("turns = %d, want 3", len(turns))
	}
	assistant := turns[1]
	if len(assistant.Blocks) != 1 || assistant.Blocks[0].Type != "tool_use" ||
		assistant.Blocks[0].ID != "toolu_1" || assistant.Blocks[0].Name != "memory_add" {
		t.Fatalf("assistant blocks = %+v, want the tool_use replay", assistant.Blocks)
	}
	user := turns[2]
	if len(user.Blocks) != 1 || user.Blocks[0].Type != "tool_result" ||
		user.Blocks[0].ToolUseID != "toolu_1" || user.Blocks[0].Content != "已记住" {
		t.Fatalf("user blocks = %+v, want the matching tool_result", user.Blocks)
	}
}

func TestAppendToolTurnsNativeWithNote(t *testing.T) {
	turns := []entry.Turn{{Role: "user", Content: "合并记忆"}}
	call := &entry.ToolCall{ID: "toolu_1", Tool: "memory_read", Arguments: map[string]any{"target": "user"}}
	// Accompanying text and an extra tool_use are replayed as an assistant text
	// block ahead of the executed tool_use, so the model sees them next round.
	turns = appendToolTurns(turns, call, "先读记忆再合并\n工具 memory_add(target=memory)", "已读")

	if len(turns) != 3 {
		t.Fatalf("turns = %d, want 3", len(turns))
	}
	assistant := turns[1]
	if len(assistant.Blocks) != 2 || assistant.Blocks[0].Type != "text" ||
		assistant.Blocks[1].Type != "tool_use" {
		t.Fatalf("assistant blocks = %+v, want [text, tool_use]", assistant.Blocks)
	}
	if !strings.Contains(assistant.Blocks[0].Text, "先读记忆再合并") {
		t.Fatalf("assistant text = %q, want the note", assistant.Blocks[0].Text)
	}
}

func TestAppendToolTurnsTextFallback(t *testing.T) {
	turns := []entry.Turn{{Role: "user", Content: "hi"}}
	// No tool_use id: the pre-tool_use text fallback must carry the call and
	// result as prose.
	call := &entry.ToolCall{Tool: "memory_add", Arguments: map[string]any{"entry": "x"}}
	turns = appendToolTurns(turns, call, "", "已记住")

	if len(turns) != 3 {
		t.Fatalf("turns = %d, want 3", len(turns))
	}
	if len(turns[1].Blocks) != 0 || !strings.Contains(turns[1].Content, "memory_add") {
		t.Fatalf("assistant fallback = %+v, want prose carrying the call", turns[1])
	}
	if len(turns[2].Blocks) != 0 || !strings.Contains(turns[2].Content, "已记住") {
		t.Fatalf("user fallback = %+v, want prose carrying the result", turns[2])
	}
}

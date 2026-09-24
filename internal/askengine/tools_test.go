package askengine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

func TestTaskDispatchCaptureTakeClears(t *testing.T) {
	first := &Result{Kind: "task", TaskID: "task-1"}
	second := &Result{Kind: "task", TaskID: "task-2"}
	var capture taskDispatchCapture
	capture.add(first)
	capture.add(second)

	got := capture.takeAll()
	if len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("takeAll = %+v, want both captured results in order", got)
	}
	if got := capture.takeAll(); len(got) != 0 {
		t.Fatalf("second takeAll = %+v, want empty after drain", got)
	}
}

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

	if got := executeTool(ctx, reg, &entry.ToolCall{Tool: "echo", Arguments: map[string]any{"x": 1}}, true, ""); got != "got 1" {
		t.Errorf("executeTool = %q, want success result", got)
	}
	// A tool failure is folded into the result, never a hard exit.
	if got := executeTool(ctx, reg, &entry.ToolCall{Tool: "failing"}, true, ""); !strings.Contains(got, "boom") {
		t.Errorf("executeTool failure = %q, want it to carry the error", got)
	}
	if got := executeTool(ctx, reg, &entry.ToolCall{Tool: "nope"}, true, ""); !strings.Contains(got, "未知工具") {
		t.Errorf("executeTool unknown = %q, want unknown-tool message", got)
	}
}

// TestExecuteToolTierGate verifies the fail-closed tool gate: a tool that
// declares no tier (0) is treated as Tier 2 and refused without consent, the
// refusal carrying the consent hint instead of a bare error.
func TestExecuteToolTierGate(t *testing.T) {
	reg := newTestRegistry()
	ctx := context.Background()

	// "echo" declares no tier: 0 is fail-closed (graded Tier 2), so an
	// unauthorized ask is refused before Run.
	if got := executeTool(ctx, reg, &entry.ToolCall{Tool: "echo"}, false, ""); !strings.Contains(got, "被拒") {
		t.Errorf("executeTool unauthorized = %q, want the refusal", got)
	}
	// The same call runs under consent.
	if got := executeTool(ctx, reg, &entry.ToolCall{Tool: "echo", Arguments: map[string]any{"x": 1}}, true, ""); got != "got 1" {
		t.Errorf("executeTool authorized = %q, want success result", got)
	}
}

func TestAppendToolTurnsNative(t *testing.T) {
	turns := []entry.Turn{{Role: "user", Content: "记住我偏好暗色主题"}}
	call := &entry.ToolCall{ID: "toolu_1", Tool: "memory_add", Arguments: map[string]any{"entry": "x"}}
	turns = appendToolCalls(turns, []*entry.ToolCall{call}, "", []string{"已记住"})

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

// A batch response replays as ONE assistant turn carrying every tool_use
// block and ONE user turn answering all of them — the Anthropic Messages API
// contract requires all tool_uses of an assistant turn to be answered in the
// next user message.
func TestAppendToolCallsNativeBatch(t *testing.T) {
	turns := []entry.Turn{{Role: "user", Content: "清理队列"}}
	calls := []*entry.ToolCall{
		{ID: "toolu_1", Tool: "taskq_cancel", Arguments: map[string]any{"task_id": "a1"}},
		{ID: "toolu_2", Tool: "taskq_cancel", Arguments: map[string]any{"task_id": "a2"}},
		{ID: "toolu_3", Tool: "taskq_cancel", Arguments: map[string]any{"task_id": "a3"}},
	}
	turns = appendToolCalls(turns, calls, "把这三个停掉。", []string{"ok a1", "ok a2", "ok a3"})

	if len(turns) != 3 {
		t.Fatalf("turns = %d, want 3", len(turns))
	}
	assistant := turns[1]
	if len(assistant.Blocks) != 4 || assistant.Blocks[0].Type != "text" {
		t.Fatalf("assistant blocks = %+v, want [text, tool_use ×3]", assistant.Blocks)
	}
	user := turns[2]
	if len(user.Blocks) != 3 {
		t.Fatalf("user blocks = %+v, want 3 tool_results", user.Blocks)
	}
	for i, id := range []string{"toolu_1", "toolu_2", "toolu_3"} {
		if assistant.Blocks[i+1].ID != id || user.Blocks[i].ToolUseID != id {
			t.Fatalf("call %d not paired: %+v / %+v", i, assistant.Blocks[i+1], user.Blocks[i])
		}
	}
}

func TestAppendToolTurnsNativeWithNote(t *testing.T) {
	turns := []entry.Turn{{Role: "user", Content: "合并记忆"}}
	call := &entry.ToolCall{ID: "toolu_1", Tool: "memory_read", Arguments: map[string]any{"target": "user"}}
	// Accompanying text is replayed as an assistant text block ahead of the
	// executed tool_use, so the model sees it next round.
	turns = appendToolCalls(turns, []*entry.ToolCall{call}, "先读记忆再合并", []string{"已读"})

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
	turns = appendToolCalls(turns, []*entry.ToolCall{call}, "", []string{"已记住"})

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

// Id-less calls (DSML / text-JSON fallback) replay as prose pairs; several of
// them keep their call order.
func TestAppendToolCallsProseBatch(t *testing.T) {
	turns := []entry.Turn{{Role: "user", Content: "清理队列"}}
	calls := []*entry.ToolCall{
		{Tool: "taskq_cancel", Arguments: map[string]any{"task_id": "a1"}},
		{Tool: "taskq_cancel", Arguments: map[string]any{"task_id": "a2"}},
	}
	turns = appendToolCalls(turns, calls, "", []string{"ok a1", "ok a2"})
	if len(turns) != 5 {
		t.Fatalf("turns = %d, want 5 (2 prose pairs)", len(turns))
	}
	if !strings.Contains(turns[1].Content, "a1") || !strings.Contains(turns[2].Content, "ok a1") ||
		!strings.Contains(turns[3].Content, "a2") || !strings.Contains(turns[4].Content, "ok a2") {
		t.Fatalf("prose pairs out of order: %+v", turns[1:])
	}
}

// TestAskTurnsMaxRoundsConverges verifies the ask loop's graceful
// degradation: a model that never stops calling tools used to surface
// "reached max tool rounds" as an error; now the engine runs one final
// tool-free call over the accumulated history, forcing a text answer — the
// ask always converges to something useful.
func TestAskTurnsMaxRoundsConverges(t *testing.T) {
	var mu sync.Mutex
	var toolCalls, plainCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		hasTools := strings.Contains(string(b), `"tools"`)
		mu.Lock()
		if hasTools {
			toolCalls++
		} else {
			plainCalls++
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if hasTools {
			// Endless tool loop: every tool-bearing request gets another call.
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"echo","arguments":"{\"x\":1}"}}]}}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"已尽力，这是最终答复"}}]}`)
	}))
	defer srv.Close()

	client, err := entry.NewClient(config.ModelConfig{
		APIType: "openai", BaseURL: srv.URL, Model: "test-model", APIKey: "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	e := &Engine{
		cfg:      &config.Config{},
		injector: memory.NewInjector(nil, nil),
		registry: newTestRegistry(),
		db:       db,
	}
	e.client.Store(client)

	// "run" keeps the ask off the Tier-1 fast path (a bare greeting like "hi"
	// is now answered conversationally, without the tool loop this test
	// exercises).
	res, err := e.AskTurns(context.Background(), nil, "run the echo tool", "", true, StreamCallbacks{})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if res.Kind != "answer" || !strings.Contains(res.Answer, "最终答复") {
		t.Fatalf("res = %+v, want a converged answer", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if toolCalls != 6 {
		t.Fatalf("tool-bearing calls = %d, want 6 (maxRounds)", toolCalls)
	}
	if plainCalls != 1 {
		t.Fatalf("final tool-free calls = %d, want 1", plainCalls)
	}
}

func TestDispatchTaskTool_MultiLanguage(t *testing.T) {
	e := &Engine{}

	// 1. Chinese (default)
	toolZh := e.dispatchTaskTool("test", AskScope{}, false, StreamCallbacks{}, &taskDispatchCapture{}, i18n.ChineseSimp)
	if !strings.Contains(toolZh.Description, "把任务派发给 agent 执行") {
		t.Errorf("expected Chinese description, got: %s", toolZh.Description)
	}
	propsZh := toolZh.Schema["properties"].(map[string]any)
	titleZh := propsZh["title"].(map[string]any)["description"].(string)
	if !strings.Contains(titleZh, "任务标题") {
		t.Errorf("expected Chinese title description, got: %s", titleZh)
	}

	// 2. English
	toolEn := e.dispatchTaskTool("test", AskScope{}, false, StreamCallbacks{}, &taskDispatchCapture{}, i18n.English)
	if !strings.Contains(toolEn.Description, "Dispatch a task to an agent") {
		t.Errorf("expected English description, got: %s", toolEn.Description)
	}
	propsEn := toolEn.Schema["properties"].(map[string]any)
	titleEn := propsEn["title"].(map[string]any)["description"].(string)
	if !strings.Contains(titleEn, "Task title") {
		t.Errorf("expected English title description, got: %s", titleEn)
	}
}

func TestExecuteTool_MultiLanguage(t *testing.T) {
	reg := entry.NewRegistry()
	reg.Register(entry.Tool{
		Name: "test_tool",
		Tier: defense.TierIrreversible,
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return "done", nil
		},
	})

	// Unauthorized refusal in English
	resEn := executeTool(context.Background(), reg, &entry.ToolCall{Tool: "test_tool"}, false, "", i18n.English)
	if !strings.Contains(resEn, "Tool execution refused") || !strings.Contains(resEn, "requires authorization") {
		t.Errorf("expected English refusal message, got: %s", resEn)
	}

	// Unknown tool in English
	unknownEn := executeTool(context.Background(), reg, &entry.ToolCall{Tool: "nonexistent"}, true, "", i18n.English)
	if !strings.Contains(unknownEn, "Tool execution failed: unknown tool") {
		t.Errorf("expected English unknown tool error, got: %s", unknownEn)
	}

	// Unknown tool in Chinese
	unknownZh := executeTool(context.Background(), reg, &entry.ToolCall{Tool: "nonexistent"}, true, "", i18n.ChineseSimp)
	if !strings.Contains(unknownZh, "工具执行失败：未知工具") {
		t.Errorf("expected Chinese unknown tool error, got: %s", unknownZh)
	}
}

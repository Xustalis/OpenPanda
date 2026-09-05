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
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/storage"
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

	if got := executeTool(ctx, reg, &entry.ToolCall{Tool: "echo", Arguments: map[string]any{"x": 1}}, true); got != "got 1" {
		t.Errorf("executeTool = %q, want success result", got)
	}
	// A tool failure is folded into the result, never a hard exit.
	if got := executeTool(ctx, reg, &entry.ToolCall{Tool: "failing"}, true); !strings.Contains(got, "boom") {
		t.Errorf("executeTool failure = %q, want it to carry the error", got)
	}
	if got := executeTool(ctx, reg, &entry.ToolCall{Tool: "nope"}, true); !strings.Contains(got, "未知工具") {
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
	if got := executeTool(ctx, reg, &entry.ToolCall{Tool: "echo"}, false); !strings.Contains(got, "被拒") {
		t.Errorf("executeTool unauthorized = %q, want the refusal", got)
	}
	// The same call runs under consent.
	if got := executeTool(ctx, reg, &entry.ToolCall{Tool: "echo", Arguments: map[string]any{"x": 1}}, true); got != "got 1" {
		t.Errorf("executeTool authorized = %q, want success result", got)
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

package entry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// deepseekPassbackBody is the rejection DeepSeek's Anthropic endpoint returns
// when a multi-turn request in thinking mode omits the assistant's thinking
// blocks. The wire-layer probe keys on the trailing clause, which the OpenAI
// variant ("The reasoning_content in the thinking mode …") shares.
const deepseekPassbackBody = `{"error":{"type":"invalid_request_error","message":"The content[].thinking in the thinking mode must be passed back to the API."}}`

// passbackTurns is a multi-turn history of the shape the ask loop builds once
// a task round or a tool round has run: a plain assistant turn and an
// assistant turn carrying a tool_use block, neither of which holds a thinking
// block (reasoning is display-only scratch per D14 and never re-sent).
func passbackTurns() []Turn {
	return []Turn{
		{Role: "user", Content: "记住我喜欢暗色主题"},
		{Role: "assistant", Blocks: []ContentBlock{
			{Type: "tool_use", ID: "toolu_1", Name: "memory_add", Input: map[string]any{"entry": "暗色主题"}},
		}},
		{Role: "user", Blocks: []ContentBlock{
			{Type: "tool_result", ToolUseID: "toolu_1", Content: "已记住"},
		}},
		{Role: "assistant", Content: "已派发子代理任务：跑测试"},
		{Role: "user", Content: "[子代理任务结果] 跑测试 成功"},
	}
}

// wireMessage captures the fields the passback tests assert on; content stays
// raw because it is either a JSON string (plain turn) or a block array.
type wireMessage struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content"`
	ReasoningContent string          `json:"reasoning_content"`
}

func decodeMessages(t *testing.T, r *http.Request) []wireMessage {
	t.Helper()
	var req struct {
		Messages []wireMessage `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return req.Messages
}

// contentBlocks returns the message's content parsed as a block array, or
// ok=false when the content is a plain JSON string.
func contentBlocks(raw json.RawMessage) (blocks []map[string]any, ok bool) {
	if len(raw) == 0 || raw[0] != '[' {
		return nil, false
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, false
	}
	return blocks, true
}

func hasThinkingBlock(raw json.RawMessage) bool {
	blocks, ok := contentBlocks(raw)
	if !ok {
		return false
	}
	for _, b := range blocks {
		if b["type"] == "thinking" {
			return true
		}
	}
	return false
}

func isStringContent(raw json.RawMessage) bool {
	return len(raw) > 0 && raw[0] == '"'
}

// writeAnthropicText answers one Messages API call with a single text block.
func writeAnthropicText(w http.ResponseWriter, text string) {
	w.Header().Set("content-type", "application/json")
	b, _ := json.Marshal(map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
	})
	_, _ = w.Write(b)
}

// TestAnthropicPassbackProbeAndRetry walks the full probe cycle on the
// non-streaming Messages path: the first call is rejected with DeepSeek's
// passback 400, the retry carries a thinking block on every assistant turn
// (the plain one and the tool_use one alike) while user turns stay untouched,
// and the sticky flag makes the FOLLOWING call inject up front — one request,
// no wasted round-trip.
func TestAnthropicPassbackProbeAndRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		msgs := decodeMessages(t, r)
		switch n {
		case 1:
			// Pre-probe: nothing is injected, exactly today's wire shape.
			for _, m := range msgs {
				if hasThinkingBlock(m.Content) {
					t.Errorf("call 1: %s message carries a thinking block before any provider demand", m.Role)
				}
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(deepseekPassbackBody))
		case 2:
			// Post-probe retry: every assistant turn carries a leading
			// thinking block; user turns keep their original shape.
			var sawAssistant bool
			for _, m := range msgs {
				switch m.Role {
				case "assistant":
					sawAssistant = true
					blocks, ok := contentBlocks(m.Content)
					if !ok {
						t.Errorf("call 2: assistant content is not a block array: %s", m.Content)
						continue
					}
					if len(blocks) == 0 || blocks[0]["type"] != "thinking" {
						t.Errorf("call 2: assistant content does not lead with a thinking block: %s", m.Content)
					}
				case "user":
					if hasThinkingBlock(m.Content) {
						t.Errorf("call 2: user message must not gain a thinking block")
					}
				}
			}
			if !sawAssistant {
				t.Errorf("call 2: no assistant message seen")
			}
			writeAnthropicText(w, "done")
		default:
			// Sticky: the third call injects up front — its FIRST (and only)
			// request already satisfies the provider.
			for _, m := range msgs {
				if m.Role == "assistant" && !hasThinkingBlock(m.Content) {
					t.Errorf("call %d: sticky flag lost; assistant sent without thinking block", n)
				}
			}
			writeAnthropicText(w, "again")
		}
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	text, err := c.CompleteTurns(context.Background(), "sys", passbackTurns())
	if err != nil {
		t.Fatalf("complete after probe: %v", err)
	}
	if text != "done" {
		t.Fatalf("text = %q, want the retried answer", text)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("calls = %d, want the 400 probe plus one retry", n)
	}

	// The flag sticks: the next multi-turn call spends a single request.
	text, err = c.CompleteTurns(context.Background(), "sys", passbackTurns())
	if err != nil {
		t.Fatalf("complete after sticky: %v", err)
	}
	if text != "again" {
		t.Fatalf("text = %q, want the sticky-path answer", text)
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("calls = %d, want the sticky call to spend one request", n)
	}
}

// TestAnthropicStreamPassbackProbeAndRetry covers the streaming Messages
// path: the 400 lands before any SSE event, so the probe retry inside
// streamAnthropic is safe, and the retried stream delivers the answer.
func TestAnthropicStreamPassbackProbeAndRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(deepseekPassbackBody))
			return
		}
		for _, m := range decodeMessages(t, r) {
			if m.Role == "assistant" && !hasThinkingBlock(m.Content) {
				t.Errorf("stream retry: assistant sent without thinking block")
			}
		}
		w.Header().Set("content-type", "text/event-stream")
		flusher := w.(http.Flusher)
		emit := func(ev map[string]any) {
			b, _ := json.Marshal(ev)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
		emit(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text"}})
		emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "ok"}})
		emit(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}})
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	resp, err := c.StreamTurnsWithTools(context.Background(), "sys", passbackTurns(), nil, nil, nil)
	if err != nil {
		t.Fatalf("stream after probe: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("text = %q, want the retried stream's answer", resp.Text)
	}
}

// TestOpenAIPassbackProbeAndRetry mirrors the probe on the Chat Completions
// wire: the passback shape there is the assistant's reasoning_content field,
// and only assistant messages may gain it.
func TestOpenAIPassbackProbeAndRetry(t *testing.T) {
	rejection := `{"error":{"message":"The reasoning_content in the thinking mode must be passed back to the API."}}`
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		msgs := decodeMessages(t, r)
		if n == 1 {
			for _, m := range msgs {
				if m.ReasoningContent != "" {
					t.Errorf("call 1: %s message carries reasoning_content before any provider demand", m.Role)
				}
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(rejection))
			return
		}
		var sawAssistant bool
		for _, m := range msgs {
			switch m.Role {
			case "assistant":
				sawAssistant = true
				if m.ReasoningContent == "" {
					t.Errorf("call 2: assistant message lacks the reasoning_content passback")
				}
			default:
				if m.ReasoningContent != "" {
					t.Errorf("call 2: %s message must not gain reasoning_content", m.Role)
				}
			}
		}
		if !sawAssistant {
			t.Errorf("call 2: no assistant message seen")
		}
		w.Header().Set("content-type", "application/json")
		b, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "done"}, "finish_reason": "stop"}},
		})
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(config.ModelConfig{APIType: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	text, err := c.CompleteTurns(context.Background(), "sys", passbackTurns())
	if err != nil {
		t.Fatalf("complete after probe: %v", err)
	}
	if text != "done" {
		t.Fatalf("text = %q, want the retried answer", text)
	}
}

// TestNoPassbackInjectionWhenProviderAccepts pins the zero-regression
// contract: a provider that accepts the bare wire shape never sees an
// injected thinking block, and the call spends exactly one request.
func TestNoPassbackInjectionWhenProviderAccepts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		for _, m := range decodeMessages(t, r) {
			if hasThinkingBlock(m.Content) {
				t.Errorf("no-demand provider must not receive thinking blocks (%s)", m.Role)
			}
		}
		writeAnthropicText(w, "plain")
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	text, err := c.CompleteTurns(context.Background(), "sys", passbackTurns())
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if text != "plain" {
		t.Fatalf("text = %q", text)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("calls = %d, want one", n)
	}
}

// TestInjectThinkingPassbackShapes exercises the injector's three content
// shapes directly: a plain-string assistant turn becomes thinking + text, a
// block-bearing turn gains a leading thinking block, an assistant turn that
// already carries thinking passes through, and other roles stay untouched.
func TestInjectThinkingPassbackShapes(t *testing.T) {
	in := []message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "dispatched"},
		{Role: "assistant", Content: []ContentBlock{{Type: "tool_use", ID: "t", Name: "n"}}},
		{Role: "assistant", Content: []ContentBlock{{Type: "thinking", Text: "kept"}, {Type: "text", Text: "x"}}},
		{Role: "user", Content: []ContentBlock{{Type: "tool_result", ToolUseID: "t", Content: "r"}}},
	}
	out := injectThinkingPassback(in)
	if len(out) != len(in) {
		t.Fatalf("out = %d messages, want %d", len(out), len(in))
	}
	// Plain-string assistant: thinking + text.
	blocks, ok := contentBlocks(mustMarshal(t, out[1].Content))
	if !ok || len(blocks) != 2 || blocks[0]["type"] != "thinking" || blocks[1]["type"] != "text" {
		t.Fatalf("plain assistant = %v, want [thinking text]", out[1].Content)
	}
	// Block-bearing assistant: thinking leads, tool_use survives.
	blocks, ok = contentBlocks(mustMarshal(t, out[2].Content))
	if !ok || len(blocks) != 2 || blocks[0]["type"] != "thinking" || blocks[1]["type"] != "tool_use" {
		t.Fatalf("tool assistant = %v, want [thinking tool_use]", out[2].Content)
	}
	// Already-thinking assistant: unchanged (no double injection).
	blocks, ok = contentBlocks(mustMarshal(t, out[3].Content))
	if !ok || len(blocks) != 2 || blocks[0]["type"] != "thinking" || blocks[0]["text"] != "kept" {
		t.Fatalf("thinking assistant = %v, want the original blocks", out[3].Content)
	}
	// User turns keep their shape entirely.
	if !isStringContent(mustMarshal(t, out[0].Content)) {
		t.Fatalf("plain user content changed: %v", out[0].Content)
	}
	if hasThinkingBlock(mustMarshal(t, out[4].Content)) {
		t.Fatalf("user tool_result turn must not gain thinking")
	}
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestPassbackRequiredDetection pins the probe's predicate: only a definitive
// 400 whose body names the passback requirement triggers it.
func TestPassbackRequiredDetection(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"anthropic wording", &statusError{status: 400, body: deepseekPassbackBody}, true},
		{"openai wording", &statusError{status: 400, body: `{"error":{"message":"The reasoning_content in the thinking mode must be passed back to the API."}}`}, true},
		{"other 400", &statusError{status: 400, body: `{"error":{"message":"unknown variant"}}`}, false},
		{"non-400", &statusError{status: 401, body: deepseekPassbackBody}, false},
		{"retryable", &retryableError{status: 429, body: deepseekPassbackBody}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := passbackRequired(tt.err); got != tt.want {
				t.Fatalf("passbackRequired = %v, want %v", got, tt.want)
			}
		})
	}
}

package entry

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xenith/panda/internal/config"
	"github.com/xenith/panda/internal/ledger"
)

// startModelServer spins up a fake Anthropic-compatible endpoint whose text
// content is the JSON-encoded `text` string.
func startModelServer(t *testing.T, text string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		// The response text must be a JSON string; encode it as the content.
		resp := map[string]any{
			"content": []map[string]string{{"type": "text", "text": text}},
		}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk-test", Model: "deepseek-chat"})
}

func TestClassifyTask(t *testing.T) {
	task := TaskSpec{
		Title:       "改导航栏",
		ContextType: "file",
		Requires:    Requires{Abilities: []string{"code:modify"}},
		Complexity:  0.4,
		Risk:        "medium",
	}
	payload, _ := json.Marshal(map[string]any{"kind": "task", "task": task})
	c := startModelServer(t, string(payload))

	out, err := Classify(context.Background(), c, nil, "", "把导航栏改成响应式")
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if out.Kind != KindTask || out.Task == nil || out.Task.Title != "改导航栏" {
		t.Fatalf("out = %+v", out)
	}
}

func TestClassifyToolCall(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"kind": "tool_call",
		"tool": map[string]any{"tool": "weather.get", "arguments": map[string]any{"location": "济南"}},
	})
	c := startModelServer(t, string(payload))

	out, err := Classify(context.Background(), c, nil, "", "济南天气")
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if out.Kind != KindToolCall || out.Tool == nil || out.Tool.Tool != "weather.get" {
		t.Fatalf("out = %+v", out)
	}
}

func TestClassifyAnswer(t *testing.T) {
	c := startModelServer(t, "今天天气不错，晴。")
	out, err := Classify(context.Background(), c, nil, "", "今天天气怎么样")
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if out.Kind != KindAnswer || out.Answer == "" {
		t.Fatalf("out = %+v", out)
	}
}

func TestParseOutputFencedJSON(t *testing.T) {
	raw := "```json\n{\"kind\":\"task\",\"task\":{\"title\":\"x\",\"context_type\":\"file\",\"requires\":{\"abilities\":[\"a\"]}}}\n```"
	out, err := ParseOutput(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Kind != KindTask || out.Task.Title != "x" {
		t.Fatalf("out = %+v", out)
	}
}

func TestParseOutputProseIsAnswer(t *testing.T) {
	out, err := ParseOutput("这是一个普通的回答，不是 JSON。")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Kind != KindAnswer {
		t.Fatalf("prose should be answer, got %s", out.Kind)
	}
}

func TestParseOutputProseThenJSON(t *testing.T) {
	// The model may prefix a structured directive with a sentence. The prose is
	// discarded and the JSON object is authoritative.
	raw := "这是一个需要运行文件操作的请求，应该路由为 task。\n\n```json\n" +
		`{"kind":"task","task":{"title":"跑 ESLint","context_type":"file","requires":{"abilities":["lint"]}}}` +
		"\n```"
	out, err := ParseOutput(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Kind != KindTask || out.Task == nil || out.Task.Title != "跑 ESLint" {
		t.Fatalf("out = %+v", out)
	}
}

func TestParseOutputJSONWithTrailingProse(t *testing.T) {
	raw := `{"kind":"tool_call","tool":{"tool":"weather.get","arguments":{"location":"济南"}}} 以上是结果。`
	out, err := ParseOutput(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Kind != KindToolCall || out.Tool == nil || out.Tool.Tool != "weather.get" {
		t.Fatalf("out = %+v", out)
	}
}

func TestParseOutputIllustrativeJSONIsAnswer(t *testing.T) {
	// A long prose answer that embeds an illustrative JSON example is not a
	// directive; it must fall back to answer rather than executing the JSON as a
	// real task.
	raw := "如果你想要发起一个任务，可以参考下面这个示例格式。" +
		"任务里需要写清楚标题、上下文类型、所需能力和作用范围，缺少任何一项都会校验失败。" +
		"这里的示例仅供参考，请根据你的实际需求调整字段：" +
		`{"kind":"task","task":{"title":"示例任务","context_type":"file","requires":{"abilities":["lint"]}}}`
	out, err := ParseOutput(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Kind != KindAnswer {
		t.Fatalf("illustrative JSON should fall back to answer, got %s", out.Kind)
	}
}

func TestExtractJSONObject(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", `{"a":1}`, `{"a":1}`},
		{"prose prefix", `here: {"a":1}`, `{"a":1}`},
		{"nested", `x {"a":{"b":[1,{"c":2}]}} y`, `{"a":{"b":[1,{"c":2}]}}`},
		{"brace in string", `x {"a":"}"}`, `{"a":"}"}`},
		{"escaped quote", `x {"a":"\""}`, `{"a":"\""}`},
		{"no object", `no json here`, ""},
		{"array only", `[1,2,3]`, ""},
	}
	for _, tc := range cases {
		if got := extractJSONObject(tc.in); got != tc.want {
			t.Fatalf("%s: extractJSONObject(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestParseOutputInvalidTaskFails(t *testing.T) {
	// Missing title + abilities → validation error, not a silent answer.
	raw := `{"kind":"task","task":{"context_type":"file"}}`
	if _, err := ParseOutput(raw); err == nil {
		t.Fatalf("expected validation error")
	}
}

func TestValidateTaskSpec(t *testing.T) {
	good := &TaskSpec{Title: "t", ContextType: "file", Requires: Requires{Abilities: []string{"a"}}}
	if err := ValidateTaskSpec(good); err != nil {
		t.Fatalf("good task rejected: %v", err)
	}

	cases := []struct {
		name string
		mut  func(*TaskSpec)
	}{
		{"no title", func(s *TaskSpec) { s.Title = "" }},
		{"no context", func(s *TaskSpec) { s.ContextType = "" }},
		{"bad context", func(s *TaskSpec) { s.ContextType = "video" }},
		{"no abilities", func(s *TaskSpec) { s.Requires.Abilities = nil }},
		{"complexity high", func(s *TaskSpec) { s.Complexity = 1.5 }},
		{"bad risk", func(s *TaskSpec) { s.Risk = "nuclear" }},
	}
	for _, tc := range cases {
		s := &TaskSpec{Title: "t", ContextType: "file", Requires: Requires{Abilities: []string{"a"}}}
		tc.mut(s)
		if err := ValidateTaskSpec(s); err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
	}
}

func TestBuildPromptIncludesDevices(t *testing.T) {
	devs := []ledger.Node{
		{Name: "macbook-m1", Chip: "Apple M1", Native: []ledger.NativeAbility{{ID: "build:macos"}}},
	}
	p := BuildPrompt(PromptOptions{Devices: devs})
	if p == "" {
		t.Fatalf("empty prompt")
	}
	if !strings.Contains(p, "macbook-m1") || !strings.Contains(p, "build:macos") {
		t.Fatalf("prompt missing device summary:\n%s", p)
	}
}

func TestClassifyTurnsSendsHistory(t *testing.T) {
	var gotMessages int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		gotMessages = len(req.Messages)
		w.Header().Set("content-type", "application/json")
		resp := map[string]any{"content": []map[string]string{{"type": "text", "text": "plain answer"}}}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk-test", Model: "m"})

	turns := []Turn{
		{Role: "user", Content: "记住我偏好暗色主题"},
		{Role: "assistant", Content: "tool_call: {\"tool\":\"memory.add\"}"},
		{Role: "user", Content: "工具结果：已记住"},
	}
	if _, err := ClassifyTurns(context.Background(), c, nil, "", turns); err != nil {
		t.Fatalf("classify turns: %v", err)
	}
	if gotMessages != 3 {
		t.Errorf("messages sent = %d, want 3", gotMessages)
	}
}

func TestClassifyToolUse(t *testing.T) {
	// A native tool_use response must become a KindToolCall output with the
	// tool_use id preserved so the caller can reply with a tool_result.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		resp := map[string]any{
			"content": []map[string]any{
				{"type": "tool_use", "id": "toolu_1", "name": "memory.add", "input": map[string]any{"target": "user", "entry": "偏好暗色主题"}},
			},
		}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk-test", Model: "m"})

	reg := NewRegistry()
	reg.Register(Tool{Name: "memory.add", Description: "remember", Schema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, args map[string]any) (string, error) { return "ok", nil }})

	out, err := ClassifyTurnsWithTools(context.Background(), c, nil, "", []Turn{{Role: "user", Content: "记住我偏好暗色主题"}}, reg)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if out.Kind != KindToolCall || out.Tool == nil || out.Tool.Tool != "memory.add" || out.Tool.ID != "toolu_1" {
		t.Fatalf("out = %+v", out)
	}
	if out.Tool.Arguments["entry"] != "偏好暗色主题" {
		t.Fatalf("args = %+v", out.Tool.Arguments)
	}
}

func TestCompleteTurnsWithToolsSendsTools(t *testing.T) {
	var gotTools int
	var gotToolChoice string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tools      []map[string]any `json:"tools"`
			ToolChoice string           `json:"tool_choice"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		gotTools = len(req.Tools)
		gotToolChoice = req.ToolChoice
		w.Header().Set("content-type", "application/json")
		resp := map[string]any{"content": []map[string]string{{"type": "text", "text": "plain answer"}}}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk-test", Model: "m"})

	tools := []ToolSpec{{Name: "memory.add", Description: "remember", InputSchema: map[string]any{"type": "object"}}}
	if _, err := c.CompleteTurnsWithTools(context.Background(), "sys", []Turn{{Role: "user", Content: "hi"}}, tools); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if gotTools != 1 {
		t.Errorf("tools sent = %d, want 1", gotTools)
	}
	if gotToolChoice != "auto" {
		t.Errorf("tool_choice = %q, want auto", gotToolChoice)
	}
}

func TestMessageBlocksMarshal(t *testing.T) {
	// A turn carrying structured blocks must marshal content as an array (not a
	// string), which is what tool_use/tool_result require.
	m := message{Role: "user", Content: []ContentBlock{{Type: "tool_result", ToolUseID: "toolu_1", Content: "ok"}}}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "tool_result") || !strings.Contains(s, "toolu_1") {
		t.Fatalf("marshal = %s", s)
	}
}

// TestCompleteRetriesServerError verifies a retryable 5xx is retried up to the
// budget and succeeds once the provider recovers (P1-3/P2-19 retry path).
func TestCompleteRetriesServerError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("content-type", "application/json")
		resp := map[string]any{"content": []map[string]string{{"type": "text", "text": "ok"}}}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)

	c := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk", Model: "m"})
	c.maxRetry = 2
	c.retryBase = time.Millisecond

	got, err := c.Complete(context.Background(), "sys", "hi")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got != "ok" {
		t.Fatalf("got %q, want ok", got)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3 (2 failures + success)", calls.Load())
	}
}

// TestCompleteRetriesTransientTransport verifies a weak-network drop (the server
// accepts then closes before responding) is treated as transient and retried
// rather than silently dropped (P1-3).
func TestCompleteRetriesTransientTransport(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	var accepts atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			_ = conn.Close() // reset before any response: a weak-network drop
		}
	}()

	c := NewClient(config.ModelConfig{BaseURL: "http://" + ln.Addr().String(), APIKey: "sk", Model: "m"})
	c.maxRetry = 1
	c.retryBase = time.Millisecond

	if _, err := c.Complete(context.Background(), "sys", "hi"); err == nil {
		t.Fatal("expected error after transport failures")
	}
	if got := accepts.Load(); got != 2 {
		t.Fatalf("accepts = %d, want 2 (initial + 1 retry)", got)
	}
}

// TestCompleteNoRetryClientError verifies a non-retryable 4xx is not retried:
// the client error is definitive, so retrying would waste a call (P1-3).
func TestCompleteNoRetryClientError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"bad"}}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk", Model: "m"})
	c.maxRetry = 2
	c.retryBase = time.Millisecond

	if _, err := c.Complete(context.Background(), "sys", "hi"); err == nil {
		t.Fatal("expected error for 400")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 (client error is not retried)", calls.Load())
	}
}

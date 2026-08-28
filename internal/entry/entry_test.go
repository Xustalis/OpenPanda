package entry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
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
	client, err := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk-test", Model: "deepseek-chat"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
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

// TestParseOutputPreservesComplexity verifies the C-02 MVP chain: a model-emitted
// complexity survives ParseOutput and passes ValidateTaskSpec with the value
// intact (the core persists it; here we assert the entry side records it).
func TestParseOutputPreservesComplexity(t *testing.T) {
	raw := `{"kind":"task","task":{"title":"跑测试","context_type":"file","requires":{"abilities":["lint"]},"complexity":0.7}}`
	out, err := ParseOutput(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Kind != KindTask || out.Task == nil {
		t.Fatalf("out = %+v", out)
	}
	if out.Task.Complexity != 0.7 {
		t.Fatalf("complexity = %v, want 0.7", out.Task.Complexity)
	}
	if err := ValidateTaskSpec(out.Task); err != nil {
		t.Fatalf("validate: %v", err)
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

// TestParseOutputScalarIsAnswer guards the terse-answer regression: a bare
// JSON scalar ("2") is valid JSON that fails to unmarshal into the envelope
// struct; it used to surface as a validation error instead of falling back
// to an answer (seen live: "1+1等于几" → "2" → unmarshal type error).
func TestParseOutputScalarIsAnswer(t *testing.T) {
	for _, raw := range []string{"2", "true", `"yes"`, "[1, 2]"} {
		out, err := ParseOutput(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		if out.Kind != KindAnswer || out.Answer != raw {
			t.Fatalf("scalar %q should be answer verbatim, got kind=%s answer=%q", raw, out.Kind, out.Answer)
		}
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

// TestParseOutputAnswerEnvelope guards the JSON-leak fix: a model that wraps its
// reply as {"kind":"answer","answer":"…"} must be unwrapped to clean prose
// rather than have the raw JSON envelope printed verbatim to the user.
func TestParseOutputAnswerEnvelope(t *testing.T) {
	raw := `{"kind":"answer","answer":"PPO 是一种策略梯度强化学习算法。"}`
	out, err := ParseOutput(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Kind != KindAnswer {
		t.Fatalf("answer envelope should be KindAnswer, got %s", out.Kind)
	}
	if out.Answer != "PPO 是一种策略梯度强化学习算法。" {
		t.Fatalf("answer not unwrapped, got %q", out.Answer)
	}
}

// TestParseOutputEmptyAnswerEnvelope verifies a contentless answer envelope
// falls back to prose handling instead of rendering a hollow "{...}".
func TestParseOutputEmptyAnswerEnvelope(t *testing.T) {
	out, err := ParseOutput(`{"kind":"answer","answer":""}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Kind != KindAnswer {
		t.Fatalf("empty answer envelope should degrade to KindAnswer, got %s", out.Kind)
	}
}

func TestParseOutputReasoningPreamble(t *testing.T) {
	// A reasoning model may emit a chain-of-thought preamble before committing to
	// the directive JSON. It is not an illustrative example, so the JSON must be
	// accepted and routed, not degraded to an answer.
	raw := "这个任务需要在远程节点上运行 uname -a 查看系统信息。根据设备列表，" +
		"worker-1 提供 sys:info 能力，最接近这个需求。我会将任务调度到该节点执行。" +
		`{"kind":"task","task":{"title":"查看系统信息","context_type":"command","requires":{"abilities":["sys:info"]}}}`
	out, err := ParseOutput(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Kind != KindTask || out.Task == nil || out.Task.Title != "查看系统信息" {
		t.Fatalf("reasoning preamble should route to task, got %+v", out)
	}
}

func TestParseOutputFractionalRAM(t *testing.T) {
	// A resource hint may legitimately be fractional (e.g. 0.1 GB RAM for a
	// small task); it must parse, not fail on an int field.
	raw := `{"kind":"task","task":{"title":"查系统","context_type":"command","requires":{"abilities":["sys:info"]},"resource_profile":{"cpu":1,"ram_gb":0.1,"gpu_vram_gb":0.5,"duration_hint":"short"}}}`
	out, err := ParseOutput(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Kind != KindTask || out.Task == nil {
		t.Fatalf("out = %+v", out)
	}
	if out.Task.Resources.RAMGB != 0.1 || out.Task.Resources.GPUVRAMGB != 0.5 {
		t.Fatalf("resources = %+v", out.Task.Resources)
	}
}

func TestParseOutputTypeErrorSurfaces(t *testing.T) {
	// Valid JSON with a schema mismatch (string where a number belongs) is a
	// model error that must surface, not silently degrade to an answer.
	raw := `{"kind":"task","task":{"title":"x","context_type":"file","requires":{"abilities":["a"]},"complexity":"high"}}`
	if _, err := ParseOutput(raw); err == nil {
		t.Fatalf("expected type error to surface")
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
	c, err := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk-test", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	turns := []Turn{
		{Role: "user", Content: "记住我偏好暗色主题"},
		{Role: "assistant", Content: "tool_call: {\"tool\":\"memory_add\"}"},
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
				{"type": "tool_use", "id": "toolu_1", "name": "memory_add", "input": map[string]any{"target": "user", "entry": "偏好暗色主题"}},
			},
		}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk-test", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	reg := NewRegistry()
	reg.Register(Tool{Name: "memory_add", Description: "remember", Schema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, args map[string]any) (string, error) { return "ok", nil }})

	out, err := ClassifyTurnsWithTools(context.Background(), c, nil, "", []Turn{{Role: "user", Content: "记住我偏好暗色主题"}}, reg)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if out.Kind != KindToolCall || out.Tool == nil || out.Tool.Tool != "memory_add" || out.Tool.ID != "toolu_1" {
		t.Fatalf("out = %+v", out)
	}
	if out.Tool.Arguments["entry"] != "偏好暗色主题" {
		t.Fatalf("args = %+v", out.Tool.Arguments)
	}
}

// TestClassifyToolUseDropsExtra verifies that text emitted alongside a tool
// call and any tool_use after the first are preserved in Output.Note instead
// of being silently dropped (P2-21).
func TestClassifyToolUseDropsExtra(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		resp := map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "先读记忆再合并"},
				{"type": "tool_use", "id": "toolu_1", "name": "memory_read", "input": map[string]any{"target": "user"}},
				{"type": "tool_use", "id": "toolu_2", "name": "memory_add", "input": map[string]any{"target": "memory", "entry": "x"}},
			},
		}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk-test", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	reg := NewRegistry()

	out, err := ClassifyTurnsWithTools(context.Background(), c, nil, "", []Turn{{Role: "user", Content: "合并记忆"}}, reg)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if out.Kind != KindToolCall || out.Tool == nil || out.Tool.Tool != "memory_read" || out.Tool.ID != "toolu_1" {
		t.Fatalf("out = %+v", out)
	}
	if !strings.Contains(out.Note, "先读记忆再合并") {
		t.Fatalf("note missing accompanying text: %q", out.Note)
	}
	if !strings.Contains(out.Note, "memory_add") {
		t.Fatalf("note missing extra tool_use: %q", out.Note)
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
	c, err := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk-test", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	tools := []ToolSpec{{Name: "memory_add", Description: "remember", InputSchema: map[string]any{"type": "object"}}}
	if _, err := c.CompleteTurnsWithTools(context.Background(), "sys", []Turn{{Role: "user", Content: "hi"}}, tools); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if gotTools != 1 {
		t.Errorf("tools sent = %d, want 1", gotTools)
	}
	if gotToolChoice != "" {
		t.Errorf("tool_choice = %q, want empty (omitted; defaults to auto)", gotToolChoice)
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

	c, err := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
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

	c, err := NewClient(config.ModelConfig{BaseURL: "http://" + ln.Addr().String(), APIKey: "sk", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
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

	c, err := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	c.maxRetry = 2
	c.retryBase = time.Millisecond

	if _, err := c.Complete(context.Background(), "sys", "hi"); err == nil {
		t.Fatal("expected error for 400")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 (client error is not retried)", calls.Load())
	}
}

// TestWrapAPIErrorActionableMessages verifies the error→guidance mapping: a
// rejected key, a wrong endpoint, a rate limit, and an outage each produce a
// distinct user-facing message that names the knob to fix, instead of the
// generic "try again later".
func TestWrapAPIErrorActionableMessages(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		wantSub string
	}{
		{"unauthorized", &statusError{status: http.StatusUnauthorized, body: "bad key"}, "api_key"},
		{"forbidden", &statusError{status: http.StatusForbidden, body: "denied"}, "api_key"},
		{"not found", &statusError{status: http.StatusNotFound, body: "no route"}, "base_url"},
		{"rate limited after retries", &retryableError{status: http.StatusTooManyRequests, body: "slow down"}, "限流"},
		{"server down after retries", &retryableError{status: http.StatusBadGateway, body: "boom"}, "暂时不可用"},
		{"unreachable", &transientError{err: errors.New("connection refused")}, "无法连接"},
		{"no key", ErrNoKey, "API key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrapped := WrapAPIError(tc.err)
			var ce *ClassifyError
			if !errors.As(wrapped, &ce) {
				t.Fatalf("WrapAPIError(%v) = %T, want *ClassifyError", tc.err, wrapped)
			}
			if !strings.Contains(ce.UserMsg, tc.wantSub) {
				t.Fatalf("UserMsg = %q, want substring %q", ce.UserMsg, tc.wantSub)
			}
		})
	}
}

// TestClassifySurfacesActionableError runs the full path — a 401 from the
// provider must reach the caller as the "fix your api_key" message, not the
// generic fallback.
func TestClassifySurfacesActionableError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk-wrong", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = Classify(context.Background(), c, nil, "", "hi")
	if err == nil {
		t.Fatal("expected error for 401")
	}
	var ce *ClassifyError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %T (%v), want *ClassifyError", err, err)
	}
	if !strings.Contains(ce.Error(), "api_key") {
		t.Fatalf("message = %q, want api_key guidance", ce.Error())
	}
}

// TestNewClientRejectsPlainHTTP verifies the endpoint guard (M2): a remote
// http:// base_url would send the API key cleartext and must be rejected at
// construction, while loopback http stays allowed for a local dev model —
// the same semantics the commander applies to adapter endpoints (D7).
func TestNewClientRejectsPlainHTTP(t *testing.T) {
	if _, err := NewClient(config.ModelConfig{BaseURL: "http://api.deepseek.com/anthropic", APIKey: "sk"}); err == nil {
		t.Fatal("expected error for a non-https remote base_url")
	}
	for _, base := range []string{"http://127.0.0.1:8080", "http://localhost:11434", "https://api.deepseek.com/anthropic"} {
		if _, err := NewClient(config.ModelConfig{BaseURL: base, APIKey: "sk"}); err != nil {
			t.Fatalf("base_url %q should be accepted: %v", base, err)
		}
	}
}

// ---- streaming delta guard ----

// startStreamServer spins up a fake OpenAI-compatible SSE endpoint emitting
// the given text chunks as content deltas, then [DONE].
func startStreamServer(t *testing.T, chunks []string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, c := range chunks {
			b, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{{"delta": map[string]string{"content": c}}},
			})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(config.ModelConfig{APIType: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}

// TestDeltaGuardSuppressesStructuredOutput verifies the guard's core rule: a
// task spec streams as bare JSON, and raw JSON must never reach the user —
// deltas are withheld while still not counting as delivered (so a mid-stream
// transport drop stays retryable).
func TestDeltaGuardSuppressesStructuredOutput(t *testing.T) {
	var got []string
	g := newDeltaGuard(func(s string) { got = append(got, s) }, nil)
	g.on(`{"kind":`)
	g.on(`"task":{}}`)
	if len(got) != 0 {
		t.Fatalf("forwarded %q, want structured output fully suppressed", got)
	}
	if g.delivered {
		t.Fatal("suppressed deltas must not count as delivered (stream stays retryable)")
	}
}

// TestDeltaGuardStreamsProseWithBufferedPrefix verifies prose streams live,
// with the whitespace buffered during the decision flushed ahead of it.
func TestDeltaGuardStreamsProseWithBufferedPrefix(t *testing.T) {
	var got []string
	g := newDeltaGuard(func(s string) { got = append(got, s) }, nil)
	g.on("\n\n") // whitespace-only: buffered, nothing forwarded yet
	g.on("你好")   // decision: prose; the buffered prefix flushes with it
	g.on("，世界")
	if strings.Join(got, "") != "\n\n你好，世界" {
		t.Fatalf("forwarded %q, want the buffered prefix plus live deltas", got)
	}
	if !g.delivered {
		t.Fatal("prose must count as delivered")
	}
}

// TestDeltaGuardSuppressesFencedJSON verifies the ```json fence form of a
// structured output is suppressed too.
func TestDeltaGuardSuppressesFencedJSON(t *testing.T) {
	var got []string
	g := newDeltaGuard(func(s string) { got = append(got, s) }, nil)
	g.on("```json\n")
	g.on(`{"kind":"task"}`)
	g.on("\n```")
	if len(got) != 0 || g.delivered {
		t.Fatalf("forwarded %q delivered=%v, want fenced JSON fully suppressed", got, g.delivered)
	}
}

// TestDeltaGuardSuppressesJSONAfterProseLeadIn covers the reasoning-preamble
// shape seen live: the model thinks out loud in prose, then emits the task
// JSON on its own line. The prose streams; the JSON must not (the parsed
// Output renders it). The structured start may split across deltas.
func TestDeltaGuardSuppressesJSONAfterProseLeadIn(t *testing.T) {
	var got []string
	g := newDeltaGuard(func(s string) { got = append(got, s) }, nil)
	g.on("需要查一下状态。\n")
	g.on("\n{") // the newline pair + '{' split right at the boundary
	g.on(`"kind":"task","task":{}}`)
	joined := strings.Join(got, "")
	if joined != "需要查一下状态。\n\n" {
		t.Fatalf("forwarded %q, want the lead-in prose only", joined)
	}
	if g.delivered != true {
		t.Fatal("prose lead-in counts as delivered once forwarded")
	}
	if !g.structured {
		t.Fatal("guard must latch structured once the JSON line starts")
	}
}

// TestDeltaGuardFlushesHeldBackTail verifies prose ending in bytes that were
// withheld as a possible structured prefix (but never became one) reaches
// the user at end-of-stream.
func TestDeltaGuardFlushesHeldBackTail(t *testing.T) {
	var got []string
	g := newDeltaGuard(func(s string) { got = append(got, s) }, nil)
	g.on("答案是 42")
	g.on("\n") // trailing newline held back pending a possible directive
	if strings.Join(got, "") != "答案是 42" {
		t.Fatalf("premature forward %q", got)
	}
	g.flush()
	if strings.Join(got, "") != "答案是 42\n" {
		t.Fatalf("flush forwarded %q, want the held-back tail", got)
	}
}

// TestDeltaGuardInlineBracesStillStream pins the counter-case: a '{' on the
// same line as prose (inline code/braces in a normal answer) must keep
// streaming — only a line-initial brace starts suppression.
func TestDeltaGuardInlineBracesStillStream(t *testing.T) {
	var got []string
	g := newDeltaGuard(func(s string) { got = append(got, s) }, nil)
	g.on("格式是 {host} 占位符")
	g.on("，注意 ``code`` 标记。")
	g.flush()
	if strings.Join(got, "") != "格式是 {host} 占位符，注意 ``code`` 标记。" {
		t.Fatalf("inline braces must stream verbatim, got %q", got)
	}
}

// TestStreamCapturesOpenAIReasoning verifies Phase 1.3 for the OpenAI path:
// chain-of-thought on delta.reasoning_content is surfaced live to the reasoning
// sink and kept entirely out of the answer text (D14). The answer content still
// streams to onDelta and lands in Response.Text.
func TestStreamCapturesOpenAIReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		emit := func(delta map[string]string) {
			b, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{{"delta": delta}},
			})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
		emit(map[string]string{"reasoning_content": "let me "})
		emit(map[string]string{"reasoning_content": "think…"})
		emit(map[string]string{"content": "Hello"})
		emit(map[string]string{"content": " world"})
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(config.ModelConfig{APIType: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	var answer, reasoning []string
	resp, err := c.StreamTurnsWithTools(context.Background(), "", []Turn{{Role: "user", Content: "hi"}}, nil,
		func(s string) { answer = append(answer, s) },
		func(s string) { reasoning = append(reasoning, s) })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if got := strings.Join(reasoning, ""); got != "let me think…" {
		t.Fatalf("reasoning sink = %q, want the chain-of-thought delivered live", got)
	}
	if got := strings.Join(answer, ""); got != "Hello world" {
		t.Fatalf("answer sink = %q, want only the answer content", got)
	}
	if resp.Text != "Hello world" {
		t.Fatalf("Response.Text = %q, want the answer with no reasoning folded in", resp.Text)
	}
	if resp.Reasoning != "let me think…" {
		t.Fatalf("Response.Reasoning = %q, want the captured chain-of-thought", resp.Reasoning)
	}
}

// TestStreamCapturesAnthropicReasoning verifies Phase 1.3 for the Anthropic
// path: a thinking block streams thinking_delta events to the reasoning sink
// and is kept out of the answer text (D14).
func TestStreamCapturesAnthropicReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		emit := func(ev map[string]any) {
			b, _ := json.Marshal(ev)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
		// A thinking block at index 0, then an answer text block at index 1.
		emit(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "thinking"}})
		emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "thinking_delta", "thinking": "weigh "}})
		emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "thinking_delta", "thinking": "options"}})
		emit(map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "text"}})
		emit(map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "text_delta", "text": "Answer."}})
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	var answer, reasoning []string
	resp, err := c.StreamTurnsWithTools(context.Background(), "", []Turn{{Role: "user", Content: "hi"}}, nil,
		func(s string) { answer = append(answer, s) },
		func(s string) { reasoning = append(reasoning, s) })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if got := strings.Join(reasoning, ""); got != "weigh options" {
		t.Fatalf("reasoning sink = %q, want the thinking block delivered live", got)
	}
	if got := strings.Join(answer, ""); got != "Answer." {
		t.Fatalf("answer sink = %q, want only the text block", got)
	}
	if resp.Text != "Answer." {
		t.Fatalf("Response.Text = %q, want no thinking folded in", resp.Text)
	}
	if resp.Reasoning != "weigh options" {
		t.Fatalf("Response.Reasoning = %q, want the captured thinking", resp.Reasoning)
	}
}

// TestStreamSuppressesTaskJSONEndToEnd runs the guard through the real
// streaming path: a task-JSON response streams nothing to onDelta while the
// full text still arrives in the Response.
func TestStreamSuppressesTaskJSONEndToEnd(t *testing.T) {
	taskJSON := `{"kind":"task","task":{"title":"跑测试","context_type":"command","requires":{"abilities":["lint"]}}}`
	c := startStreamServer(t, []string{`{"kind":"task",`, `"task":{"title":"跑测试",`, `"context_type":"command","requires":{"abilities":["lint"]}}}`})

	var got []string
	resp, err := c.StreamTurnsWithTools(context.Background(), "", []Turn{{Role: "user", Content: "跑下测试"}}, nil, func(s string) { got = append(got, s) }, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("onDelta got %q, want the task JSON suppressed", got)
	}
	if resp.Text != taskJSON {
		t.Fatalf("text = %q, want the full JSON in the response", resp.Text)
	}
}

// TestStreamRetriesThenSuppressesStructured verifies the retry and the guard
// compose: a 429 first attempt is retried (nothing was delivered — the JSON
// was suppressed), and the successful replay still streams nothing raw.
func TestStreamRetriesThenSuppressesStructured(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("rate limited"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, c := range []string{`{"kind":`, `"task":{}}`} {
			b, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{{"delta": map[string]string{"content": c}}},
			})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(config.ModelConfig{APIType: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	c.maxRetry = 2
	c.retryBase = time.Millisecond

	var got []string
	resp, err := c.StreamTurnsWithTools(context.Background(), "", []Turn{{Role: "user", Content: "hi"}}, nil, func(s string) { got = append(got, s) }, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 (429 then success)", calls.Load())
	}
	if len(got) != 0 {
		t.Fatalf("onDelta got %q, want structured output suppressed", got)
	}
	if resp.Text != `{"kind":"task":{}}` {
		t.Fatalf("text = %q", resp.Text)
	}
}

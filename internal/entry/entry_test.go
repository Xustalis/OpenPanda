package entry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

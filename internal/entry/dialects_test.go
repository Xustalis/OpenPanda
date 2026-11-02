// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// TestResponsesComplete pins the non-streaming Responses path end to end:
// endpoint derivation, the instructions/input/tool request shapes, the output
// item reduction, and usage accounting.
func TestResponsesComplete(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path = %q, want /v1/responses", r.URL.Path)
		}
		if got := r.Header.Get("authorization"); got != "Bearer sk-test" {
			t.Errorf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
			"status":"completed",
			"output":[
				{"type":"reasoning","summary":[{"type":"summary_text","text":"why"}]},
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]},
				{"type":"function_call","call_id":"call_1","name":"web_search","arguments":"{\"q\":\"x\"}"}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer srv.Close()

	c, err := NewClient(config.ModelConfig{
		APIType: config.APITypeResponses, BaseURL: srv.URL + "/v1",
		APIKey: "sk-test", Model: "gpt-6.1-sol", MaxTokens: 128,
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	resp, err := c.CompleteTurnsWithTools(context.Background(), "sys",
		[]Turn{{Role: "user", Content: "hi"}},
		[]ToolSpec{{Name: "web_search", Description: "search", InputSchema: map[string]any{"type": "object"}}})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Text != "hello" || resp.Reasoning != "why" || resp.Truncated {
		t.Fatalf("response = %+v", resp)
	}
	if len(resp.ToolUses) != 1 || resp.ToolUses[0].ID != "call_1" ||
		resp.ToolUses[0].Name != "web_search" || resp.ToolUses[0].Input["q"] != "x" {
		t.Fatalf("tool uses = %+v", resp.ToolUses)
	}
	if u := c.Usage(); u.InputTokens != 10 || u.OutputTokens != 5 {
		t.Fatalf("usage = %+v", u)
	}

	if body["instructions"] != "sys" {
		t.Errorf("instructions = %v", body["instructions"])
	}
	if body["max_output_tokens"] != float64(128) || body["store"] != false {
		t.Errorf("max_output_tokens/store = %v/%v", body["max_output_tokens"], body["store"])
	}
	input, _ := body["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("input = %#v", body["input"])
	}
	msg, _ := input[0].(map[string]any)
	if msg["type"] != "message" || msg["role"] != "user" {
		t.Errorf("input message = %#v", msg)
	}
	parts, _ := msg["content"].([]any)
	part, _ := parts[0].(map[string]any)
	if part["type"] != "input_text" || part["text"] != "hi" {
		t.Errorf("input content = %#v", parts)
	}
	tools, _ := body["tools"].([]any)
	tool, _ := tools[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "web_search" {
		t.Errorf("tools = %#v", tools)
	}
	if _, nested := tool["function"]; nested {
		t.Error("Responses tools must be flat, not chat-completions nested")
	}
}

// TestResponsesStream pins the SSE event reduction: text and reasoning
// deltas stream live, function-call arguments finalize from the done
// envelope, and the completion envelope carries usage.
func TestResponsesStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path = %q, want /v1/responses", r.URL.Path)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"response.output_text.delta","delta":"he"}`,
			`data: {"type":"response.output_text.delta","delta":"llo"}`,
			`data: {"type":"response.reasoning_summary_text.delta","delta":"why"}`,
			`data: {"type":"response.output_item.added","item_id":"fc_1","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_9","name":"web_search"}}`,
			`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"{\"q\":\"x\"}"}`,
			`data: {"type":"response.output_item.done","item_id":"fc_1","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_9","name":"web_search","arguments":"{\"q\":\"x\"}"}}`,
			`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":10,"output_tokens":5}}}`,
			``,
		}, "\n\n")))
	}))
	defer srv.Close()

	c, err := NewClient(config.ModelConfig{
		APIType: config.APITypeResponses, BaseURL: srv.URL + "/v1",
		APIKey: "sk-test", Model: "gpt-6.1-sol", MaxTokens: 128,
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	var deltas, reasoning []string
	resp, err := c.StreamTurnsWithTools(context.Background(), "sys",
		[]Turn{{Role: "user", Content: "hi"}}, nil,
		func(s string) { deltas = append(deltas, s) },
		func(s string) { reasoning = append(reasoning, s) })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if strings.Join(deltas, "") != "hello" || strings.Join(reasoning, "") != "why" {
		t.Fatalf("deltas = %q reasoning = %q", deltas, reasoning)
	}
	if resp.Text != "hello" || resp.Reasoning != "why" {
		t.Fatalf("response = %+v", resp)
	}
	if len(resp.ToolUses) != 1 || resp.ToolUses[0].ID != "call_9" ||
		resp.ToolUses[0].Name != "web_search" || resp.ToolUses[0].Input["q"] != "x" {
		t.Fatalf("tool uses = %+v", resp.ToolUses)
	}
	if u := c.Usage(); u.InputTokens != 10 || u.OutputTokens != 5 {
		t.Fatalf("usage = %+v", u)
	}
}

// TestGoogleComplete pins the non-streaming Gemini path: the model-and-method
// URL, x-goog-api-key auth, contents/systemInstruction/generationConfig
// shapes, thought parts kept out of the answer, and usage accounting.
func TestGoogleComplete(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-4-argon:generateContent" {
			t.Errorf("path = %q, want the generateContent endpoint", r.URL.Path)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "sk-test" {
			t.Errorf("x-goog-api-key = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates":[{
				"content":{"role":"model","parts":[
					{"text":"plan","thought":true},
					{"text":"hello"},
					{"functionCall":{"name":"web_search","args":{"q":"x"}}}
				]},
				"finishReason":"STOP"
			}],
			"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}
		}`))
	}))
	defer srv.Close()

	c, err := NewClient(config.ModelConfig{
		APIType: config.APITypeGoogle, BaseURL: srv.URL + "/v1beta",
		APIKey: "sk-test", Model: "gemini-4-argon", MaxTokens: 128,
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	resp, err := c.CompleteTurnsWithTools(context.Background(), "sys",
		[]Turn{{Role: "user", Content: "hi"}},
		[]ToolSpec{{Name: "web_search", Description: "search", InputSchema: map[string]any{"type": "object"}}})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Text != "hello" || resp.Reasoning != "plan" || resp.Truncated {
		t.Fatalf("response = %+v", resp)
	}
	if len(resp.ToolUses) != 1 || resp.ToolUses[0].Name != "web_search" || resp.ToolUses[0].Input["q"] != "x" {
		t.Fatalf("tool uses = %+v", resp.ToolUses)
	}
	if u := c.Usage(); u.InputTokens != 10 || u.OutputTokens != 5 {
		t.Fatalf("usage = %+v", u)
	}

	contents, _ := body["contents"].([]any)
	first, _ := contents[0].(map[string]any)
	if first["role"] != "user" {
		t.Errorf("contents[0] role = %v", first["role"])
	}
	sys, _ := body["systemInstruction"].(map[string]any)
	sysParts, _ := sys["parts"].([]any)
	sysPart, _ := sysParts[0].(map[string]any)
	if sysPart["text"] != "sys" {
		t.Errorf("systemInstruction = %#v", sys)
	}
	gen, _ := body["generationConfig"].(map[string]any)
	if gen["maxOutputTokens"] != float64(128) {
		t.Errorf("generationConfig = %#v", gen)
	}
	tools, _ := body["tools"].([]any)
	tool, _ := tools[0].(map[string]any)
	decls, _ := tool["functionDeclarations"].([]any)
	decl, _ := decls[0].(map[string]any)
	if decl["name"] != "web_search" {
		t.Errorf("tools = %#v", tools)
	}
}

// TestGoogleStream pins the streamed Gemini chunks: the alt=sse URL, text
// deltas, a whole function call, and the usage metadata on the final chunk.
func TestGoogleStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-4-argon:streamGenerateContent" {
			t.Errorf("path = %q, want the streaming endpoint", r.URL.Path)
		}
		if got := r.URL.Query().Get("alt"); got != "sse" {
			t.Errorf("alt = %q, want sse", got)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"candidates":[{"content":{"parts":[{"text":"he"}]}}]}`,
			`data: {"candidates":[{"content":{"parts":[{"text":"llo"}]}}]}`,
			`data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"web_search","args":{"q":"x"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`,
			``,
		}, "\n\n")))
	}))
	defer srv.Close()

	c, err := NewClient(config.ModelConfig{
		APIType: config.APITypeGoogle, BaseURL: srv.URL + "/v1beta",
		APIKey: "sk-test", Model: "gemini-4-argon", MaxTokens: 128,
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	var deltas []string
	resp, err := c.StreamTurnsWithTools(context.Background(), "sys",
		[]Turn{{Role: "user", Content: "hi"}}, nil,
		func(s string) { deltas = append(deltas, s) }, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if strings.Join(deltas, "") != "hello" || resp.Text != "hello" {
		t.Fatalf("deltas = %q response = %+v", deltas, resp)
	}
	if len(resp.ToolUses) != 1 || resp.ToolUses[0].Name != "web_search" || resp.ToolUses[0].Input["q"] != "x" {
		t.Fatalf("tool uses = %+v", resp.ToolUses)
	}
	if u := c.Usage(); u.InputTokens != 10 || u.OutputTokens != 5 {
		t.Fatalf("usage = %+v", u)
	}
}

// TestAggregatorURLs pins the endpoint derivation both dialects use.
func TestAggregatorURLs(t *testing.T) {
	responses := map[string]string{
		"":                          "https://api.openai.com/v1/responses",
		"https://api.openai.com":    "https://api.openai.com/v1/responses",
		"https://api.openai.com/v1": "https://api.openai.com/v1/responses",
		"https://opencode.ai/inference/openai/v1": "https://opencode.ai/inference/openai/v1/responses",
		"https://example.com/v1/chat/completions": "https://example.com/v1/responses",
		"https://example.com/v1/responses":        "https://example.com/v1/responses",
		// Codex's ChatGPT backend is unversioned: /backend-api/codex/responses.
		"https://chatgpt.com/backend-api/codex": "https://chatgpt.com/backend-api/codex/responses",
	}
	for base, want := range responses {
		if got := responsesURL(base); got != want {
			t.Errorf("responsesURL(%q) = %q, want %q", base, got, want)
		}
	}

	google := []struct {
		base, model string
		stream      bool
		want        string
	}{
		{"https://opencode.ai/inference/google/v1beta", "gemini-4-argon", false,
			"https://opencode.ai/inference/google/v1beta/models/gemini-4-argon:generateContent"},
		{"https://opencode.ai/inference/google/v1beta", "gemini-4-argon", true,
			"https://opencode.ai/inference/google/v1beta/models/gemini-4-argon:streamGenerateContent?alt=sse"},
		// A base copied from the docs with the model baked in is reduced to
		// its root, so the configured model wins.
		{"https://generativelanguage.googleapis.com/v1beta/models/gemini-x", "gemini-4-argon", false,
			"https://generativelanguage.googleapis.com/v1beta/models/gemini-4-argon:generateContent"},
		{"", "gemini-4-argon", false,
			"https://generativelanguage.googleapis.com/v1beta/models/gemini-4-argon:generateContent"},
	}
	for _, tc := range google {
		if got := googleURL(tc.base, tc.model, tc.stream); got != tc.want {
			t.Errorf("googleURL(%q, %q, %v) = %q, want %q", tc.base, tc.model, tc.stream, got, tc.want)
		}
	}
}

// TestTurnsToGoogleSplitsToolBlocks pins the turn split Gemini requires:
// functionCall rides a model turn, functionResponse a user turn, even when the
// engine packed both block kinds into one Turn.
func TestTurnsToGoogleSplitsToolBlocks(t *testing.T) {
	got := turnsToGoogle([]Turn{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Blocks: []ContentBlock{
			{Type: "text", Text: "working"},
			{Type: "tool_use", ID: "t1", Name: "web_search", Input: map[string]any{"q": "x"}},
			{Type: "tool_result", ToolUseID: "t1", Content: "ok"},
		}},
	})
	if len(got) != 3 {
		t.Fatalf("contents = %+v", got)
	}
	if got[1].Role != "model" || got[1].Parts[0].Text != "working" || got[1].Parts[1].FunctionCall == nil {
		t.Fatalf("model turn = %+v", got[1])
	}
	if got[2].Role != "user" || len(got[2].Parts) != 1 || got[2].Parts[0].FunctionResponse == nil {
		t.Fatalf("user turn = %+v", got[2])
	}
	if name := got[2].Parts[0].FunctionResponse.Name; name != "web_search" {
		t.Fatalf("functionResponse name = %q, want the tool name", name)
	}
}

// TestTurnsToResponses pins the input-item shapes: assistant history uses
// output_text, and tool traffic becomes function_call/function_call_output
// items linked by call_id.
func TestTurnsToResponses(t *testing.T) {
	items := turnsToResponses([]Turn{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Blocks: []ContentBlock{
			{Type: "text", Text: "working"},
			{Type: "tool_use", ID: "t1", Name: "web_search", Input: map[string]any{"q": "x"}},
			{Type: "tool_result", ToolUseID: "t1", Content: "ok"},
		}},
	})
	if len(items) != 4 {
		t.Fatalf("items = %#v", items)
	}
	msg, ok := items[1].(responsesMessage)
	if !ok || msg.Role != "assistant" || msg.Content[0].Type != "output_text" {
		t.Fatalf("assistant message = %#v", items[1])
	}
	call, ok := items[2].(responsesFunctionCall)
	if !ok || call.CallID != "t1" || call.Name != "web_search" {
		t.Fatalf("function call = %#v", items[2])
	}
	out, ok := items[3].(responsesFunctionOutput)
	if !ok || out.CallID != "t1" || out.Output != "ok" {
		t.Fatalf("function output = %#v", items[3])
	}
}

// TestGoogleStreamJSONArrayFallback pins the non-SSE shape: a gateway that
// ignores alt=sse answers with a JSON array of chunks, which the stream path
// still folds rather than reporting an empty answer.
func TestGoogleStreamJSONArrayFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`[
			{"candidates":[{"content":{"parts":[{"text":"he"}]}}]},
			{"candidates":[{"content":{"parts":[{"text":"llo"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}
		]`))
	}))
	defer srv.Close()

	c, err := NewClient(config.ModelConfig{
		APIType: config.APITypeGoogle, BaseURL: srv.URL + "/v1beta",
		APIKey: "sk-test", Model: "gemini-4-argon", MaxTokens: 128,
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	var deltas []string
	resp, err := c.StreamTurnsWithTools(context.Background(), "sys",
		[]Turn{{Role: "user", Content: "hi"}}, nil,
		func(s string) { deltas = append(deltas, s) }, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if strings.Join(deltas, "") != "hello" || resp.Text != "hello" {
		t.Fatalf("deltas = %q response = %+v", deltas, resp)
	}
	if u := c.Usage(); u.InputTokens != 10 || u.OutputTokens != 5 {
		t.Fatalf("usage = %+v", u)
	}
}

// TestResponsesStreamEnvelopeOnly pins the proxy that streams only the
// completion envelope: its output items become the answer when no deltas
// arrived, without duplicating text when they did.
func TestResponsesStreamEnvelopeOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]},{"type":"function_call","call_id":"call_1","name":"web_search","arguments":"{\"q\":\"x\"}"}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
			``,
		}, "\n\n")))
	}))
	defer srv.Close()

	c, err := NewClient(config.ModelConfig{
		APIType: config.APITypeResponses, BaseURL: srv.URL + "/v1",
		APIKey: "sk-test", Model: "gpt-6.1-sol", MaxTokens: 128,
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	resp, err := c.StreamTurnsWithTools(context.Background(), "sys",
		[]Turn{{Role: "user", Content: "hi"}}, nil, nil, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if resp.Text != "hello" || len(resp.ToolUses) != 1 || resp.ToolUses[0].Name != "web_search" {
		t.Fatalf("response = %+v", resp)
	}
	if u := c.Usage(); u.InputTokens != 10 || u.OutputTokens != 5 {
		t.Fatalf("usage = %+v", u)
	}
}

// TestResponsesStreamJSONBody pins the gateway that ignores stream=true and
// answers with one JSON body: the stream path still returns the answer.
func TestResponsesStreamJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer srv.Close()

	c, err := NewClient(config.ModelConfig{
		APIType: config.APITypeResponses, BaseURL: srv.URL + "/v1",
		APIKey: "sk-test", Model: "gpt-6.1-sol", MaxTokens: 128,
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	resp, err := c.StreamTurnsWithTools(context.Background(), "sys",
		[]Turn{{Role: "user", Content: "hi"}}, nil, nil, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if resp.Text != "hello" {
		t.Fatalf("response = %+v", resp)
	}
	if u := c.Usage(); u.InputTokens != 10 || u.OutputTokens != 5 {
		t.Fatalf("usage = %+v", u)
	}
}

// TestGoogleListModels pins the Google catalogue shape: <root>/models with
// x-goog-api-key auth, and entries named "models/<id>" reduced to the id.
func TestGoogleListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models" {
			t.Errorf("path = %q, want /v1beta/models", r.URL.Path)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "sk-test" {
			t.Errorf("x-goog-api-key = %q", got)
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-4-argon"},{"name":"models/gemini-3.8-flash"}]}`))
	}))
	defer srv.Close()

	c, err := NewClient(config.ModelConfig{
		APIType: config.APITypeGoogle, BaseURL: srv.URL + "/v1beta",
		APIKey: "sk-test", Model: "gemini-4-argon",
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	if len(models) != 2 || models[0].ID != "gemini-4-argon" || models[1].ID != "gemini-3.8-flash" {
		t.Fatalf("models = %+v", models)
	}
}

// TestAggregatorThinking pins the reasoning knobs each dialect emits: an
// effort object for Responses, a thinkingConfig (with the output cap lifted
// above the budget) for Google, and nothing at all when thinking is unset.
func TestAggregatorThinking(t *testing.T) {
	on := &Client{model: "gpt-6.1-sol", maxTokens: 1024, thinking: "on"}
	req := on.buildResponsesRequest("sys", []Turn{{Role: "user", Content: "hi"}}, nil, false)
	if req.Reasoning == nil || req.Reasoning.Effort != "high" {
		t.Fatalf("responses thinking = %+v", req.Reasoning)
	}

	off := &Client{model: "gpt-6.1-sol", maxTokens: 1024, thinking: "off"}
	if req := off.buildResponsesRequest("sys", nil, nil, false); req.Reasoning == nil || req.Reasoning.Effort != "low" {
		t.Fatalf("responses off = %+v", req.Reasoning)
	}

	if req := (&Client{model: "gpt-6.1-sol", thinking: ""}).buildResponsesRequest("sys", nil, nil, false); req.Reasoning != nil {
		t.Fatalf("unset thinking sent %+v", req.Reasoning)
	}

	gem := &Client{model: "gemini-4-argon", maxTokens: 1024, thinking: "on", thinkingBudget: 2048}
	gre := gem.buildGoogleRequest("sys", nil, nil)
	if gre.GenerationConfig == nil || gre.GenerationConfig.ThinkingConfig == nil {
		t.Fatalf("google thinking missing: %+v", gre.GenerationConfig)
	}
	tc := gre.GenerationConfig.ThinkingConfig
	if !tc.IncludeThoughts || tc.ThinkingBudget == nil || *tc.ThinkingBudget != 2048 {
		t.Fatalf("google thinking = %+v", tc)
	}
	// Thinking tokens count as output, so the cap must clear the budget.
	if gre.GenerationConfig.MaxOutputTokens <= 2048 {
		t.Fatalf("maxOutputTokens = %d, want it lifted above the budget", gre.GenerationConfig.MaxOutputTokens)
	}

	gemOff := &Client{model: "gemini-4-argon", maxTokens: 1024, thinking: "off"}
	gre = gemOff.buildGoogleRequest("sys", nil, nil)
	if tc := gre.GenerationConfig.ThinkingConfig; tc == nil || tc.ThinkingBudget == nil || *tc.ThinkingBudget != 0 {
		t.Fatalf("google off thinking = %+v", tc)
	}
}

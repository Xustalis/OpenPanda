// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

func TestBoundedCompletionHasOneToolFreeRequest(t *testing.T) {
	for _, dialect := range []string{config.APITypeAnthropic, config.APITypeOpenAI, config.APITypeResponses, config.APITypeGoogle} {
		for _, failure := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/error=%v", dialect, failure), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					for _, key := range []string{"tools", "tool_choice", "web_search_options"} {
						if _, ok := body[key]; ok {
							t.Errorf("unexpected %s: %v", key, body[key])
						}
					}
					if dialect != config.APITypeGoogle && r.Header.Get("Authorization") != "Bearer live-key" && r.Header.Get("x-api-key") != "live-key" {
						t.Error("live credential was not preserved")
					}
					w.Header().Set("Content-Type", "application/json")
					if failure {
						w.WriteHeader(400)
						_, _ = w.Write([]byte(`{"error":{"message":"thinking is unsupported; retry with passback","type":"invalid_request_error"}}`))
						return
					}
					switch dialect {
					case config.APITypeOpenAI:
						thinking, _ := body["thinking"].(map[string]any)
						if thinking["type"] != "disabled" {
							t.Error("explicit reasoning-off request lost")
						}
						if body["max_tokens"] != float64(300) && body["max_completion_tokens"] != float64(300) {
							t.Error("output budget lost")
						}
						_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"draft"}}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`))
					case config.APITypeResponses:
						if body["max_output_tokens"] != float64(300) {
							t.Error("output budget lost")
						}
						_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"draft"}]}],"usage":{"input_tokens":10,"output_tokens":2}}`))
					case config.APITypeGoogle:
						_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"draft"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2}}`))
					default:
						if body["max_tokens"] != float64(300) {
							t.Error("output budget lost")
						}
						thinking, _ := body["thinking"].(map[string]any)
						if len(thinking) != 0 && thinking["type"] != "disabled" {
							t.Error("thinking still enabled")
						}
						_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"draft"}],"usage":{"input_tokens":10,"output_tokens":2}}`))
					}
				}))
				defer server.Close()
				c, err := NewClient(config.ModelConfig{Provider: "deepseek", APIType: dialect, BaseURL: server.URL + "/custom", APIKey: "old-key", Model: "custom-model", MaxTokens: 32000, Thinking: "on", ThinkingBudget: 16000, MaxRetries: 5, Params: map[string]any{"tools": []any{map[string]any{"type": "web_search"}}, "web_search_options": map[string]any{}}})
				if err != nil {
					t.Fatal(err)
				}
				c.keyFn = func(context.Context) (string, error) { return "live-key", nil }
				got, err := c.CompleteBounded(context.Background(), "system", "input", 300)
				if failure && err == nil || !failure && (err != nil || got != "draft") {
					t.Fatalf("got %q, %v", got, err)
				}
				if calls.Load() != 1 {
					t.Fatalf("requests=%d", calls.Load())
				}
				if !failure && c.Usage().OutputTokens != 2 {
					t.Fatal("usage was not conserved")
				}
				if c.maxTokens != 32000 || c.thinking != "on" {
					t.Fatal("primary client was mutated")
				}
			})
		}
	}
}

func TestBoundedRejectsToolsAndOversizedInput(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"content":[{"type":"tool_use","id":"call","name":"shell","input":{}}]}`))
	}))
	defer srv.Close()
	c, err := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "key", Model: "custom"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.CompleteBounded(context.Background(), "sys", strings.Repeat("x", 7501), 300); err == nil {
		t.Fatal("oversized input accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("oversized input reached network")
	}
	if _, err = c.CompleteBounded(context.Background(), "sys", "input", 300); err == nil {
		t.Fatal("tool output accepted")
	}
	if calls.Load() != 1 {
		t.Fatal("unexpected retry")
	}
}

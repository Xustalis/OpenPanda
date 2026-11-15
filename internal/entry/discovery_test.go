// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/defense"
)

func TestDiscoveryModelLoopAndSchemaCache(t *testing.T) {
	reg := NewRegistry()
	reg.Register(Tool{Name: "task_submit", Schema: map[string]any{"type": "object"}})
	reg.Register(Tool{Name: "inspect_machine", Description: "inspect memory pressure on this machine", Schema: map[string]any{"type": "object"}, Tier: defense.TierReversible, ReadOnly: true, Run: func(context.Context, map[string]any) (string, error) { return "memory is healthy", nil }})
	reg.EnableDiscovery(func(name string) bool { return name == "task_submit" })
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req struct {
			Tools []ToolSpec `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		names := map[string]bool{}
		for _, s := range req.Tools {
			names[s.Name] = true
		}
		if !names["task_submit"] || !names["tool_search"] {
			t.Error("lost universal dispatch/discovery bridge")
		}
		if calls == 1 {
			if names["inspect_machine"] {
				t.Error("eager schema loading")
			}
			fmt.Fprint(w, `{"content":[{"type":"tool_use","id":"discover","name":"tool_search","input":{"query":"memory pressure"}}],"stop_reason":"tool_use"}`)
		} else {
			if !names["inspect_machine"] {
				t.Error("discovered schema missing on next round")
			}
			fmt.Fprint(w, `{"content":[{"type":"tool_use","id":"inspect","name":"inspect_machine","input":{}}],"stop_reason":"tool_use"}`)
		}
	}))
	defer srv.Close()
	client, err := NewClient(config.ModelConfig{APIType: config.APITypeAnthropic, BaseURL: srv.URL, APIKey: "test", Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	turns := []Turn{{Role: "user", Content: "inspect available memory"}}
	for i := 0; i < 2; i++ {
		out, err := ClassifyTurnsWithTools(context.Background(), client, nil, "", turns, reg)
		if err != nil {
			t.Fatal(err)
		}
		if out.Tool == nil {
			t.Fatalf("no tool: %+v", out)
		}
		tool, ok := reg.Lookup(out.Tool.Tool)
		if !ok {
			t.Fatal("missing callable")
		}
		result, err := tool.Run(context.Background(), out.Tool.Arguments)
		if err != nil {
			t.Fatal(err)
		}
		turns = append(turns, Turn{Role: "user", Content: result})
		if i == 1 && result != "memory is healthy" {
			t.Fatal(result)
		}
	}
	before, _ := classifyCacheKey(PromptOptions{}, "test", reg)
	tool, _ := reg.Lookup("inspect_machine")
	tool.Description += " changed"
	reg.Register(tool)
	after, _ := classifyCacheKey(PromptOptions{}, "test", reg)
	if before == after {
		t.Fatal("schema revisions share classification cache")
	}
}

func TestDiscoveryCanBrowseUnmatchedCapabilities(t *testing.T) {
	r := NewRegistry()
	for i := 0; i < 19; i++ {
		r.Register(Tool{Name: fmt.Sprintf("cap_%02d", i), Description: "operation", Schema: map[string]any{"type": "object"}})
	}
	r.EnableDiscovery(func(string) bool { return false })
	tool, _ := r.Lookup("tool_search")
	got, err := tool.Run(context.Background(), map[string]any{"query": "*", "offset": float64(16)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "cap_18") || strings.Contains(got, "cap_00") {
		t.Fatal(got)
	}
	if len(r.Specs()) != 4 {
		t.Fatalf("catalog page was not bounded: %d", len(r.Specs()))
	}
}

func TestProviderCacheUsageAccounting(t *testing.T) {
	c := &Client{}
	// OpenAI reports cached input as a subset; Anthropic reports separate fields.
	var o oaiUsage
	_ = json.Unmarshal([]byte(`{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":70}}`), &o)
	c.addUsage(o.PromptTokens, o.CompletionTokens, o.PromptDetails.CachedTokens)
	if got := c.Usage(); got.InputTokens != 100 || got.CachedInputTokens != 70 || got.Total() != 105 {
		t.Fatalf("OpenAI cache double counted: %+v", got)
	}
	before := c.Usage()
	var a anthropicUsage
	_ = json.Unmarshal([]byte(`{"input_tokens":10,"output_tokens":6,"cache_read_input_tokens":60,"cache_creation_input_tokens":30}`), &a)
	c.addUsage(a.InputTokens+a.CacheRead+a.CacheWrite, a.OutputTokens, a.CacheRead, a.CacheWrite)
	if got := c.Usage().Sub(before); got.InputTokens != 100 || got.CachedInputTokens != 60 || got.CacheCreationTokens != 30 || got.OutputTokens != 6 {
		t.Fatalf("Anthropic cache missing: %+v", got)
	}
}

func TestProviderCacheStreamUsageIsCumulative(t *testing.T) {
	client := &Client{}
	var anth anthAccumulator
	for _, raw := range []string{
		`{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":60,"cache_creation_input_tokens":30}}}`,
		`{"type":"message_delta","usage":{"output_tokens":6}}`,
		`{"type":"message_delta","usage":{"input_tokens":10,"output_tokens":6,"cache_read_input_tokens":60,"cache_creation_input_tokens":30}}`,
	} {
		var event anthropicStreamEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatal(err)
		}
		anth.noteUsage(&event)
	}
	want := Usage{InputTokens: 100, OutputTokens: 6, CachedInputTokens: 60, CacheCreationTokens: 30}
	if got := anth.usage(client); got != want || client.Usage().Total() != 0 {
		t.Fatalf("Anthropic stream lost or double-counted cache usage: %+v", got)
	}
	client.addUsage(anth.usageIn, anth.usageOut, anth.usageCacheRead, anth.usageCacheWrite)
	var oai oaiAccumulator
	var chunk oaiChunk
	if err := json.Unmarshal([]byte(`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":70}}}`), &chunk); err != nil {
		t.Fatal(err)
	}
	oai.feed(&chunk, nil, nil)
	oai.feed(&chunk, nil, nil) // Usage chunks are cumulative, not deltas.
	if got := oai.usage(client).Sub(client.Usage()); got != (Usage{InputTokens: 100, OutputTokens: 5, CachedInputTokens: 70}) {
		t.Fatalf("OpenAI stream double-counted cached input: %+v", got)
	}
}

func TestDiscoveryCopiesAndFiltersStayIsolated(t *testing.T) {
	r := NewRegistry()
	r.Register(Tool{Name: "read", Description: "read"})
	r.Register(Tool{Name: "write", Description: "write"})
	r.EnableDiscovery(func(string) bool { return false })
	copy := r.Copy()
	search, _ := copy.Lookup("tool_search")
	if _, err := search.Run(context.Background(), map[string]any{"query": "read"}); err != nil {
		t.Fatal(err)
	}
	if len(copy.Specs()) != 2 || len(r.Specs()) != 1 {
		t.Fatal("copied discovery changed the source registry")
	}
	filtered := copy.Filter(func(name string) bool { return name != "write" })
	search, _ = filtered.Lookup("tool_search")
	text, err := search.Run(context.Background(), map[string]any{"query": "*"})
	if err != nil || strings.Contains(text, `"name":"write"`) {
		t.Fatalf("filtered capability leaked: %s %v", text, err)
	}
	if _, err := search.Run(context.Background(), map[string]any{"query": "*", "offset": 1e100}); err != nil {
		t.Fatal(err)
	}
}

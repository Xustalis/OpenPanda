// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

func TestBriefCallDoesNotMutateConversationBudget(t *testing.T) {
	var requests []messagesRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req messagesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		requests = append(requests, req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"digest"}],"usage":{"input_tokens":10,"output_tokens":2}}`))
	}))
	defer srv.Close()
	model := config.ModelConfig{BaseURL: srv.URL, APIKey: "test", Model: "test", MaxTokens: 32000, Thinking: "on", ThinkingBudget: 16000, MaxRetries: 5}
	c, err := NewClient(model)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CompleteBrief(context.Background(), "compress", "history"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Complete(context.Background(), "normal", "task"); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0].MaxTokens != 2048 || requests[0].Thinking["type"] != "disabled" || requests[1].MaxTokens != 32000 || requests[1].Thinking["type"] != "enabled" {
		t.Fatalf("requests: %+v", requests)
	}
	if u := c.Usage(); u.InputTokens != 20 || u.OutputTokens != 4 {
		t.Fatalf("usage not conserved: %+v", u)
	}
	brief := BriefModelConfig(model)
	if brief.MaxRetries != 1 || model.MaxTokens != 32000 || c.thinking != "on" {
		t.Fatal("brief call changed primary configuration")
	}
}

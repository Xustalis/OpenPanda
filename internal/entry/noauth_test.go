package entry_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
)

func TestNoAuthClient(t *testing.T) {
	var receivedAuthHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")
		if receivedAuthHeader == "" {
			receivedAuthHeader = r.Header.Get("x-api-key")
		}

		if r.URL.Path == "/v1/chat/completions" || r.URL.Path == "/chat/completions" {
			w.Header().Set("content-type", "application/json")
			resp := map[string]any{
				"choices": []map[string]any{
					{
						"message": map[string]string{
							"role":    "assistant",
							"content": "ok from local noauth",
						},
						"finish_reason": "stop",
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		if r.URL.Path == "/v1/models" || r.URL.Path == "/models" {
			w.Header().Set("content-type", "application/json")
			resp := map[string]any{
				"data": []map[string]any{
					{"id": "local-llama"},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		http.NotFound(w, r)
	}))
	defer srv.Close()

	client, err := entry.NewClient(config.ModelConfig{
		BaseURL: srv.URL + "/v1",
		Model:   "local-llama",
		APIType: config.APITypeOpenAI,
		NoAuth:  true,
		APIKey:  "",
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	// 1. Test complete call with no auth
	resp, err := client.CompleteTurnsWithTools(context.Background(), "system", []entry.Turn{
		{Role: "user", Content: "hello"},
	}, nil)
	if err != nil {
		t.Fatalf("CompleteTurnsWithTools failed: %v", err)
	}
	if resp.Text != "ok from local noauth" {
		t.Fatalf("unexpected response: %q", resp.Text)
	}
	if receivedAuthHeader != "" {
		t.Fatalf("expected no auth header sent, got: %q", receivedAuthHeader)
	}

	// 2. Test ListModels
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if len(models) == 0 || models[0].ID != "local-llama" {
		t.Fatalf("unexpected models: %+v", models)
	}
}

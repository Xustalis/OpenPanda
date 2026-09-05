package askengine

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

func TestAskModelFallback(t *testing.T) {
	root := t.TempDir()

	// Primary server fails with 401 Unauthorized
	primarySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		resp := map[string]any{
			"error": map[string]string{
				"type":    "authentication_error",
				"message": "invalid api key or quota exhausted",
			},
		}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer primarySrv.Close()

	// Fallback server succeeds
	fallbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		resp := map[string]any{
			"content": []map[string]string{{"type": "text", "text": "备用模型回答成功"}},
			"usage":   map[string]int{"input_tokens": 10, "output_tokens": 8},
		}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer fallbackSrv.Close()

	primaryClient, err := entry.NewClient(config.ModelConfig{
		BaseURL: primarySrv.URL,
		Model:   "primary-model",
		APIKey:  "bad-key",
	})
	if err != nil {
		t.Fatal(err)
	}

	fallbackClient, err := entry.NewClient(config.ModelConfig{
		BaseURL: fallbackSrv.URL,
		Model:   "fallback-model",
		APIKey:  "good-key",
	})
	if err != nil {
		t.Fatal(err)
	}

	db, err := storage.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(db); err != nil {
		t.Fatal(err)
	}

	hermes := memory.NewHermes(root)
	e := &Engine{
		cfg: &config.Config{
			Model: config.ModelConfig{
				BaseURL: primarySrv.URL,
				Model:   "primary-model",
				APIKey:  "bad-key",
			},
		},
		injector:  memory.NewInjector(hermes, nil),
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		fallbacks: []*entry.Client{fallbackClient},
	}
	e.registry = buildToolRegistry(e, hermes, nil, nil)
	e.db = db
	e.client.Store(primaryClient)

	res, err := e.Ask(context.Background(), "你好", false)
	if err != nil {
		t.Fatalf("unexpected error during fallback: %v", err)
	}
	if res.Answer != "备用模型回答成功" {
		t.Fatalf("unexpected answer: got %q, want '备用模型回答成功'", res.Answer)
	}
	if !strings.Contains(res.Note, "备用模型") {
		t.Fatalf("expected note to mention fallback model, got %q", res.Note)
	}
}

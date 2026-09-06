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

func TestCircuitBreakerFallback(t *testing.T) {
	root := t.TempDir()

	primaryHits := 0
	primarySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryHits++
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		resp := map[string]any{
			"error": map[string]string{
				"type":    "server_error",
				"message": "upstream outage",
			},
		}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer primarySrv.Close()

	fallbackHits := 0
	fallbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits++
		w.Header().Set("content-type", "application/json")
		resp := map[string]any{
			"content": []map[string]string{{"type": "text", "text": "fallback response"}},
			"usage":   map[string]int{"input_tokens": 5, "output_tokens": 5},
		}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer fallbackSrv.Close()

	primaryClient, err := entry.NewClient(config.ModelConfig{
		BaseURL: primarySrv.URL,
		Model:   "primary-model",
		APIKey:  "key",
	})
	if err != nil {
		t.Fatal(err)
	}

	fallbackClient, err := entry.NewClient(config.ModelConfig{
		BaseURL: fallbackSrv.URL,
		Model:   "fallback-model",
		APIKey:  "key",
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
				APIKey:  "key",
			},
		},
		injector:  memory.NewInjector(hermes, nil),
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		fallbacks: []*entry.Client{fallbackClient},
	}
	e.registry = buildToolRegistry(e, hermes, nil, nil)
	e.db = db
	e.client.Store(primaryClient)

	// Call 1: primary fails, falls back to fallback model
	res1, err := e.Ask(context.Background(), "hello 1", false)
	if err != nil {
		t.Fatalf("first ask failed: %v", err)
	}
	if res1.Answer != "fallback response" {
		t.Fatalf("expected fallback response, got %s", res1.Answer)
	}
	if primaryHits != 3 {
		t.Fatalf("expected primaryHits == 3 (1 initial + 2 retries), got %d", primaryHits)
	}
	if fallbackHits != 1 {
		t.Fatalf("expected fallbackHits == 1, got %d", fallbackHits)
	}

	// Call 2: primary is in circuit breaker cooldown, should route directly to fallback
	res2, err := e.Ask(context.Background(), "hello 2", false)
	if err != nil {
		t.Fatalf("second ask failed: %v", err)
	}
	if res2.Answer != "fallback response" {
		t.Fatalf("expected fallback response, got %s", res2.Answer)
	}
	if primaryHits != 3 {
		t.Fatalf("expected primaryHits still == 3 (bypassed via circuit breaker), got %d", primaryHits)
	}
	if fallbackHits != 2 {
		t.Fatalf("expected fallbackHits == 2, got %d", fallbackHits)
	}
}

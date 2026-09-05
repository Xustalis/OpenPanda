package askengine

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// TestFastPathCancellationReturnsImmediateErr verifies that if context is cancelled
// during fast-path triage, it immediately returns ctx.Err() without falling through
// to the heavy classification/scheduler loop.
func TestFastPathCancellationReturnsImmediateErr(t *testing.T) {
	root := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow/hanging response that will be cancelled
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	client, err := entry.NewClient(config.ModelConfig{
		APIType: "openai", BaseURL: srv.URL, Model: "test-model", APIKey: "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}

	e := &Engine{
		cfg:      &config.Config{},
		injector: memory.NewInjector(memory.NewHermes(root), nil),
	}
	db, err := storage.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	e.db = db
	e.client.Store(client)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Pre-cancelled

	res, err := e.AskTurns(ctx, nil, "你好", "", true, StreamCallbacks{})
	if err == nil {
		t.Fatalf("expected error on cancelled context, got nil res=%+v", res)
	}
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// TestFastPathReachableThroughNewEngine is the production-shape regression for
// the fast-path gate: a real engine from New() always carries a non-empty
// built-in tool registry, so the gate must key on the triage verdict, not on
// registry emptiness. It pins that a plain conversational question sends the
// fast-path system prompt to the provider — and not the heavy orchestration
// prompt — through a fully constructed engine.
func TestFastPathReachableThroughNewEngine(t *testing.T) {
	root := t.TempDir()
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Hello world!"}}]}`)
	}))
	defer srv.Close()

	cfg := &config.Config{}
	cfg.Storage.DBPath = filepath.Join(root, "test.db")
	cfg.Storage.MemoryPath = filepath.Join(root, "memory")
	cfg.Storage.ProjectsPath = filepath.Join(root, "projects")
	cfg.Model = config.ModelConfig{APIType: "openai", BaseURL: srv.URL, Model: "test-model", APIKey: "test-key"}

	e, err := New(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer e.Close()

	res, err := e.AskTurns(context.Background(), nil, "什么是量子纠缠？", "", true, StreamCallbacks{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil || res.Kind != "answer" {
		t.Fatalf("expected answer result, got %+v", res)
	}
	if res.Answer != "Hello world!" {
		t.Fatalf("expected fast-path answer 'Hello world!', got %q", res.Answer)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) == 0 {
		t.Fatal("provider received no requests")
	}
	if !strings.Contains(bodies[0], "智能技术助手") {
		t.Errorf("first provider request did not carry the fast-path system prompt; body: %.200s", bodies[0])
	}
	if strings.Contains(bodies[0], "大总管") {
		t.Errorf("first provider request carried the heavy orchestration system prompt; body: %.200s", bodies[0])
	}
}

// TestFastPathStreamingDeliversDirectAnswer verifies that a simple query takes
// the fast path, streams deltas, and delivers the answer directly.
func TestFastPathStreamingDeliversDirectAnswer(t *testing.T) {
	root := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("test server received: %s %s", r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Send SSE chunks
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hello \"}}]}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"world!\"}}]}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	client, err := entry.NewClient(config.ModelConfig{
		APIType: "openai", BaseURL: srv.URL, Model: "test-model", APIKey: "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}

	e := &Engine{
		cfg:      &config.Config{},
		injector: memory.NewInjector(memory.NewHermes(root), nil),
	}
	db, err := storage.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	e.db = db
	e.client.Store(client)

	var mu sync.Mutex
	var deltas []string
	cb := StreamCallbacks{
		OnDelta: func(chunk string) {
			mu.Lock()
			deltas = append(deltas, chunk)
			mu.Unlock()
		},
	}

	res, err := e.AskTurns(context.Background(), nil, "explain the difference between process and thread", "", true, cb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil || res.Kind != "answer" {
		t.Fatalf("expected answer result, got %+v", res)
	}
	if res.Answer != "Hello world!" {
		t.Fatalf("expected 'Hello world!', got %q", res.Answer)
	}

	mu.Lock()
	collected := strings.Join(deltas, "")
	mu.Unlock()
	if collected != "Hello world!" {
		t.Fatalf("expected streamed deltas 'Hello world!', got %q", collected)
	}
}

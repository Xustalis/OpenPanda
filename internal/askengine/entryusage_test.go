package askengine

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// TestAskRecordsEntryUsage verifies the ask engine bills the entry (commander)
// model's own token consumption into the delegation metrics after each ask, so
// the web panel's tokens column reflects the commander's cost alongside
// adapter delegations.
func TestAskRecordsEntryUsage(t *testing.T) {
	root := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		resp := map[string]any{
			"content": []map[string]string{{"type": "text", "text": "你好呀"}},
			"usage":   map[string]int{"input_tokens": 8, "output_tokens": 6},
		}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)

	client, err := entry.NewClient(config.ModelConfig{BaseURL: srv.URL, Model: "test-model", APIKey: "test-key"})
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
		cfg:      &config.Config{},
		injector: memory.NewInjector(hermes, nil),
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	e.registry = buildToolRegistry(e, hermes, nil, nil)
	e.db = db
	e.client.Store(client)

	res, err := e.Ask(context.Background(), "你好", false)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if res.Kind != "answer" || res.Answer != "你好呀" {
		t.Fatalf("res = %+v, want the answer 你好呀", res)
	}

	metrics, err := core.NewTaskStore(db, nil).ListDelegationMetrics(context.Background())
	if err != nil {
		t.Fatalf("list metrics: %v", err)
	}
	found := false
	for _, m := range metrics {
		if m.Executor == "entry:test-model" {
			found = true
			if !m.Tokens.Valid || m.Tokens.Int64 != 14 {
				t.Fatalf("entry usage tokens = %+v, want 14 (8 in + 6 out)", m.Tokens)
			}
		}
	}
	if !found {
		t.Fatalf("no entry:<model> metric row recorded; metrics = %+v", metrics)
	}
}

package askengine

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// TestAskTurnsMemoryWall verifies the isolation wall of design §17.2 at the
// engine level: Hermes personal memory enters the classification prompt only
// for project-free conversations (empty workDir). A conversation pinned to a
// workspace (workDir set — a session worktree or the shared work path) must
// carry no personal memory at all, so nothing personal can leak into a task
// spawned from that conversation.
func TestAskTurnsMemoryWall(t *testing.T) {
	// One Hermes store holding one deliberately recognizable profile fact.
	root := t.TempDir()
	hermes := memory.NewHermes(root)
	profile := filepath.Join(root, "USER.md")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profile, []byte("- 用户喜欢暗色主题\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A fake OpenAI endpoint that records the request body and always answers.
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
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
		injector: memory.NewInjector(hermes, nil),
		registry: buildToolRegistry(hermes, nil, nil),
	}
	// AskTurns reads the capability directory (ledger.Query); give it a real
	// (empty) SQLite store so the query succeeds with no devices.
	db, err := storage.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	e.db = db
	e.client.Store(client)

	// Project-free conversation: memory must be present.
	if _, err := e.AskTurns(context.Background(), nil, "你好", "", true, StreamCallbacks{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	free := bodies[len(bodies)-1]
	mu.Unlock()
	if !strings.Contains(free, "暗色主题") {
		t.Fatal("project-free conversation is missing personal memory")
	}

	// Workspace conversation: memory must be absent.
	if _, err := e.AskTurns(context.Background(), nil, "重构导航栏", filepath.Join(root, "worktree"), true, StreamCallbacks{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	proj := bodies[len(bodies)-1]
	mu.Unlock()
	if strings.Contains(proj, "暗色主题") {
		t.Fatal("personal memory leaked into a workspace conversation (memory wall §17.2 violated)")
	}
}

// TestWorkPath ensures the panel can pin non-repo sessions to the work path.
func TestWorkPath(t *testing.T) {
	e := &Engine{cfg: &config.Config{}}
	e.cfg.Storage.WorkPath = "/tmp/panda-work"
	var got atomic.Value
	got.Store(e.WorkPath())
	if got.Load() != "/tmp/panda-work" {
		t.Fatalf("WorkPath = %v, want /tmp/panda-work", got.Load())
	}
}

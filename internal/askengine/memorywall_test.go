// SPDX-License-Identifier: AGPL-3.0-or-later

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

// Personal memory follows the trusted owner entry, independently of cwd.
// Non-owner asks remain isolated even when they do not name a project.
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
	}
	e.registry = buildToolRegistry(e, hermes, nil, nil)
	// AskTurns reads the capability directory (ledger.Query); give it a real
	// (empty) SQLite store so the query succeeds with no devices.
	db, err := storage.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	e.db = db
	e.client.Store(client)

	for _, tc := range []struct {
		name  string
		scope AskScope
		want  bool
	}{
		{"owner project-free", AskScope{PersonalMemory: true}, true},
		{"owner workspace", AskScope{PersonalMemory: true, Project: "alpha", WorkDir: filepath.Join(root, "alpha")}, true},
		{"owner side", AskScope{PersonalMemory: true, Side: true, Project: "beta", WorkDir: filepath.Join(root, "beta")}, true},
		{"worker workspace", AskScope{Project: "alpha", WorkDir: filepath.Join(root, "alpha")}, false},
		{"worker project-free", AskScope{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := e.AskTurnsScoped(context.Background(), nil, "读取我的偏好", tc.scope, true, StreamCallbacks{}); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			body := bodies[len(bodies)-1]
			mu.Unlock()
			if got := strings.Contains(body, "暗色主题"); got != tc.want {
				t.Fatalf("personal memory in prompt = %v, want %v", got, tc.want)
			}
		})
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

// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/configsync"
)

func TestPersistedModelSwitchReachesRunningEngine(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	card := filepath.Join(root, "capabilities.yaml")
	body := fmt.Sprintf("node:\n  name: sync-node\nstorage:\n  db_path: %q\n  memory_path: %q\n  projects_path: %q\n  work_path: %q\nmodel:\n  provider: deepseek\n  model: first\n  api_key: fake-test-key\n", filepath.Join(root, "tasks.db"), filepath.Join(root, "memory"), filepath.Join(root, "projects"), root)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(card, []byte("device: sync-node\nresource_class: Standard\nnative:\n  - id: sys:info\n    command: go\n    args: [version]\n    tier: 1\ncapacity:\n  cpu_cores: 2\n  ram_gb: 4\n  max_concurrent_tasks: 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(context.Background(), cfg, Options{ConfigPath: path, CardPath: card, LocalOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	next := cfg.Model
	next.Model = "second"
	if err := config.UpdateModelSection(path, next); err != nil {
		t.Fatal(err)
	}
	await := func(model string, configured bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			mc := e.ModelConfig()
			sc := e.sched.Load().ModelConfig()
			report, err := configsync.ReadReport(context.Background(), e.db, path)
			if err == nil && len(report.Processes) == 1 && report.Processes[0].Applied && mc.Model == model && sc.Model == model && mc.Configured() == configured {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("persisted model did not reach entry and scheduler with acknowledgement")
	}
	await("second", true)
	if err := config.ClearModelSection(path); err != nil {
		t.Fatal(err)
	}
	await("", false)
}

// Disable polling and prove the request preflight closes the one-second
// window between an external save and the next watcher tick.
func TestAskImmediatelyObservesPersistedModel(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	card := filepath.Join(root, "capabilities.yaml")
	var observed atomic.Value
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		observed.Store(req.Model)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"A conceptual answer."}]}`))
	}))
	defer model.Close()
	body := fmt.Sprintf("node:\n  name: sync-node\nstorage:\n  db_path: %q\n  memory_path: %q\n  projects_path: %q\n  work_path: %q\nmodel:\n  base_url: %q\n  api_type: anthropic\n  model: first\n  no_auth: true\n", filepath.Join(root, "tasks.db"), filepath.Join(root, "memory"), filepath.Join(root, "projects"), root, model.URL)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(card, []byte("device: sync-node\nresource_class: Standard\nnative:\n  - id: sys:info\n    command: go\n    args: [version]\n    tier: 1\ncapacity:\n  cpu_cores: 2\n  ram_gb: 4\n  max_concurrent_tasks: 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(context.Background(), cfg, Options{ConfigPath: path, CardPath: card, LocalOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.modelSyncCancel()
	next := cfg.Model
	next.Model = "immediate"
	if err := config.UpdateModelSection(path, next); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Ask(context.Background(), "Explain a concept", false); err != nil {
		t.Fatal(err)
	}
	if got := observed.Load(); got != "immediate" || e.sched.Load().ModelConfig().Model != "immediate" {
		t.Fatalf("request used stale model: %v", got)
	}
}

func TestPersistedAgentPolicyReachesRunningEngine(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	card := filepath.Join(root, "capabilities.yaml")
	body := fmt.Sprintf("node:\n  name: policy-sync\nstorage:\n  db_path: %q\n  memory_path: %q\n  work_path: %q\n", filepath.Join(root, "tasks.db"), filepath.Join(root, "memory"), root)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(card, []byte("device: policy-sync\nresource_class: Standard\nnative:\n  - id: sys:info\n    command: go\n    args: [version]\n    tier: 1\ncapacity:\n  max_concurrent_tasks: 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(context.Background(), cfg, Options{ConfigPath: path, CardPath: card, LocalOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	r := cfg.Routing
	r.AgentModels = map[string][]string{"codex": {"root"}}
	r.SubagentModels = map[string][]string{"codex": {"child"}}
	r.MaxConcurrent = 5
	d := cfg.Delegation
	d.MaxConcurrent, d.FanOut = 2, 3
	if err := config.UpdateAgentExecution(path, r, d); err != nil {
		t.Fatal(err)
	}
	// Disable polling and force the same pre-attempt refresh used by executions.
	e.modelSyncCancel()
	if err := e.modelWatcher.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := e.sched.Load().RuntimeConfig()
	if got.Routing.SubagentModels["codex"][0] != "child" || got.Routing.MaxConcurrent != 5 || got.Delegation.FanOut != 3 || got.Delegation.MaxConcurrent != 2 {
		t.Fatalf("stale runtime: %+v", got)
	}
	if e.RuntimeConfig().Routing.AgentModels["codex"][0] != "root" {
		t.Fatal("engine acknowledgement did not match applied policy")
	}
}

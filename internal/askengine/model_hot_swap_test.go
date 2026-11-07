// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

func TestSetModelSynchronizesScheduler(t *testing.T) {
	root := t.TempDir()
	cardPath := filepath.Join(root, "capabilities.yaml")
	if err := os.WriteFile(cardPath, []byte(`device: model-test
resource_class: Standard
native:
  - id: sys:info
    command: go
    args: [version]
    tier: 1
capacity:
  cpu_cores: 2
  ram_gb: 4
  max_concurrent_tasks: 2
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Node.Name = "model-test"
	cfg.Node.Kind = "vm"
	cfg.Storage.DBPath = filepath.Join(root, "panda.db")
	cfg.Storage.MemoryPath = filepath.Join(root, "memory")
	cfg.Storage.ProjectsPath = filepath.Join(root, "projects")
	cfg.Storage.WorkPath = root
	cfg.Model = config.ModelConfig{Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-old"}
	e, err := New(context.Background(), cfg, Options{CardPath: cardPath, LocalOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	sched := e.sched.Load()
	if sched == nil {
		t.Fatal("explicit card did not initialize a scheduler")
	}
	next := config.ModelConfig{Provider: "deepseek", Model: "deepseek-reasoner", APIKey: "sk-new"}
	if err := e.SetModel(next); err != nil {
		t.Fatal(err)
	}
	if got := sched.ModelConfig(); got.Model != next.Model || got.APIKey != next.APIKey {
		t.Fatal("entry model switch did not propagate to scheduler")
	}

	// SetModel/ClearModel share the scheduler-init lock, including when
	// concurrent settings requests arrive. Race-test both readers as well.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 10; n++ {
				if err := e.SetModel(next); err != nil {
					t.Error(err)
				}
				_ = sched.ModelConfig()
				_ = e.ModelConfig()
				e.ClearModel()
			}
		}()
	}
	wg.Wait()
	e.ClearModel()
	if got := sched.ModelConfig(); got.Model != "" || got.APIKey != "" {
		t.Fatal("clearing entry model retained scheduler credentials")
	}
	if err := e.SetModel(next); err != nil {
		t.Fatal(err)
	}
	if sched.ModelConfig().Model != next.Model {
		t.Fatal("re-adding a model did not restore scheduler defaults")
	}
}

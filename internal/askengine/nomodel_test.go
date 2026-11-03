// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
)

// TestEngineWithoutModel covers the model-less node: a Micro/edge node whose
// only job is executing delegated work carries no API key, so New must not
// hard-fail on ErrNoModel — the task surface (enqueue/cancel/plan) stays
// available and only the ask path reports the missing model lazily.
func TestEngineWithoutModel(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{}
	cfg.Storage.DBPath = filepath.Join(root, "panda.db")
	cfg.Storage.MemoryPath = filepath.Join(root, "memory")
	cfg.Storage.ProjectsPath = filepath.Join(root, "projects")
	cfg.Storage.WorkPath = root
	cfg.Node.Name = "modelless"
	cfg.Node.Kind = "vm"
	cardPath := filepath.Join(root, "capabilities.yaml")

	e, err := New(context.Background(), cfg, Options{CardPath: cardPath})
	if err != nil {
		t.Fatalf("New with no model configured must succeed: %v", err)
	}
	defer e.Close()

	// The ask path fails lazily with the same contract the caller prints.
	if _, err := e.Ask(context.Background(), "hello", false); !errors.Is(err, entry.ErrNoModel) {
		t.Fatalf("Ask on a model-less engine = %v, want ErrNoModel", err)
	}

	// Task submission is model-free: it needs the scheduler (capability card),
	// which the engine built from the generated card at startup.
	if e.sched.Load() == nil {
		t.Skip("no capability card available in this environment; skipping enqueue check")
	}
	ctx := context.Background()
	task, _, err := e.EnqueueTask(ctx, core.TaskInput{
		Title:    "probe",
		Intent:   "probe",
		Requires: []string{"nonexistent-ability-xyz"},
	}, core.DefaultQueueSpec())
	if err != nil {
		t.Fatalf("EnqueueTask on a model-less engine: %v", err)
	}
	if task.TaskID == "" {
		t.Fatal("EnqueueTask returned an empty task id")
	}

	// Cancel on the row just created exercises the store path end-to-end —
	// no model client is ever consulted.
	ids, err := e.CancelTask(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("CancelTask on a model-less engine: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("CancelTask returned no cancelled ids for a queued task")
	}
}

// TestClearModelStopsDialingTheRemovedEntry covers deleting the last model at
// runtime. ClearModelSection empties `model:` on disk, but the engine keeps the
// client it built at startup — and the ask path reads that client, not the
// config — so the row the user just deleted kept answering every turn while
// every surface reported "no model configured". ClearModel is the missing half:
// the same model-less state New builds for a keyless node.
func TestClearModelStopsDialingTheRemovedEntry(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{}
	cfg.Storage.DBPath = filepath.Join(root, "panda.db")
	cfg.Storage.MemoryPath = filepath.Join(root, "memory")
	cfg.Storage.ProjectsPath = filepath.Join(root, "projects")
	cfg.Storage.WorkPath = root
	cfg.Node.Name = "clear-model"
	cfg.Node.Kind = "vm"
	cfg.Model = config.ModelConfig{Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-test"}

	e, err := New(context.Background(), cfg, Options{CardPath: filepath.Join(root, "capabilities.yaml")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer e.Close()

	if got := e.ModelConfig(); got.Model != "deepseek-chat" {
		t.Fatalf("model before clear = %+v, want the configured one", got)
	}

	e.ClearModel()

	if got := e.ModelConfig(); got.Model != "" || got.Provider != "" {
		t.Fatalf("model after clear = %+v, want an empty section", got)
	}
	if _, err := e.Ask(context.Background(), "hello", false); !errors.Is(err, entry.ErrNoModel) {
		t.Fatalf("Ask after clear = %v, want ErrNoModel — the removed client must not be dialed", err)
	}

	// The engine is reusable: adding a model back is the same SetModel the
	// console and /model add take, and it must leave the model-less state.
	if err := e.SetModel(config.ModelConfig{Provider: "deepseek", Model: "deepseek-reasoner", APIKey: "sk-next"}); err != nil {
		t.Fatalf("SetModel after clear: %v", err)
	}
	if got := e.ModelConfig(); got.Model != "deepseek-reasoner" {
		t.Fatalf("model after re-adding = %+v, want the new one", got)
	}
}

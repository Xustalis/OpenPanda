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
	task, err := e.EnqueueTask(ctx, core.TaskInput{
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

// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/nodeidentity"
)

// TestBorrowedEngineIsSubmitOnly pins the one-pipeline-per-identity rule:
// when a daemon holds the identity lock, an engine sharing that identity —
// `panda web`, the panel sidecar, any QueueTasks surface — must not start a
// second queue consumer, peer dials, or discovery. It submits to the shared
// store and the daemon's scheduler does the work. Before the gate, the web
// console ran a parallel scheduler that raced the daemon for claims and its
// sibling conns churned the daemon's held edges: two work logics for one
// node.
func TestBorrowedEngineIsSubmitOnly(t *testing.T) {
	identity := "borrowed-engine-" + t.Name()
	lock, err := nodeidentity.Acquire(config.NodeKindVM, identity)
	if err != nil {
		t.Fatalf("simulate daemon lock: %v", err)
	}
	defer lock.Release()

	root := t.TempDir()
	cfg := &config.Config{}
	cfg.Storage.DBPath = filepath.Join(root, "panda.db")
	cfg.Storage.MemoryPath = filepath.Join(root, "memory")
	cfg.Storage.ProjectsPath = filepath.Join(root, "projects")
	cfg.Storage.WorkPath = root
	cfg.Node.Name = "borrowed"
	cfg.Node.Kind = config.NodeKindVM
	cfg.Node.Identity = identity
	cfg.Network.DiscoveryAddr = "off"
	cardPath := filepath.Join(root, "capabilities.yaml")

	e, err := New(context.Background(), cfg, Options{
		CardPath:   cardPath,
		QueueTasks: true, // the web console's flag — the borrowed gate must override it
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer e.Close()

	sched := e.sched.Load()
	if sched == nil {
		t.Skip("no capability card available in this environment")
	}
	if sched.OwnsNodeRow() {
		t.Fatalf("borrowed engine claims the node row while the lock is held")
	}

	task, err := e.EnqueueTask(context.Background(), core.TaskInput{
		Title:    "borrowed submit",
		Requires: []string{"coding"},
	}, core.DefaultQueueSpec())
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// No consumer may ever claim the row — it waits for the daemon that owns
	// the identity. A short window distinguishes "not started" from "slow".
	time.Sleep(400 * time.Millisecond)
	got, err := e.TaskStore().Get(context.Background(), task.TaskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != core.StateQueued {
		t.Fatalf("borrowed engine consumed the task itself: state=%s, want queued", got.State)
	}
}

// TestOwnerEngineConsumes is the control: the same engine shape with the lock
// free owns the row, starts the queue consumer, and claims the task.
func TestOwnerEngineConsumes(t *testing.T) {
	identity := "owner-engine-" + t.Name()
	root := t.TempDir()
	cfg := &config.Config{}
	cfg.Storage.DBPath = filepath.Join(root, "panda.db")
	cfg.Storage.MemoryPath = filepath.Join(root, "memory")
	cfg.Storage.ProjectsPath = filepath.Join(root, "projects")
	cfg.Storage.WorkPath = root
	cfg.Node.Name = "owner"
	cfg.Node.Kind = config.NodeKindVM
	cfg.Node.Identity = identity
	cfg.Network.DiscoveryAddr = "off"
	cardPath := filepath.Join(root, "capabilities.yaml")

	e, err := New(context.Background(), cfg, Options{
		CardPath:   cardPath,
		QueueTasks: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer e.Close()

	sched := e.sched.Load()
	if sched == nil {
		t.Skip("no capability card available in this environment")
	}
	if !sched.OwnsNodeRow() {
		t.Fatalf("owner engine with free lock marked borrowed")
	}

	task, err := e.EnqueueTask(context.Background(), core.TaskInput{
		Title:    "owner claim",
		Requires: []string{"coding"},
	}, core.DefaultQueueSpec())
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := e.TaskStore().Get(context.Background(), task.TaskID)
		if err != nil {
			t.Fatalf("get task: %v", err)
		}
		if got.State != core.StateQueued {
			return // claimed — the consumer is running
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("owner engine's queue consumer never claimed the task")
}

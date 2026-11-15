// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/nodeidentity"
	"github.com/Xustalis/OpenPanda/internal/testdb"
)

func TestWorkRequestStartupRecoveryRespectsQueueOwnership(t *testing.T) {
	for _, borrowed := range []bool{false, true} {
		name := "owner"
		if borrowed {
			name = "borrowed"
		}
		t.Run(name, func(t *testing.T) {
			isolateIdentityLockRoot(t)
			root := t.TempDir()
			cfg := &config.Config{}
			cfg.Storage.DBPath = filepath.Join(root, "panda.db")
			cfg.Storage.MemoryPath = filepath.Join(root, "memory")
			cfg.Storage.ProjectsPath = filepath.Join(root, "projects")
			cfg.Storage.WorkPath = root
			cfg.Node.Name, cfg.Node.Kind, cfg.Node.Identity = name, config.NodeKindVM, t.Name()
			cfg.Network.DiscoveryAddr = "off"
			db := testdb.OpenFile(t, cfg.Storage.DBPath)
			c := core.NewCore(db, core.RuntimeNodeID(name, cfg.Node.Kind, cfg.Node.Identity), ledger.Card{Device: name}, 5, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg.Model)
			c.SetWorkDir(root)
			// Leave a genuinely reserved request at the failed publication
			// boundary, rather than duplicating the kernel's payload hashing.
			if _, err := db.Exec(`CREATE TRIGGER interrupt_work BEFORE INSERT ON tasks BEGIN SELECT RAISE(ABORT,'interrupted publication'); END`); err != nil {
				t.Fatal(err)
			}
			_, err := c.EnqueueRequest(context.Background(), core.ExecutionRequest{RequestID: "restart-request", TaskKey: "local/local/local/restart", TriggerKey: "restart", Purpose: "execute"}, core.TaskInput{Title: "recover reserved work"}, core.DefaultQueueSpec())
			if err == nil {
				t.Fatal("publication unexpectedly succeeded")
			}
			if _, err := db.Exec(`DROP TRIGGER interrupt_work`); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if borrowed {
				lock, err := nodeidentity.Acquire(cfg.Node.Kind, cfg.Node.Identity)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Release()
			}
			e, err := New(context.Background(), cfg, Options{CardPath: filepath.Join(root, "capabilities.yaml"), QueueTasks: true})
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			var state, taskID string
			if err := e.db.QueryRow(`SELECT state,task_id FROM execution_requests WHERE request_id='restart-request'`).Scan(&state, &taskID); err != nil {
				t.Fatal(err)
			}
			if borrowed {
				if state != "preparing" || taskID != "" {
					t.Fatalf("borrowed engine recovered owner's work: state=%s task=%s", state, taskID)
				}
			} else if state != "published" || taskID == "" {
				t.Fatalf("owner failed to recover before startup: state=%s task=%s", state, taskID)
			}
		})
	}
}

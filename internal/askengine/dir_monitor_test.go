// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// TestOwnerMeshEngineSweepsTheDirectory pins the wiring that was missing for
// console-only nodes: an engine that owns the row *and* the mesh (`panda web`,
// the sidecar) holds the peer set, so expiring silent peers is its duty. The
// task monitor alone never touched employee_cache, which is why a dead peer's
// row stayed `online` for the console's whole lifetime — and a row that claims
// to be up is exactly what `panda nodes remove` and DELETE /api/nodes/{id}
// refuse to delete.
func TestOwnerMeshEngineSweepsTheDirectory(t *testing.T) {
	isolateIdentityLockRoot(t)
	identity := "owner-mesh-" + t.Name()
	root := t.TempDir()
	cfg := &config.Config{}
	cfg.Storage.DBPath = filepath.Join(root, "panda.db")
	cfg.Storage.MemoryPath = filepath.Join(root, "memory")
	cfg.Storage.ProjectsPath = filepath.Join(root, "projects")
	cfg.Storage.WorkPath = root
	cfg.Node.Name = "owner-mesh"
	cfg.Node.Kind = config.NodeKindVM
	cfg.Node.Identity = identity
	cfg.Network.DiscoveryAddr = "off"

	// A card on disk keeps New off the hardware probe (five seconds on
	// Windows), so the only wait left in this test is the sweep's own tick.
	cardPath := filepath.Join(root, "capabilities.yaml")
	if err := os.WriteFile(cardPath, []byte(`device: owner-mesh
resource_class: Standard
capacity:
  cpu_cores: 8
  ram_gb: 16
  max_concurrent_tasks: 3
`), 0o600); err != nil {
		t.Fatalf("write card: %v", err)
	}

	// Seed the ghost before the engine exists: the sweep's first tick fires
	// inside New, so a row filed afterwards would cost a whole extra tick.
	db, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// The shape a killed peer leaves behind: online, but silent for an hour.
	old := time.Now().Add(-time.Hour).Unix()
	if _, err := db.Exec(
		`INSERT INTO employee_cache (id, name, status, last_seen) VALUES ('ghost', 'ghost', 'online', ?)`,
		old); err != nil {
		t.Fatalf("seed ghost: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	e, err := New(context.Background(), cfg, Options{
		CardPath:   cardPath,
		QueueTasks: true,
		MeshNode:   true,
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
		t.Fatalf("engine with a free lock did not take the row")
	}

	// The sweep runs on the core's five-second tick; the deadline is generous
	// so a loaded CI box does not turn a slow tick into a failure.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		if err := e.db.QueryRow(`SELECT status FROM employee_cache WHERE id='ghost'`).Scan(&status); err != nil {
			t.Fatalf("read ghost: %v", err)
		}
		if status == "offline" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("owner mesh engine never expired a silent peer row")
}

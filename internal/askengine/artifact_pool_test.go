// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// TestEngineAttachesArtifactPool is the production-shape regression for the
// silent broken-pipeline bug: an ephemeral engine's scheduler packed stage
// outputs into a nil pool and reported done with no artifact, so every
// successor stage ran against an empty directory with its predecessor's files
// nowhere in the mesh. The daemon always attached a pool; the ask engine must
// too, and this pins it through the real constructor rather than a struct
// literal.
func TestEngineAttachesArtifactPool(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{}
	cfg.Storage.DBPath = filepath.Join(root, "test.db")
	cfg.Storage.MemoryPath = filepath.Join(root, "memory")
	cfg.Storage.ProjectsPath = filepath.Join(root, "projects")
	cfg.Storage.ArtifactPath = filepath.Join(root, "artifacts")

	e, err := New(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer e.Close()

	sched := e.sched.Load()
	if sched == nil {
		t.Fatal("engine has no scheduler")
	}
	if sched.Artifacts() == nil {
		t.Fatal("engine scheduler has no artifact pool — stage outputs would pack nowhere")
	}
}

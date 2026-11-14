// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/testdb"
)

// TestAdoptRefusesHome pins the root cause of the "every task fails with work
// dir is too broad" incident. Adoption used to mint a project row for any
// marker-bearing directory, and $HOME carries markers on most developer
// machines (~/.claude alone is enough). The row was legal when written; the
// work-dir breadth rule landed later and made every task under it fail at
// creation, with nothing in the error naming the project that caused it.
func TestAdoptRefusesHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skipf("no home directory to test with: %v", err)
	}
	s := newProjectStore(t)
	if name := adoptWorkspaceProject(s, home); name != "" {
		t.Fatalf("adopting %q = %q, want refusal", home, name)
	}
	if list, _ := s.List(); len(list) != 0 {
		t.Fatalf("refused adoption still wrote %d row(s)", len(list))
	}
	if active, _ := s.Active(); active != "" {
		t.Fatalf("refused adoption set active = %q", active)
	}
}

// TestAdoptRefusesAncestorOfHome: the breadth rule rejects every ancestor of
// $HOME too, so adoption must stop at the same boundary rather than mint a row
// the executor will refuse.
func TestAdoptRefusesAncestorOfHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skipf("no home directory to test with: %v", err)
	}
	s := newProjectStore(t)
	if name := adoptWorkspaceProject(s, filepath.Dir(home)); name != "" {
		t.Fatalf("adopting the parent of home = %q, want refusal", name)
	}
}

// TestProjectForDirSkipsTooBroadRow: a legacy row pointing at $HOME must not
// claim every directory beneath it. Before this, launching in any ordinary
// workspace under the home tree resolved to the home project, so the task ran
// with a work dir the executor refuses.
func TestProjectForDirSkipsTooBroadRow(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skipf("no home directory to test with: %v", err)
	}
	db := testdb.OpenFile(t, filepath.Join(t.TempDir(), "test.db"))
	s := projects.NewStore(db)
	// The row is written straight into the table the way a pre-rule build left
	// it; Store.Create refuses this path today.
	if _, err := db.Exec(
		`INSERT INTO projects (name, work_dir, description, created_at, updated_at)
		 VALUES ('home', ?, '', 0, 0)`, home); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	deep := filepath.Join(home, "some-workspace")
	if name, _ := projectForDir(s, deep); name != "" {
		t.Fatalf("legacy home row claimed %q as %q", deep, name)
	}
}

// TestActiveProjectDropsTooBroadWorkDir covers the second half of the home
// regression: a legacy row that is also the ACTIVE project. projectForDir skips
// it (tested above), but activeProject's store.Active() branch returned the raw
// WorkDir, so `panda task add` from $HOME still pinned a refused path and the
// task failed at creation. The row may stay bound — its approval policy still
// applies — but it contributes no work dir.
func TestActiveProjectDropsTooBroadWorkDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skipf("no home directory to test with: %v", err)
	}
	root := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			DBPath:       filepath.Join(root, "test.db"),
			ContextPath:  filepath.Join(root, "context"),
			MemoryPath:   filepath.Join(root, "memory"),
			ProjectsPath: filepath.Join(root, "projects"),
			SkillsPath:   filepath.Join(root, "skills"),
			WorkPath:     filepath.Join(root, "work"),
			ArtifactPath: filepath.Join(root, "artifacts"),
		},
	}
	db, _, err := panelStore(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO projects (name, work_dir, description, created_at, updated_at)
		 VALUES ('legacy-home', ?, '', 0, 0)`, home); err != nil {
		db.Close()
		t.Fatalf("seed legacy row: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO settings (key, value) VALUES ('active_project', 'legacy-home')
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`); err != nil {
		db.Close()
		t.Fatalf("seed active: %v", err)
	}
	db.Close()

	// Run from a directory nothing owns, so the active-project branch (not
	// projectForDir) is what answers.
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	name, workDir := activeProject(cfg)
	if name != "legacy-home" {
		t.Fatalf("active project = %q, want legacy-home to stay bound", name)
	}
	if workDir != "" {
		t.Fatalf("work dir = %q; a too-broad row must contribute none, or every task under it fails at creation", workDir)
	}
}

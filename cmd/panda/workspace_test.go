package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

func newProjectStore(t *testing.T) *projects.Store {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return projects.NewStore(db)
}

func TestLooksLikeWorkspace(t *testing.T) {
	bare := t.TempDir()
	if looksLikeWorkspace(bare) {
		t.Fatalf("bare temp dir must not read as a workspace")
	}
	// Each marker class qualifies on its own.
	for _, marker := range []string{"go.mod", ".git", "package.json", ".pi", "CLAUDE.md"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, marker), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !looksLikeWorkspace(dir) {
			t.Fatalf("dir with %s should read as a workspace", marker)
		}
	}
}

func TestProjectForDirLongestPrefix(t *testing.T) {
	s := newProjectStore(t)
	outer := t.TempDir()
	inner := filepath.Join(outer, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	sibling := outer + "2" // shares a string prefix but is not inside outer
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("outer", outer, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("inner", inner, ""); err != nil {
		t.Fatal(err)
	}

	// cwd at the nested project binds to the deeper workdir, not the parent.
	if name, dir := projectForDir(s, inner); name != "inner" || dir == "" {
		t.Fatalf("inner match = %q %q, want inner project", name, dir)
	}
	// A subdirectory of the nested project still binds to it.
	deep := filepath.Join(inner, "sub", "dir")
	if name, _ := projectForDir(s, deep); name != "inner" {
		t.Fatalf("deep match = %q, want inner", name)
	}
	// cwd in the parent but outside the child binds to the parent.
	if name, _ := projectForDir(s, filepath.Join(outer, "other")); name != "outer" {
		t.Fatalf("outer match = %q, want outer", name)
	}
	// A string-prefix sibling (/tmp/x2 vs /tmp/x) is NOT inside the project.
	if name, _ := projectForDir(s, sibling); name != "" {
		t.Fatalf("sibling must not match, got %q", name)
	}
	// A directory no project owns resolves to nothing.
	if name, _ := projectForDir(s, t.TempDir()); name != "" {
		t.Fatalf("unrelated dir must not match, got %q", name)
	}
}

func TestAdoptWorkspaceProject(t *testing.T) {
	s := newProjectStore(t)
	dir := t.TempDir()

	name := adoptWorkspaceProject(s, dir)
	if name == "" {
		t.Fatal("adoption returned empty name")
	}
	p, err := s.Get(name)
	if err != nil {
		t.Fatalf("get adopted: %v", err)
	}
	if filepath.Clean(p.WorkDir) != filepath.Clean(dir) {
		t.Fatalf("work dir = %q, want %q", p.WorkDir, dir)
	}
	// Adoption marks the project active so later entry points agree.
	if active, _ := s.Active(); active != name {
		t.Fatalf("active = %q, want %q", active, name)
	}

	// Re-adopting the same dir returns the existing project, never a "-2".
	if again := adoptWorkspaceProject(s, dir); again != name {
		t.Fatalf("re-adopt = %q, want %q", again, name)
	}
	list, _ := s.List()
	if len(list) != 1 {
		t.Fatalf("re-adoption created a duplicate: %d projects", len(list))
	}

	// Adopting a second workspace creates a second project and moves active.
	other := t.TempDir()
	name2 := adoptWorkspaceProject(s, other)
	if name2 == "" || name2 == name {
		t.Fatalf("second adoption = %q, want a distinct project", name2)
	}
	if active, _ := s.Active(); active != name2 {
		t.Fatalf("active after second adopt = %q, want %q", active, name2)
	}
}

func TestAdoptWorkspaceProjectAncestor(t *testing.T) {
	s := newProjectStore(t)
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("rootproj", root, ""); err != nil {
		t.Fatal(err)
	}
	// Launching inside a subdirectory of an existing project binds that
	// project — minting "sub" would split one tree into two spaces.
	if got := adoptWorkspaceProject(s, sub); got != "rootproj" {
		t.Fatalf("subdir adopt = %q, want rootproj", got)
	}
	list, _ := s.List()
	if len(list) != 1 {
		t.Fatalf("subdir adoption created a duplicate: %d projects", len(list))
	}
}

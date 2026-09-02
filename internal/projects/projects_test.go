package projects

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/storage"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewStore(db)
}

func TestCreateGetList(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	p, err := s.Create("panda-dev", dir, "the node itself")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.Name != "panda-dev" || p.Description != "the node itself" {
		t.Fatalf("created = %+v", p)
	}
	// The work dir is stored absolute so a task started from any cwd resolves to
	// the same tree.
	if !filepath.IsAbs(p.WorkDir) {
		t.Fatalf("work dir %q is not absolute", p.WorkDir)
	}
	if p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() {
		t.Fatalf("timestamps not set: %+v", p)
	}
	if _, err := s.Create("panda-dev", "", ""); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate create err = %v, want ErrExists", err)
	}
	if _, err := s.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing get err = %v, want ErrNotFound", err)
	}
	if _, err := s.Create("other", "", ""); err != nil {
		t.Fatalf("create other: %v", err)
	}
	list, err := s.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %v (%d), err %v", list, len(list), err)
	}
}

// TestActivePointer covers the reason this table exists: `panda ask` is one-shot,
// so "which project am I in" has to survive between two processes.
func TestActivePointer(t *testing.T) {
	s := newTestStore(t)
	if name, err := s.Active(); err != nil || name != "" {
		t.Fatalf("fresh active = %q, %v; want empty", name, err)
	}
	if err := s.SetActive("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("entering a project that does not exist: err = %v, want ErrNotFound", err)
	}
	if _, err := s.Create("demo", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetActive("demo"); err != nil {
		t.Fatalf("set active: %v", err)
	}
	if name, _ := s.Active(); name != "demo" {
		t.Fatalf("active = %q, want demo", name)
	}
	// Setting again must not conflict on the settings primary key.
	if err := s.SetActive("demo"); err != nil {
		t.Fatalf("re-set active: %v", err)
	}
	if err := s.ClearActive(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if name, _ := s.Active(); name != "" {
		t.Fatalf("active after clear = %q", name)
	}
	// Leaving when not in a project is already true, not an error.
	if err := s.ClearActive(); err != nil {
		t.Fatalf("clear twice: %v", err)
	}
}

// TestActiveForgetsADeletedProject: the pointer must not outlive its project, or
// every later "which project am I in" answers with one that is gone.
func TestActiveForgetsADeletedProject(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("demo", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetActive("demo"); err != nil {
		t.Fatalf("set active: %v", err)
	}
	if _, err := s.db.Exec(`DELETE FROM projects WHERE name = ?`, "demo"); err != nil {
		t.Fatalf("raw delete: %v", err)
	}
	if name, err := s.Active(); err != nil || name != "" {
		t.Fatalf("active = %q, %v; want empty after the project vanished", name, err)
	}
}

// TestRenameCarriesActivePointer is the invariant that makes rename safe: a
// rename that moved the row but left the pointer behind would leave the user
// inside a project that no longer exists.
func TestRenameCarriesActivePointer(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("old", t.TempDir(), "d"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetActive("old"); err != nil {
		t.Fatalf("set active: %v", err)
	}
	if _, err := s.Rename("old", "new"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if name, _ := s.Active(); name != "new" {
		t.Fatalf("active after rename = %q, want new", name)
	}
	if _, err := s.Get("old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old name still resolves: %v", err)
	}
	if _, err := s.Rename("nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rename missing = %v, want ErrNotFound", err)
	}
	if _, err := s.Create("taken", "", ""); err != nil {
		t.Fatalf("create taken: %v", err)
	}
	if _, err := s.Rename("new", "taken"); !errors.Is(err, ErrExists) {
		t.Fatalf("rename onto existing = %v, want ErrExists", err)
	}
}

// TestDeleteLeavesTheTree pins the one thing this package must not do: removing a
// project is bookkeeping, and deleting the directory the user pointed at would be
// the only irreversible operation here.
func TestDeleteLeavesTheTree(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if _, err := s.Create("demo", dir, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetActive("demo"); err != nil {
		t.Fatalf("set active: %v", err)
	}
	if err := s.Delete("demo"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("delete removed the work tree: %v", err)
	}
	if name, _ := s.Active(); name != "" {
		t.Fatalf("active still %q after deleting it", name)
	}
	if err := s.Delete("demo"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice = %v, want ErrNotFound", err)
	}
}

// TestEnsureFromName covers adoption: projects existed as bare memory files
// before this table, so a name from a task or from projects/<name>/ must still
// resolve to a project.
func TestEnsureFromName(t *testing.T) {
	s := newTestStore(t)
	p, err := s.EnsureFromName("legacy")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if p.Name != "legacy" || p.WorkDir != "" {
		t.Fatalf("adopted = %+v, want name legacy and no work dir", p)
	}
	again, err := s.EnsureFromName("legacy")
	if err != nil || !again.CreatedAt.Equal(p.CreatedAt) {
		t.Fatalf("ensure is not idempotent: %+v vs %+v (err %v)", again, p, err)
	}
}

func TestUpdateWorkDirAndDescription(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("demo", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	dir := t.TempDir()
	p, err := s.Update("demo", dir, "now with a tree")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !filepath.IsAbs(p.WorkDir) || p.Description != "now with a tree" {
		t.Fatalf("updated = %+v", p)
	}
	// An empty work dir clears it: that is how a project gives up its tree.
	if p, err = s.Update("demo", "", ""); err != nil || p.WorkDir != "" {
		t.Fatalf("cleared = %+v, err %v", p, err)
	}
	if _, err := s.Update("nope", "", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing = %v, want ErrNotFound", err)
	}
}

func TestValidateName(t *testing.T) {
	bad := []string{"", "   ", ".", "..", "a/b", `a\b`, "a:b", "a*b", "a?b", "a\"b", "a<b", "a>b", "a|b",
		"x\x00y", string(make([]byte, 65))}
	for _, n := range bad {
		if err := ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", n)
		}
	}
	for _, n := range []string{"panda-dev", "项目 一", "a.b", "A_B-1"} {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}
}

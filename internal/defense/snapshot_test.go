package defense

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// writeFiles creates a small tree under dir for snapshot tests.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
}

func TestSnapshotDirAndChanged(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"a.txt":          "hello",
		"sub/b.txt":      "world",
		"src/Navbar.vue": "old",
	})

	before, err := SnapshotDir(dir)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Modify b.txt, add c.txt, delete a.txt. src/Navbar.vue untouched.
	if err := os.WriteFile(filepath.Join(dir, "sub/b.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal(err)
	}

	after, err := SnapshotDir(dir)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	got := before.Changed(after)
	sort.Strings(got)
	want := []string{"a.txt", "c.txt", "sub/b.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Changed() = %v, want %v", got, want)
	}
}

func TestSnapshotDirMissingRootIsEmpty(t *testing.T) {
	s, err := SnapshotDir(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(s.files) != 0 {
		t.Errorf("missing root should snapshot empty, got %d files", len(s.files))
	}
	if got := s.Changed(s); len(got) != 0 {
		t.Errorf("empty snapshot changed against itself = %v, want none", got)
	}
}

func TestSnapshotDirOnFileIsEmpty(t *testing.T) {
	f := filepath.Join(t.TempDir(), "single.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := SnapshotDir(f)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(s.files) != 0 {
		t.Errorf("snapshot of a file should be empty, got %d", len(s.files))
	}
}

func TestChangedNoDiff(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"x": "1"})
	a, _ := SnapshotDir(dir)
	b, _ := SnapshotDir(dir)
	if got := a.Changed(b); len(got) != 0 {
		t.Errorf("identical snapshots changed = %v, want none", got)
	}
}

// TestSnapshotPrunesVendoredDirs verifies that dependency/generated trees are
// skipped wholesale: an agent vendoring or refreshing node_modules mid-run
// produces no drift entries, while drift in real source still registers.
func TestSnapshotPrunesVendoredDirs(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"src/app.js":                "code",
		"node_modules/dep/index.js": "vendored-1",
		"vendor/lib/x.go":           "vendored-2",
		"__pycache__/m.pyc":         "bytecode",
		".git/HEAD":                 "ref: refs/heads/main",
	})

	before, err := SnapshotDir(dir)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if _, ok := before.files["node_modules/dep/index.js"]; ok {
		t.Fatalf("node_modules file recorded in snapshot; pruned dirs must be skipped")
	}
	// .git is deliberately NOT pruned — history rewrites are drift worth flagging.
	if _, ok := before.files[".git/HEAD"]; !ok {
		t.Fatalf(".git file missing from snapshot; .git must not be pruned")
	}

	// Rewriting vendored content mid-run registers nothing.
	if err := os.WriteFile(filepath.Join(dir, "node_modules/dep/index.js"), []byte("vendored-2"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := SnapshotDir(dir)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if got := before.Changed(after); len(got) != 0 {
		t.Errorf("vendored-dir change read as drift: %v, want none", got)
	}
}

// TestSnapshotIgnoresSQLiteJournal verifies that SQLite journal files (-wal/-shm)
// are excluded from the snapshot: they are transient host machine state written
// by any live SQLite connection, so a changing WAL must not read as agent drift.
func TestSnapshotIgnoresSQLiteJournal(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"openpanda.db":     "db",
		"openpanda.db-wal": "wal-1",
		"openpanda.db-shm": "shm",
		"real/work.txt":    "todo",
	})

	before, err := SnapshotDir(dir)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	// Only the journal file changes; the real work file is untouched.
	if err := os.WriteFile(filepath.Join(dir, "openpanda.db-wal"), []byte("wal-2-changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := SnapshotDir(dir)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if got := before.Changed(after); len(got) != 0 {
		t.Errorf("journal change read as drift: %v, want none", got)
	}

	// A real file change is still detected.
	if err := os.WriteFile(filepath.Join(dir, "real/work.txt"), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	after2, _ := SnapshotDir(dir)
	if got := before.Changed(after2); !reflect.DeepEqual(got, []string{"real/work.txt"}) {
		t.Errorf("real change = %v, want [real/work.txt]", got)
	}
}

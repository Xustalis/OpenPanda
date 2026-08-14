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

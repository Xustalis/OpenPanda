//go:build !windows

package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAddRemovePATHIdempotent pins the marked-block contract of unix PATH
// persistence: the block is appended to the shell rc once (never duplicated
// by re-runs), user content survives around it, the doctor probe sees it,
// and removal strips exactly the block. Windows persistence lives in the
// registry instead (path_windows.go), so this test builds only where the
// rc-file path does.
func TestAddRemovePATHIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", "")

	rc := filepath.Join(home, ".zshrc")
	orig := "# user content\nexport EDITOR=vim\n"
	if err := os.WriteFile(rc, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(home, ".local", "bin")
	written, err := AddToPATH(dir)
	if err != nil || len(written) != 1 || written[0] != rc {
		t.Fatalf("AddToPATH = %v, %v", written, err)
	}
	data, _ := os.ReadFile(rc)
	s := string(data)
	if !strings.Contains(s, markerBegin) || !strings.Contains(s, dir) {
		t.Fatalf("rc missing marker/export: %q", s)
	}
	if !strings.Contains(s, "export EDITOR=vim") {
		t.Fatal("user content lost")
	}

	// Second run must not duplicate the block.
	if _, err := AddToPATH(dir); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(rc)
	if got := strings.Count(string(data), markerBegin); got != 1 {
		t.Fatalf("marker duplicated %d times", got)
	}

	// Doctor's persistence probe must see it; removal must restore the file.
	if got := PathPersistedAt(dir); len(got) != 1 {
		t.Fatalf("PathPersistedAt = %v, want [%s]", got, rc)
	}
	changed, err := RemovePATHPersistence(dir)
	if err != nil || len(changed) != 1 {
		t.Fatalf("RemovePATHPersistence = %v, %v", changed, err)
	}
	data, _ = os.ReadFile(rc)
	if strings.Contains(string(data), markerBegin) {
		t.Fatalf("marker survived removal: %q", data)
	}
	if !strings.Contains(string(data), "export EDITOR=vim") {
		t.Fatal("user content lost on removal")
	}
}

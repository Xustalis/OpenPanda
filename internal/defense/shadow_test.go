package defense

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// §5.2: only files under the declared scope roots are parked — the shadow
// preserves what arbitration fought over, not the whole machine.
func TestSaveShadowScopedOnly(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "src/a.go", "agent edit")
	write(t, dir, "src/b.go", "agent edit")
	write(t, dir, "docs/readme.md", "unrelated")
	write(t, dir, "node_modules/pkg/x.js", "vendored")
	if err := SaveShadow(dir, "t1", []string{"src"}); err != nil {
		t.Fatalf("SaveShadow: %v", err)
	}
	root := ShadowRoot(dir, "t1")
	if got := read(t, root, "src/a.go"); got != "agent edit" {
		t.Fatalf("shadow a.go = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "docs")); !os.IsNotExist(err) {
		t.Fatal("unscoped docs/ leaked into shadow")
	}
	if _, err := os.Stat(filepath.Join(root, "node_modules")); !os.IsNotExist(err) {
		t.Fatal("pruned dir leaked into shadow")
	}
}

// Merge: untouched paths return verbatim; paths the winner rewrote stay with
// the winner and are reported as conflicts.
func TestMergeShadowRestoresAndConflicts(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "src/ours.go", "ours v1")
	write(t, dir, "src/mine.go", "mine v1")
	write(t, dir, "src/deleted.go", "winner will delete")
	if err := SaveShadow(dir, "t1", []string{"src"}); err != nil {
		t.Fatalf("SaveShadow: %v", err)
	}
	// The winner's turn: rewrite ours.go, leave mine.go, delete deleted.go.
	write(t, dir, "src/ours.go", "winner v2")
	if err := os.Remove(filepath.Join(dir, "src", "deleted.go")); err != nil {
		t.Fatal(err)
	}
	restored, conflicts, err := MergeShadow(dir, "t1")
	if err != nil {
		t.Fatalf("MergeShadow: %v", err)
	}
	if len(restored) != 1 || restored[0] != "src/deleted.go" {
		t.Fatalf("restored = %v, want [src/deleted.go]", restored)
	}
	if len(conflicts) != 1 || conflicts[0] != "src/ours.go" {
		t.Fatalf("conflicts = %v, want [src/ours.go]", conflicts)
	}
	if got := read(t, dir, "src/ours.go"); got != "winner v2" {
		t.Fatalf("conflicted file = %q, want winner's bytes kept", got)
	}
	if got := read(t, dir, "src/mine.go"); got != "mine v1" {
		t.Fatalf("untouched file = %q", got)
	}
	// The shadow is consumed: a second merge must not replay.
	if _, err := os.Stat(ShadowRoot(dir, "t1")); !os.IsNotExist(err) {
		t.Fatal("shadow dir not consumed after merge")
	}
}

func TestMergeShadowNoShadowIsNoop(t *testing.T) {
	dir := t.TempDir()
	restored, conflicts, err := MergeShadow(dir, "ghost")
	if err != nil || len(restored) != 0 || len(conflicts) != 0 {
		t.Fatalf("empty merge = %v %v %v", restored, conflicts, err)
	}
}

func TestSaveShadowNoScopeIsNoop(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.txt", "x")
	if err := SaveShadow(dir, "t1", nil); err != nil {
		t.Fatalf("SaveShadow: %v", err)
	}
	if _, err := os.Stat(ShadowRoot(dir, "t1")); !os.IsNotExist(err) {
		t.Fatal("scope-less task parked a shadow anyway")
	}
}

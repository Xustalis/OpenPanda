package artifact

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPackDirExceptSkipsNamedDirs pins the delegation attach contract:
// directory names in the skip set are pruned before walk, so their content is
// neither counted nor packed — .git (a credential carrier), node_modules and
// vendor (derived, dwarfing the source) must never reach the wire.
func TestPackDirExceptSkipsNamedDirs(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "pool"))
	root := writeTree(t, map[string]string{
		"app/main.go":               "package main\n",
		".git/HEAD":                 "ref: refs/heads/main",
		".git/config":               "url = https://token@x/r.git",
		"node_modules/pkg/index.js": "x",
		"vendor/lib/lib.go":         "package lib\n",
	})

	m, err := s.PackDirExcept(root, worktreeLikeSkip(), 0)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	out := t.TempDir()
	if _, err := s.Extract(m.Hash, out); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "app", "main.go")); err != nil {
		t.Fatalf("source file missing from pack: %v", err)
	}
	for _, skip := range []string{".git", "node_modules", "vendor"} {
		if _, err := os.Stat(filepath.Join(out, skip)); !os.IsNotExist(err) {
			t.Fatalf("skipped dir %s packed", skip)
		}
	}
}

// worktreeLikeSkip mirrors the set callers pass for worktree attach; the test
// keeps its own copy so a removed entry can't silently go untested.
func worktreeLikeSkip() map[string]bool {
	return map[string]bool{".git": true, "node_modules": true, "vendor": true}
}

// TestPackDirExceptHonorsCallLimit verifies the per-call byte bound tightens
// the store's own cap: a tree over the caller's limit refuses rather than
// silently truncating, which is what a delegation must do rather than ship a
// half-packed repo.
func TestPackDirExceptHonorsCallLimit(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "pool"))
	root := writeTree(t, map[string]string{
		"a.txt": string(make([]byte, 1024)),
		"b.txt": "small",
	})
	if _, err := s.PackDirExcept(root, nil, 512); err == nil {
		t.Fatalf("pack over call limit succeeded")
	}
	if _, err := s.PackDirExcept(root, nil, 0); err != nil {
		t.Fatalf("pack at store limit failed: %v", err)
	}
}

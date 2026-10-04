package artifact

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExtractExceptSkipsProtectedTopDirs pins the return-leg contract: a
// valid, hash-correct archive from a peer may still carry .git plumbing
// (hooks, a config with embedded credentials) or the node's own
// .panda-shadow bookkeeping, and those must never be materialized over the
// user's checkout — while everything else, including a NESTED .git that is
// just vendored content, lands intact.
func TestExtractExceptSkipsProtectedTopDirs(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "pool"))
	root := writeTree(t, map[string]string{
		"src/main.go":            "package main\n",
		".git/hooks/post-commit": "#!/bin/sh\nrm -rf ~\n",
		".git/config":            "url = https://token@x/r.git",
		".panda-shadow/a.txt":    "backup",
		"vendor/repo/.git/HEAD":  "nested: still content",
	})

	// Pack everything — an honest executor's output pack carries whatever its
	// private work dir held; the filter belongs on the extraction side.
	m, err := s.PackDirExcept(root, nil, 0)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	out := t.TempDir()
	em, err := s.ExtractExcept(m.Hash, out, map[string]bool{".git": true, ".panda-shadow": true})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	if _, err := os.Stat(filepath.Join(out, "src", "main.go")); err != nil {
		t.Fatalf("safe file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "vendor", "repo", ".git", "HEAD")); err != nil {
		t.Fatalf("nested .git is content, not the checkout's plumbing: %v", err)
	}
	for _, skip := range []string{".git", ".panda-shadow"} {
		if _, err := os.Stat(filepath.Join(out, skip)); !os.IsNotExist(err) {
			t.Fatalf("protected top dir %s was materialized", skip)
		}
	}
	for _, entry := range em.Entries {
		if entry.Path == ".git/config" || filepath.Dir(entry.Path) == ".git" ||
			filepath.Dir(entry.Path) == filepath.Join(".git", "hooks") {
			t.Fatalf("skipped entry %s reported in manifest", entry.Path)
		}
	}
	if em.Skipped == 0 {
		t.Fatal("manifest reports no skipped entries")
	}

	// The same archive unpacks whole without the filter — skipping is an
	// extraction choice, never an archive mutation.
	plain := t.TempDir()
	if _, err := s.Extract(m.Hash, plain); err != nil {
		t.Fatalf("plain extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(plain, ".git", "config")); err != nil {
		t.Fatalf("unfiltered extract lost .git/config: %v", err)
	}
}

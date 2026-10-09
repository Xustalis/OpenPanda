// SPDX-License-Identifier: AGPL-3.0-or-later

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

// TestPackDirExceptPathsPrunesHostState pins the absolute-path prune set: a
// work tree rooted at a checkout that also holds the node's live SQLite file
// must not ship it — the db's settings table holds the node private key, and
// the receiver's extract would otherwise overwrite a live database,
// orphaning the daemon's open handle on a dead inode.
func TestPackDirExceptPathsPrunesHostState(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "pool"))
	root := writeTree(t, map[string]string{
		"app/main.go":        "package main\n",
		"data/node.db":       "sqlite-bytes",
		"data/node.db-wal":   "wal-bytes",
		"data/daemon.pid":    "12345",
		"data/context/x.bin": "ctx",
	})

	// The whole data/ dir counts as host state — as it does when db_path
	// points inside the packed tree.
	prune := []string{filepath.Join(root, "data"), filepath.Join(root, "nonexistent")}
	m, err := s.PackDirExceptPaths(root, nil, prune, 0)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	out := t.TempDir()
	em, err := s.Extract(m.Hash, out)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "app", "main.go")); err != nil {
		t.Fatalf("source file missing from pack: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "data")); !os.IsNotExist(err) {
		t.Fatalf("host state dir packed: %v", err)
	}
	for _, e := range em.Entries {
		if e.Path == "data" || len(e.Path) > 5 && e.Path[:5] == "data/" {
			t.Fatalf("host state entry packed: %v", e)
		}
	}

	// File-level pruning: when only the db file is listed (state dir == the
	// tree root, which cannot be pruned wholesale), the file and its WAL
	// sidecar still stay home.
	m2, err := s.PackDirExceptPaths(root, nil,
		[]string{filepath.Join(root, "data", "node.db"), filepath.Join(root, "data", "node.db-wal")}, 0)
	if err != nil {
		t.Fatalf("pack2: %v", err)
	}
	out2 := t.TempDir()
	if _, err := s.Extract(m2.Hash, out2); err != nil {
		t.Fatalf("extract2: %v", err)
	}
	for _, gone := range []string{"data/node.db", "data/node.db-wal"} {
		if _, err := os.Stat(filepath.Join(out2, gone)); !os.IsNotExist(err) {
			t.Fatalf("live state file %s packed", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(out2, "data", "daemon.pid")); err != nil {
		t.Fatalf("unlisted file should still pack: %v", err)
	}
}

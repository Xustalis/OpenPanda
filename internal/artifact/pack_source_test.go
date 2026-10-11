// SPDX-License-Identifier: AGPL-3.0-or-later

package artifact

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestSourcePackPrivacyBaseline pins the no-credential contract on source
// packs: a tree holding identity keys, login files and OS-private dirs must
// pack WITHOUT them — the project-workdir-is-home case, where nothing else
// stands between ~/.ssh and the wire. Output packs keep no such filter, so
// the same tree through PackDir ships everything.
func TestSourcePackPrivacyBaseline(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "pool"))
	root := writeTree(t, map[string]string{
		"app/main.go":          "package main\n",
		".ssh/id_ed25519":      "PRIVATE KEY",
		".ssh/config":          "Host *",
		".aws/credentials":     "[default]",
		".gnupg/secring.gpg":   "gpg",
		".config/gh/hosts.yml": "oauth_token: x",
		".Trash/old.db":        "trash",
		".env":                 "API_KEY=secret",
		".env.local":           "TOKEN=secret",
		".envrc":               "export X=1",
		".netrc":               "machine x login y password z",
		"id_rsa":               "loose private key",
		"secrets/keep.txt":     "a dir literally named secrets is not skipped: only .secrets is",
		"vendor/lib/lib.go":    "package lib\n",
	})

	m, err := s.PackSourceDirExceptPaths(root, map[string]bool{"vendor": true}, nil, 0)
	if err != nil {
		t.Fatalf("source pack: %v", err)
	}
	out := t.TempDir()
	em, err := s.Extract(m.Hash, out)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "app", "main.go")); err != nil {
		t.Fatalf("source file missing from pack: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "secrets", "keep.txt")); err != nil {
		t.Fatalf("non-private 'secrets' dir pruned: %v", err)
	}
	for _, gone := range []string{
		".ssh", ".aws", ".gnupg", ".Trash",
		".env", ".env.local", ".envrc", ".netrc", "id_rsa", "vendor",
	} {
		if _, err := os.Stat(filepath.Join(out, gone)); !os.IsNotExist(err) {
			t.Fatalf("private path %s shipped in a source pack", gone)
		}
	}
	// The tree is NOT the user's home: home-shaped names (.config, .local,
	// AppData, Library) are ordinary project directories here and must pack —
	// pruning them by name at any depth would silently drop a project's own
	// .config/. They are only credential-shaped when the pack root IS $HOME
	// (see TestSourcePackHomeBaseline below).
	if _, err := os.Stat(filepath.Join(out, ".config", "gh", "hosts.yml")); err != nil {
		t.Fatalf("project .config dir pruned in a non-home tree: %v", err)
	}
	_ = em

	// The same tree through the OUTPUT pack path ships everything — an
	// executor's product is returned verbatim, privacy filter or not.
	om, err := s.PackDirExceptPaths(root, nil, nil, 0)
	if err != nil {
		t.Fatalf("output pack: %v", err)
	}
	out2 := t.TempDir()
	if _, err := s.Extract(om.Hash, out2); err != nil {
		t.Fatalf("extract output: %v", err)
	}
	for _, keep := range []string{".env", ".ssh/id_ed25519", ".aws/credentials"} {
		if _, err := os.Stat(filepath.Join(out2, keep)); err != nil {
			t.Fatalf("output pack withheld %s — outputs must round-trip verbatim", keep)
		}
	}
}

// TestSourcePackLenientOnUnreadable pins the failure contract that replaced
// fail-fast: one unreadable entry must not sink the whole pack — it lands in
// Manifest.PackSkipped instead, which is how a project rooted at a home dir
// (with a permission-denied ~/.Trash or a locked system path) still ships.
func TestSourcePackLenientOnUnreadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 000 is not a reliable read barrier on Windows")
	}
	s := NewStore(filepath.Join(t.TempDir(), "pool"))
	root := writeTree(t, map[string]string{
		"app/main.go":       "package main\n",
		"locked/secret.txt": "cannot read this",
	})
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	// Lenient source pack: succeeds, names the unreadable entry.
	m, err := s.PackSourceDirExceptPaths(root, nil, nil, 0)
	if err != nil {
		t.Fatalf("lenient pack failed: %v", err)
	}
	found := false
	for _, p := range m.PackSkipped {
		if p == "locked" || p == "locked/secret.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("PackSkipped = %v, want the locked path named", m.PackSkipped)
	}
	out := t.TempDir()
	if _, err := s.Extract(m.Hash, out); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "app", "main.go")); err != nil {
		t.Fatalf("readable half of the tree did not ship: %v", err)
	}

	// The strict output pack still refuses: a partial executor result is a
	// loud failure, never a silently truncated successor input.
	if _, err := s.PackDirExceptPaths(root, nil, nil, 0); err == nil {
		t.Fatalf("strict pack succeeded over an unreadable entry")
	}
}

// TestSourcePackHomeBaseline covers the tier the project-tree test cannot:
// when the pack root IS the user's home, home-shaped names (.config, .local,
// AppData, Library) are credential stores, not project directories, and the
// source pack must drop them — the belt to the ambient-tree guard's
// suspenders. HOME/USERPROFILE are repointed at the tree so the test runs on
// a controlled root.
func TestSourcePackHomeBaseline(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "pool"))
	home := writeTree(t, map[string]string{
		"work/app/main.go":         "package main\n",
		".config/gh/hosts.yml":     "oauth_token: x",
		".local/share/keyring/db":  "keys",
		"AppData/Roaming/app/cred": "cred",
		"Library/Keychains/db":     "keys",
	})
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	m, err := s.PackSourceDirExceptPaths(home, nil, nil, 0)
	if err != nil {
		t.Fatalf("home source pack: %v", err)
	}
	out := t.TempDir()
	if _, err := s.Extract(m.Hash, out); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "work", "app", "main.go")); err != nil {
		t.Fatalf("project subtree under home did not ship: %v", err)
	}
	for _, gone := range []string{".config", ".local", "AppData", "Library"} {
		if _, err := os.Stat(filepath.Join(out, gone)); !os.IsNotExist(err) {
			t.Fatalf("home-private path %s shipped in a source pack", gone)
		}
	}
}

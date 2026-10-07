// SPDX-License-Identifier: AGPL-3.0-or-later

package defense

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestHashDirStableAcrossMtime(t *testing.T) {
	root := t.TempDir()
	write := func(rel, data string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "package a\n")
	write("sub/b.go", "package b\n")

	h1, n1, err := HashDir(root, 100)
	if err != nil || h1 == "" || n1 != 2 {
		t.Fatalf("h1=%q n=%d err=%v", h1, n1, err)
	}
	// Touch-only rewrite: same content, new mtime → identical hash.
	write("a.go", "package a\n")
	h2, n2, _ := HashDir(root, 100)
	if h1 != h2 || n2 != 2 {
		t.Fatalf("mtime-only change moved the hash: %s -> %s", h1, h2)
	}
	// Content change moves the hash.
	write("a.go", "package a // edited\n")
	h3, _, _ := HashDir(root, 100)
	if h3 == h1 {
		t.Fatal("content change did not move the hash")
	}
	// Add a file.
	write("c.go", "package c\n")
	h4, n4, _ := HashDir(root, 100)
	if h4 == h1 || n4 != 3 {
		t.Fatalf("added file: h=%q n=%d", h4, n4)
	}
}

func TestHashDirDeterministicOrder(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"z.txt", "a.txt", "m.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h1, _, _ := HashDir(root, 100)
	h2, _, _ := HashDir(root, 100)
	if h1 != h2 {
		t.Fatal("same tree hashed differently")
	}
}

func TestHashDirMaxFiles(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(root, string(rune('a'+i))+".txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h, n, err := HashDir(root, 3)
	if err != nil {
		t.Fatal(err)
	}
	if h != "" || n <= 3 {
		t.Fatalf("expected over-cap bail, got h=%q n=%d", h, n)
	}
}

func TestHashDirMissingAndEmpty(t *testing.T) {
	if h, n, err := HashDir(filepath.Join(t.TempDir(), "nope"), 10); err != nil || h != "" || n != 0 {
		t.Fatalf("missing dir: h=%q n=%d err=%v", h, n, err)
	}
	if h, n, err := HashDir(t.TempDir(), 10); err != nil || h != "" || n != 0 {
		t.Fatalf("empty dir: h=%q n=%d err=%v", h, n, err)
	}
}

// initGitRepo creates a repo with one committed file so the fingerprint has
// a real HEAD to mix in. Skips when git is unavailable.
func initGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "tracked.txt")
	run("commit", "-qm", "init")
	return root
}

// TestGitFingerprintTracksContent verifies the fallback hash: identical
// states hash identically, and editing a tracked file — or reverting that
// edit — moves (and restores) the fingerprint, which is exactly what the
// oscillation window relies on to catch an A→B→A regression.
func TestGitFingerprintTracksContent(t *testing.T) {
	root := initGitRepo(t)
	ctx := context.Background()

	h1, err := GitFingerprint(ctx, root)
	if err != nil || h1 == "" {
		t.Fatalf("fingerprint: %q %v", h1, err)
	}
	// Same state → same fingerprint.
	h2, _ := GitFingerprint(ctx, root)
	if h1 != h2 {
		t.Fatal("identical tree fingerprinted differently")
	}
	// Edit a tracked file → fingerprint moves.
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h3, _ := GitFingerprint(ctx, root)
	if h3 == h1 {
		t.Fatal("tracked edit did not move the fingerprint")
	}
	// Revert → back to the original fingerprint (the A→B→A signal).
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h4, _ := GitFingerprint(ctx, root)
	if h4 != h1 {
		t.Fatalf("reverted tree did not restore the fingerprint: %s != %s", h4, h1)
	}
	// An untracked file participates by name.
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h5, _ := GitFingerprint(ctx, root)
	if h5 == h1 {
		t.Fatal("new untracked file did not move the fingerprint")
	}
}

// TestGitFingerprintNonRepo verifies the sentinel the caller branches on.
func TestGitFingerprintNonRepo(t *testing.T) {
	if _, err := GitFingerprint(context.Background(), t.TempDir()); !errors.Is(err, errNotGitRepo) {
		t.Fatalf("err = %v, want errNotGitRepo", err)
	}
}

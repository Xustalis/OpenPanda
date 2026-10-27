// SPDX-License-Identifier: AGPL-3.0-or-later

package defense

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
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

// TestHashDirMatchesManualComposition pins the digest format HashDir produces:
// a sorted (rel path, NUL, content hash) sequence folded into one SHA-256. It
// exists because the file read loop was rewritten (io.CopyBuffer → a shared
// buffer, see hashFileInto) and any drift there would silently change every
// fingerprint the oscillation window compares — including the ones persisted by
// an earlier process. The 3 MiB file spans several buffer reads, so a loop that
// dropped a chunk or hashed a partial read fails here rather than in production.
func TestHashDirMatchesManualComposition(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"a.txt":       "alpha",
		"sub/b.txt":   strings.Repeat("beta", 1000),
		"sub/big.bin": strings.Repeat("x", 3<<20),
		"empty.txt":   "",
	}
	for rel, data := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	names := make([]string, 0, len(files))
	for rel := range files {
		names = append(names, rel)
	}
	sort.Strings(names)
	outer := sha256.New()
	for _, rel := range names {
		inner := sha256.New()
		inner.Write([]byte(files[rel]))
		outer.Write([]byte(rel))
		outer.Write([]byte{0})
		outer.Write(inner.Sum(nil))
	}
	want := hex.EncodeToString(outer.Sum(nil))

	got, n, err := HashDir(root, 100)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(files) {
		t.Fatalf("nfiles = %d, want %d", n, len(files))
	}
	if got != want {
		t.Fatalf("digest drifted from the documented composition:\n got %s\nwant %s", got, want)
	}
}

// benchTree materializes files files of size bytes under a temp root. Contents
// differ per file so the tree looks like a source checkout rather than a pile of
// duplicates.
func benchTree(b *testing.B, files, size int) string {
	b.Helper()
	root := b.TempDir()
	blob := make([]byte, size)
	for i := range blob {
		blob[i] = byte(i)
	}
	for i := 0; i < files; i++ {
		blob[0], blob[1] = byte(i), byte(i>>8)
		if err := os.WriteFile(filepath.Join(root, "f"+strconv.Itoa(i)+".txt"), blob, 0o644); err != nil {
			b.Fatal(err)
		}
	}
	return root
}

// allocBytes returns how many bytes f allocated, skipping what the measurement
// harness itself costs.
func allocBytes(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// BenchmarkHashDir watches the cost of the per-round oscillation check. The
// figure that matters is KB/file, reported as a custom metric: it holds across
// tree sizes, while B/op grows with the file count. Before the shared-buffer
// read loop it sat near 34 KB/file (io.CopyBuffer discarding the 1 MB buffer
// and os.File.WriteTo allocating 32 KB per call), i.e. ~165 MB for a 5000-file
// project on every supervision round; it now stays under 2 KB/file.
func BenchmarkHashDir(b *testing.B) {
	for _, tc := range []struct {
		name  string
		files int
	}{
		{"1000f_4KB", 1000},
		{"5000f_4KB", 5000},
	} {
		b.Run(tc.name, func(b *testing.B) {
			root := benchTree(b, tc.files, 4<<10)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				h, n, err := HashDir(root, tc.files+1)
				if err != nil || n != tc.files || h == "" {
					b.Fatalf("HashDir: n=%d h=%q err=%v", n, h, err)
				}
			}
			b.StopTimer()
			// One extra call for the per-file figure: dividing B/op by the file
			// count would fold in the fixed 1 MB buffer, which is exactly the
			// part that should NOT look like a per-file cost.
			got := allocBytes(func() { _, _, _ = HashDir(root, tc.files+1) })
			b.ReportMetric(float64(got)/float64(tc.files)/1024, "KB/file")
		})
	}
}

package artifact

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// writeTree materializes a name->content map under a fresh temp dir.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

func packTo(t *testing.T, root string) (Manifest, []byte) {
	t.Helper()
	var buf bytes.Buffer
	m, err := Pack(root, &buf)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	return m, buf.Bytes()
}

// TestPackIsDeterministic is the property the whole data plane rests on: the
// hash names the artifact in storage and verifies it after transfer, so two
// identical trees must pack to identical bytes. mtimes differ between the two
// copies here on purpose — that is the failure mode being pinned.
func TestPackIsDeterministic(t *testing.T) {
	files := map[string]string{
		"model.bin":        "weights",
		"src/train.py":     "print('train')",
		"src/lib/util.py":  "pass",
		"docs/README.md":   "# notes",
		"empty/.gitkeep":   "",
		"zzz-last-by-sort": "z",
	}
	a := writeTree(t, files)
	b := writeTree(t, files)
	// Skew one tree's timestamps: a real second copy never shares mtimes.
	future := time.Now().Add(48 * time.Hour)
	_ = filepath.Walk(b, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, future, future)
	})

	ma, bytesA := packTo(t, a)
	mb, bytesB := packTo(t, b)

	if ma.Hash != mb.Hash {
		t.Fatalf("hash differs across identical trees: %s vs %s", ma.Hash, mb.Hash)
	}
	if !bytes.Equal(bytesA, bytesB) {
		t.Fatalf("packed bytes differ across identical trees")
	}
	// Packing the same tree twice must also be stable.
	again, _ := packTo(t, a)
	if again.Hash != ma.Hash {
		t.Fatalf("second pack of the same tree changed the hash: %s vs %s", again.Hash, ma.Hash)
	}
	if len(ma.Entries) != len(files) {
		t.Fatalf("manifest has %d entries, want %d", len(ma.Entries), len(files))
	}
	for i := 1; i < len(ma.Entries); i++ {
		if ma.Entries[i-1].Path >= ma.Entries[i].Path {
			t.Fatalf("manifest entries not sorted: %q before %q",
				ma.Entries[i-1].Path, ma.Entries[i].Path)
		}
	}
}

// TestPackHashChangesWithContent guards against a determinism fix that went too
// far — a hash that ignores content would be perfectly stable and useless.
func TestPackHashChangesWithContent(t *testing.T) {
	a := writeTree(t, map[string]string{"out.txt": "one"})
	b := writeTree(t, map[string]string{"out.txt": "two"})
	ma, _ := packTo(t, a)
	mb, _ := packTo(t, b)
	if ma.Hash == mb.Hash {
		t.Fatalf("different content packed to the same hash %s", ma.Hash)
	}

	// A rename with identical bytes must also change the hash: layout is part of
	// the artifact, since a successor stage reads specific paths.
	c := writeTree(t, map[string]string{"renamed.txt": "one"})
	mc, _ := packTo(t, c)
	if mc.Hash == ma.Hash {
		t.Fatalf("a renamed file packed to the same hash %s", ma.Hash)
	}
}

// TestRoundTripRestoresTree is the end-to-end contract: what one node packs,
// another node reconstructs byte for byte, and both agree on the hash.
func TestRoundTripRestoresTree(t *testing.T) {
	files := map[string]string{
		"model.bin":        "weights\x00\x01binary",
		"src/train.py":     "print('train')",
		"nested/a/b/c.txt": "deep",
	}
	root := writeTree(t, files)
	if err := os.Chmod(filepath.Join(root, "src/train.py"), 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	packed, raw := packTo(t, root)
	dst := filepath.Join(t.TempDir(), "restored")
	unpacked, err := Unpack(bytes.NewReader(raw), dst)
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if unpacked.Hash != packed.Hash {
		t.Fatalf("unpack hash %s != pack hash %s", unpacked.Hash, packed.Hash)
	}
	if unpacked.Size != packed.Size {
		t.Fatalf("unpack size %d != pack size %d", unpacked.Size, packed.Size)
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dst, "src/train.py"))
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if fi.Mode().Perm() != 0o755 {
			t.Fatalf("mode = %v, want 0755: the executable bit must survive the trip", fi.Mode().Perm())
		}
	}

	// Repacking the restored tree reproduces the hash: the round trip is a fixed
	// point, so an artifact can be re-forwarded down a multi-hop chain.
	again, _ := packTo(t, dst)
	if again.Hash != packed.Hash {
		t.Fatalf("repack of the restored tree = %s, want %s", again.Hash, packed.Hash)
	}
}

// TestPackSkipsSymlinks: a link is a reference, and the node an artifact travels
// to has no reason to hold what it references. Following one would silently pull
// in data from outside the packed root.
func TestPackSkipsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	root := writeTree(t, map[string]string{"real.txt": "content"})
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("do not pack me"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	m, raw := packTo(t, root)
	if len(m.Entries) != 1 || m.Entries[0].Path != "real.txt" {
		t.Fatalf("manifest = %+v, want only real.txt", m.Entries)
	}
	if bytes.Contains(mustGunzip(t, raw), []byte("do not pack me")) {
		t.Fatalf("packed archive contains data from outside the root")
	}
}

// mustGunzip decompresses raw so a test can assert on the tar bytes.
func mustGunzip(t *testing.T, raw []byte) []byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	defer gz.Close()
	out, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("read gunzip: %v", err)
	}
	return out
}

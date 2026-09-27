package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeZip builds a .zip from name→content pairs with an optional mode; names
// are used verbatim so a test can ship entries zip would never emit itself.
func writeZip(t *testing.T, path string, entries map[string][]byte, modes map[string]os.FileMode) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, data := range entries {
		hdr := &zip.FileHeader{Name: name, Method: zip.Store}
		if m, ok := modes[name]; ok {
			hdr.SetMode(m)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestExtractRejectsNonLayoutEntries pins the strict contract: an entry that
// is not inside the openpanda/ tree — foreign top-level, absolute, or a ".."
// traversal — refuses the archive instead of being silently skipped, the same
// verdict artifact.Unpack gives.
func TestExtractRejectsNonLayoutEntries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry string
	}{
		{"foreign top-level", "evil/payload"},
		{"absolute path", "/etc/cron.d/payload"},
		{"dot-dot escape", "openpanda/../../escape"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			archive := filepath.Join(dir, "rel.tar.gz")
			writeTarGz(t, archive, []*tar.Header{
				{Name: "openpanda/", Typeflag: tar.TypeDir, Mode: 0o755},
				{Name: tc.entry, Typeflag: tar.TypeReg, Mode: 0o644, Size: 4},
			})
			root := filepath.Join(dir, "out")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := untargz(archive, root); err == nil {
				t.Fatalf("untargz silently skipped %q", tc.entry)
			}
		})
	}
}

// TestExtractSkipsMacOSMetadata pins the AppleDouble/`__MACOSX` exception to
// the strict-layout rule: bsdtar on macOS encodes file xattrs as "._name"
// shadow members (a top-level "._openpanda" included), and ditto-made zips
// carry a "__MACOSX/" tree. They hold no payload, so extraction skips them
// instead of writing junk files or refusing the archive outright.
func TestExtractSkipsMacOSMetadata(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "rel.tar.gz")
	writeTarGz(t, archive, []*tar.Header{
		{Name: "._openpanda", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4},
		{Name: "openpanda/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "openpanda/._bin", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4},
		{Name: "openpanda/bin/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "openpanda/bin/panda", Typeflag: tar.TypeReg, Mode: 0o755, Size: 2},
		{Name: "__MACOSX/openpanda/._bin", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4},
	})
	root := filepath.Join(dir, "out")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := untargz(archive, root); err != nil {
		t.Fatalf("untargz refused macOS metadata: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "bin", "panda")); err != nil {
		t.Fatalf("real payload missing after extract: %v", err)
	}
	for _, junk := range []string{"._openpanda", "bin/._bin", "__MACOSX"} {
		if _, err := os.Stat(filepath.Join(root, junk)); !os.IsNotExist(err) {
			t.Errorf("metadata entry %q materialized on disk", junk)
		}
	}
}

// TestUnzipRejectsSymlinkAndTraversal covers the zip half, which previously
// had no type checks at all: a symlink entry and a traversal name must both
// refuse the archive.
func TestUnzipRejectsSymlinkAndTraversal(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "out")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	sym := filepath.Join(dir, "sym.zip")
	writeZip(t, sym, map[string][]byte{
		"openpanda/ok":      []byte("x"),
		"openpanda/link.py": []byte("a.py"),
	}, map[string]os.FileMode{
		"openpanda/link.py": 0o777 | os.ModeSymlink,
	})
	if err := unzip(sym, root); err == nil {
		t.Fatal("unzip accepted a symlink entry")
	}

	trav := filepath.Join(dir, "trav.zip")
	writeZip(t, trav, map[string][]byte{
		"openpanda/../escape": []byte("x"),
	}, nil)
	if err := unzip(trav, root); err == nil {
		t.Fatal("unzip silently skipped a traversal entry")
	}
}

// TestWriteFileHonorsBudget: the decompressed-byte ceiling is enforced per
// write, so a bomb entry fails instead of filling the disk.
func TestWriteFileHonorsBudget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "f")
	n, err := writeFile(bytes.NewReader(make([]byte, 32)), target, 0o644, 8)
	if err == nil || !strings.Contains(err.Error(), "decompressed") {
		t.Fatalf("err = %v, want a decompressed-bytes refusal", err)
	}
	if n <= 8 {
		t.Fatalf("n = %d, the overshoot byte must be counted", n)
	}
	if _, statErr := os.Stat(target); statErr == nil {
		t.Error("over-budget file was left at the final path")
	}
}

// TestChecksumForBinaryMarker: sha256sum's binary mode writes "hash *name";
// a checksums.txt regenerated that way must still match.
func TestChecksumForBinaryMarker(t *testing.T) {
	sums := "abc123  other.tar.gz\nDEF456 *" + "panda-1.0.0-linux-amd64.tar.gz" + "\n"
	got := checksumFor(sums, "panda-1.0.0-linux-amd64.tar.gz")
	if got != "def456" {
		t.Fatalf("checksumFor = %q, want def456 (lowercased)", got)
	}
}

// TestInstallAdaptersPrunesStale: the manifest lets a release retract an
// adapter — files the previous update installed but the new release no
// longer ships are removed, while files never in the manifest (user-added)
// survive.
func TestInstallAdaptersPrunesStale(t *testing.T) {
	src := t.TempDir()
	adapters := filepath.Join(src, "adapters")
	if err := os.MkdirAll(adapters, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.py", "b.py"} {
		if err := os.WriteFile(filepath.Join(adapters, name), []byte("# "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(adapters, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	t.Setenv("OPENPANDA_ADAPTER_DIR", dst)
	// Previous update installed a.py + stale.py; the user added user.py.
	if err := os.WriteFile(filepath.Join(dst, adapterManifest), []byte("a.py\nstale.py\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"stale.py", "user.py"} {
		if err := os.WriteFile(filepath.Join(dst, name), []byte("# "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := installAdapters(&stagedRelease{root: src}); err != nil {
		t.Fatalf("installAdapters: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "stale.py")); !os.IsNotExist(err) {
		t.Error("stale.py should have been pruned by the manifest")
	}
	for _, name := range []string{"a.py", "b.py", "user.py"} {
		if _, err := os.Stat(filepath.Join(dst, name)); err != nil {
			t.Errorf("%s missing after install: %v", name, err)
		}
	}
	got, err := os.ReadFile(filepath.Join(dst, adapterManifest))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "a.py\nb.py\n" {
		t.Errorf("manifest = %q, want %q", got, "a.py\nb.py\n")
	}
}

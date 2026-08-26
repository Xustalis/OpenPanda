package updater

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTarGz builds a .tar.gz from the given headers, using each header's Name
// verbatim so a test can ship entries a well-behaved archiver would never emit.
func writeTarGz(t *testing.T, path string, entries []*tar.Header) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg && h.Size > 0 {
			if _, err := tw.Write(make([]byte, h.Size)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestUntargzRejectsEscapingSymlink covers the extraction half of the
// self-update trust chain (audit P3-12). sanitizeEntry bounds an entry's own
// name, which is why the attack is not carried by the name: the archive ships a
// symlink whose *target* leaves the root, and a later entry with a blameless
// name writes through it. Before this check, untargz created that link with the
// error deliberately discarded.
func TestUntargzRejectsEscapingSymlink(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "rel.tar.gz")
	writeTarGz(t, archive, []*tar.Header{
		{Name: "openpanda/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "openpanda/escape", Typeflag: tar.TypeSymlink, Linkname: "../../..", Mode: 0o777},
	})

	root := filepath.Join(dir, "out")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	err := untargz(archive, root)
	if err == nil {
		t.Fatal("untargz accepted a symlink pointing outside the extraction root")
	}
	if !strings.Contains(err.Error(), "outside the extraction root") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, lerr := os.Lstat(filepath.Join(root, "escape")); lerr == nil {
		t.Error("the rejected symlink was created anyway")
	}
}

// TestUntargzRejectsAbsoluteSymlinkAndHardLink pins the two other link shapes an
// archive can use to reach outside the root: an absolute target needs no
// traversal at all, and a hard link names an inode that no path check can vet.
func TestUntargzRejectsAbsoluteSymlinkAndHardLink(t *testing.T) {
	for _, tc := range []struct {
		name string
		hdr  *tar.Header
		want string
	}{
		{"absolute symlink",
			&tar.Header{Name: "openpanda/abs", Typeflag: tar.TypeSymlink, Linkname: "/etc", Mode: 0o777},
			"is absolute"},
		{"hard link",
			&tar.Header{Name: "openpanda/hard", Typeflag: tar.TypeLink, Linkname: "/etc/passwd", Mode: 0o644},
			"hard link"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			archive := filepath.Join(dir, "rel.tar.gz")
			writeTarGz(t, archive, []*tar.Header{tc.hdr})
			root := filepath.Join(dir, "out")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			err := untargz(archive, root)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

// TestUntargzAcceptsOrdinaryArchive is the other half: the new rejections must
// not make a normal release archive — files, directories and an in-tree symlink
// — unextractable, and pax/GNU metadata entries must still be ignored rather
// than treated as unsupported types.
func TestUntargzAcceptsOrdinaryArchive(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "rel.tar.gz")
	writeTarGz(t, archive, []*tar.Header{
		{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, Size: 0},
		{Name: "openpanda/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "openpanda/panda", Typeflag: tar.TypeReg, Mode: 0o755, Size: 8},
		{Name: "openpanda/adapters/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "openpanda/adapters/a.py", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4},
		{Name: "openpanda/adapters/link.py", Typeflag: tar.TypeSymlink, Linkname: "a.py", Mode: 0o777},
	})

	root := filepath.Join(dir, "out")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := untargz(archive, root); err != nil {
		t.Fatalf("untargz rejected an ordinary archive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "panda")); err != nil {
		t.Errorf("binary not extracted: %v", err)
	}
	target, err := os.Readlink(filepath.Join(root, "adapters", "link.py"))
	if err != nil {
		t.Fatalf("in-tree symlink not created: %v", err)
	}
	if target != "a.py" {
		t.Errorf("symlink target = %q, want a.py", target)
	}
}

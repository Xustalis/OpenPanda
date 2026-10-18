// SPDX-License-Identifier: AGPL-3.0-or-later

package artifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// hostileEntry is one header (plus optional body) to hand to Unpack. These
// archives cannot be produced by Pack — they are what a malicious or
// compromised peer would send, so they have to be built by hand.
type hostileEntry struct {
	hdr  tar.Header
	body string
}

// buildTarGz assembles a tar.gz from raw headers, bypassing Pack's own
// normalization so Unpack's defences are what is under test.
func buildTarGz(t *testing.T, entries ...hostileEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := e.hdr
		if hdr.Typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if hdr.Mode == 0 {
			hdr.Mode = 0o644
		}
		hdr.Format = tar.FormatPAX
		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatalf("write header %q: %v", hdr.Name, err)
		}
		if e.body != "" {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("write body %q: %v", hdr.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

// TestWriteRegularReplacesReadOnlyDestination: a re-extraction lands on files
// that already exist, and git marks its objects read-only — Windows refuses
// the overwrite rename on a read-only destination with ACCESS_DENIED, which
// is what killed project-tree delivery to a second machine. The writer must
// clear the attribute and replace the file on every platform.
func TestWriteRegularReplacesReadOnlyDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "object")
	if err := os.WriteFile(path, []byte("old"), 0o444); err != nil {
		t.Fatalf("seed read-only file: %v", err)
	}
	// Read-only must actually be in force on POSIX or the test proves nothing.
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatalf("chmod read-only: %v", err)
	}
	if _, err := writeRegular(strings.NewReader("new content"), path, 0o644, int64(len("new content"))); err != nil {
		t.Fatalf("writeRegular over read-only destination: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "new content" {
		t.Fatalf("content = %q, want the replacement", got)
	}
	if _, err := os.Stat(path + ".partial"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial temp file left behind: %v", err)
	}
}

// TestWriteRegularKeepsExecBitsOnWindowsMode pins the cross-platform project
// contract: a packer that cannot express POSIX exec bits ships every file at
// 0666, and adopting that tree over a POSIX checkout must not turn every
// script un-executable — the failure showed up for real as `git status`
// reporting dozens of 100755→100644 mode flips after a remote task ran on
// Windows.
func TestWriteRegularKeepsExecBitsOnWindowsMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no POSIX exec bits to preserve")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("seed executable: %v", err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod executable: %v", err)
	}
	// The Windows fingerprint: every regular file arrives at 0666.
	landed, err := writeRegular(strings.NewReader("#!/bin/sh\necho hi\n"), path, 0o666, int64(len("#!/bin/sh\necho hi\n")))
	if err != nil {
		t.Fatalf("writeRegular over executable destination: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat result: %v", err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("mode = %o, exec bits were stripped from an existing executable", fi.Mode().Perm())
	}
	// The mode the writer reports is the mode on disk (umask included), so
	// a manifest entry describes the file a verifier will actually stat.
	if landed != fi.Mode().Perm() {
		t.Fatalf("writeRegular reported mode %o, disk has %o", landed, fi.Mode().Perm())
	}

	// The same archive mode on a path that does not exist yet must land
	// verbatim — "keep exec" is a destination-merge rule, not a reason to
	// make every new file executable.
	fresh := filepath.Join(dir, "new.txt")
	landed, err = writeRegular(strings.NewReader("x"), fresh, 0o666, 1)
	if err != nil {
		t.Fatalf("writeRegular new file: %v", err)
	}
	if fi, err := os.Stat(fresh); err != nil {
		t.Fatalf("stat new file: %v", err)
	} else if fi.Mode().Perm()&0o111 != 0 {
		t.Fatalf("new file mode = %o, exec bits were invented", fi.Mode().Perm())
	} else if landed != fi.Mode().Perm() {
		t.Fatalf("writeRegular reported mode %o for a new file, disk has %o", landed, fi.Mode().Perm())
	}

	// A symlink at the destination is a reference, not the file itself: its
	// target's exec bits must not be carried onto the replacement (the probe
	// is Lstat deliberately), and the reported mode still matches the disk.
	target := filepath.Join(dir, "real.sh")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("seed symlink target: %v", err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatalf("chmod symlink target: %v", err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	landed, err = writeRegular(strings.NewReader("x"), link, 0o666, 1)
	if err != nil {
		t.Fatalf("writeRegular over symlink destination: %v", err)
	}
	fi, err = os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat link: %v", err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 != 0 {
		t.Fatalf("link replacement mode = %o, want the archive mode without exec bits", fi.Mode().Perm())
	}
	if landed != fi.Mode().Perm() {
		t.Fatalf("writeRegular reported mode %o over a symlink, disk has %o", landed, fi.Mode().Perm())
	}
}

// TestUnpackRejectsHostileEntries is the security contract. An artifact arrives
// from another node over the network, so each of these is a real attack an
// unpacker has to refuse — and refuse for the whole archive, since honouring the
// remaining entries would leave the caller with a tree it wrongly believes is
// complete.
func TestUnpackRejectsHostileEntries(t *testing.T) {
	cases := []struct {
		name  string
		entry hostileEntry
		want  string // substring of the rejection reason
	}{
		{
			name:  "parent traversal",
			entry: hostileEntry{hdr: tar.Header{Name: "../escaped.txt", Typeflag: tar.TypeReg}, body: "pwned"},
			want:  "traversal",
		},
		{
			name:  "deep traversal",
			entry: hostileEntry{hdr: tar.Header{Name: "a/b/../../../escaped.txt", Typeflag: tar.TypeReg}, body: "pwned"},
			want:  "traversal",
		},
		{
			name:  "absolute path",
			entry: hostileEntry{hdr: tar.Header{Name: "/tmp/escaped.txt", Typeflag: tar.TypeReg}, body: "pwned"},
			want:  "absolute path",
		},
		{
			name:  "windows drive path",
			entry: hostileEntry{hdr: tar.Header{Name: `C:/Windows/escaped.txt`, Typeflag: tar.TypeReg}, body: "pwned"},
			want:  "absolute path",
		},
		{
			name:  "symlink to absolute target",
			entry: hostileEntry{hdr: tar.Header{Name: "link", Linkname: "/etc", Typeflag: tar.TypeSymlink}},
			want:  "absolute target",
		},
		{
			name:  "symlink escaping via ..",
			entry: hostileEntry{hdr: tar.Header{Name: "link", Linkname: "../../outside", Typeflag: tar.TypeSymlink}},
			want:  "outside the destination",
		},
		{
			name:  "symlink escaping from a subdirectory",
			entry: hostileEntry{hdr: tar.Header{Name: "sub/link", Linkname: "../../../outside", Typeflag: tar.TypeSymlink}},
			want:  "outside the destination",
		},
		{
			name:  "hard link",
			entry: hostileEntry{hdr: tar.Header{Name: "hard", Linkname: "/etc/passwd", Typeflag: tar.TypeLink}},
			want:  "hard link",
		},
		{
			name:  "character device",
			entry: hostileEntry{hdr: tar.Header{Name: "dev", Typeflag: tar.TypeChar}},
			want:  "files and directories only",
		},
		{
			name:  "fifo",
			entry: hostileEntry{hdr: tar.Header{Name: "pipe", Typeflag: tar.TypeFifo}},
			want:  "files and directories only",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A benign entry first, so a test that only checked "nothing was
			// written" could not pass by accident.
			raw := buildTarGz(t,
				hostileEntry{hdr: tar.Header{Name: "ok.txt", Typeflag: tar.TypeReg}, body: "fine"},
				tc.entry,
			)
			dst := filepath.Join(t.TempDir(), "out")
			_, err := Unpack(bytes.NewReader(raw), dst)
			if err == nil {
				t.Fatalf("unpack accepted a hostile archive (%s)", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.want)
			}
			// Nothing may exist outside dst, whatever the entry asked for.
			parent := filepath.Dir(dst)
			if _, err := os.Stat(filepath.Join(parent, "escaped.txt")); err == nil {
				t.Fatalf("%s wrote a file outside the destination", tc.name)
			}
		})
	}
}

// TestUnpackRefusesWriteThroughExistingSymlink is the other half of the link
// contract: safeRel and checkLinkTarget vet the archive's own names and
// links, but the destination is often a pre-existing tree (the project
// checkout a worktree adoption writes over). A symlinked directory that was
// already there — node_modules, a mounted checkout, a planted hole — is
// followed silently by MkdirAll and OpenFile, so an entry under it would
// write outside dst. The whole archive must be refused instead.
func TestUnpackRefusesWriteThroughExistingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	outside := t.TempDir()
	dst := filepath.Join(t.TempDir(), "checkout")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("make dst: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(dst, "linked-out")); err != nil {
		t.Fatalf("plant pre-existing link: %v", err)
	}

	raw := buildTarGz(t, hostileEntry{
		hdr:  tar.Header{Name: "linked-out/pwned.txt", Typeflag: tar.TypeReg},
		body: "pwned",
	})
	if _, err := Unpack(bytes.NewReader(raw), dst); err == nil {
		t.Fatal("unpack wrote through a pre-existing symlink out of the destination")
	}
	if _, err := os.Lstat(filepath.Join(outside, "pwned.txt")); err == nil {
		t.Fatal("file landed outside the destination")
	}

	// The archive's own link pointing back inside is still legitimate: an
	// entry under it resolves within dst and must not be confused with the
	// escape above.
	raw = buildTarGz(t,
		hostileEntry{hdr: tar.Header{Name: "real", Typeflag: tar.TypeDir}},
		hostileEntry{hdr: tar.Header{Name: "alias", Linkname: "real", Typeflag: tar.TypeSymlink}},
		hostileEntry{hdr: tar.Header{Name: "alias/note.txt", Typeflag: tar.TypeReg}, body: "kept"},
	)
	if _, err := Unpack(bytes.NewReader(raw), dst); err != nil {
		t.Fatalf("unpack through an in-archive link that stays inside: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dst, "real", "note.txt")); err != nil || string(data) != "kept" {
		t.Fatalf("inside-link write lost: data=%q err=%v", data, err)
	}
}

// TestUnpackCapsDirectoryEntries pins the count contract: maxEntries bounds
// every tar member, not only the regular files the manifest lists — an
// archive of nothing but directories compresses to almost nothing, so a
// files-only cap would still allow an inode-flood of MkdirAll calls. The
// cap is shrunk for the test rather than materialising a six-figure
// tar — the contract under test is that directory members count, not the
// particular bound.
func TestUnpackCapsDirectoryEntries(t *testing.T) {
	orig := maxEntries
	maxEntries = 4
	defer func() { maxEntries = orig }()

	entries := make([]hostileEntry, 0, maxEntries+1)
	for i := 0; i <= maxEntries; i++ {
		entries = append(entries, hostileEntry{
			hdr: tar.Header{Name: fmt.Sprintf("dir-%d", i), Typeflag: tar.TypeDir},
		})
	}
	raw := buildTarGz(t, entries...)
	_, err := Unpack(bytes.NewReader(raw), filepath.Join(t.TempDir(), "out"))
	if err == nil || !errors.Is(err, ErrTooLarge) {
		t.Fatalf("unpack of %d directory entries: err = %v, want ErrTooLarge", maxEntries+1, err)
	}
}

// TestUnpackAcceptsContainedSymlink is the other side of the symlink rule: a
// link pointing at a sibling inside the artifact is ordinary content (a build
// output often has one) and must survive the trip.
func TestUnpackAcceptsContainedSymlink(t *testing.T) {
	raw := buildTarGz(t,
		hostileEntry{hdr: tar.Header{Name: "real.txt", Typeflag: tar.TypeReg}, body: "content"},
		hostileEntry{hdr: tar.Header{Name: "sub/", Typeflag: tar.TypeDir, Mode: 0o755}},
		hostileEntry{hdr: tar.Header{Name: "sub/link", Linkname: "../real.txt", Typeflag: tar.TypeSymlink}},
	)
	dst := filepath.Join(t.TempDir(), "out")
	if _, err := Unpack(bytes.NewReader(raw), dst); err != nil {
		t.Fatalf("unpack rejected a contained symlink: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "sub", "link"))
	if err != nil {
		t.Fatalf("read through symlink: %v", err)
	}
	if string(got) != "content" {
		t.Fatalf("symlink resolved to %q, want %q", got, "content")
	}
}

// TestUnpackRejectsOversizedContent covers the gzip bomb: a few KiB on the wire
// that expands until the disk is full. The declared sizes are what Unpack
// budgets against, so the archive is refused before any of it is written.
func TestUnpackRejectsOversizedContent(t *testing.T) {
	// One entry whose header claims more than the cap. The body is short, so the
	// archive itself stays tiny — exactly a bomb's shape.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: "huge.bin", Typeflag: tar.TypeReg, Mode: 0o644, Size: MaxBytes + 1, Format: tar.FormatPAX,
	}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	// Deliberately not writing Size bytes: Unpack must refuse on the declaration.
	_, _ = tw.Write(bytes.Repeat([]byte("x"), 4096))
	_ = tw.Close()
	_ = gz.Close()

	dst := filepath.Join(t.TempDir(), "out")
	_, err := unpack(bytes.NewReader(buf.Bytes()), dst, MaxBytes, 0, nil)
	if err == nil {
		t.Fatalf("unpack accepted an archive declaring more than %d bytes", MaxBytes)
	}
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("error = %v, want ErrTooLarge", err)
	}

	// Unbounded mode refuses the same shape against the disk instead: no real
	// filesystem has 1 EiB free, so the declared size is refused outright.
	buf2 := func() *bytes.Buffer {
		var b bytes.Buffer
		gz := gzip.NewWriter(&b)
		tw := tar.NewWriter(gz)
		if err := tw.WriteHeader(&tar.Header{
			Name: "absurd.bin", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1 << 60, Format: tar.FormatPAX,
		}); err != nil {
			t.Fatalf("write header: %v", err)
		}
		_, _ = tw.Write(bytes.Repeat([]byte("x"), 4096))
		_ = tw.Close()
		_ = gz.Close()
		return &b
	}()
	if _, err = unpack(buf2, filepath.Join(t.TempDir(), "out3"), 0, 0, nil); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("unbounded unpack error = %v, want ErrNoSpace", err)
	}
}

// TestUnpackRejectsTamperedStream pins the property the chunked transfer relies
// on: a modified archive cannot round-trip to the sender's hash. Either the
// gzip/tar layer rejects it outright, or it unpacks to a different hash — never
// silently to the same one.
func TestUnpackRejectsTamperedStream(t *testing.T) {
	root := writeTree(t, map[string]string{"a.txt": strings.Repeat("payload", 500)})
	packed, raw := packTo(t, root)

	tampered := append([]byte(nil), raw...)
	tampered[len(tampered)/2] ^= 0xff

	dst := filepath.Join(t.TempDir(), "out")
	got, err := Unpack(bytes.NewReader(tampered), dst)
	if err != nil {
		return // corruption caught by the gzip CRC or the tar layer
	}
	if got.Hash == packed.Hash {
		t.Fatalf("a tampered archive unpacked to the sender's hash %s", packed.Hash)
	}
}

// TestPackRejectsNonDirectoryRoot: an artifact is a tree. A caller pointing at a
// single file should hear so rather than get an empty archive.
func TestPackRejectsNonDirectoryRoot(t *testing.T) {
	root := writeTree(t, map[string]string{"a.txt": "x"})
	if _, err := Pack(filepath.Join(root, "a.txt"), &bytes.Buffer{}); err == nil {
		t.Fatalf("Pack accepted a file as its root")
	}
	if _, err := Pack(filepath.Join(root, "missing"), &bytes.Buffer{}); err == nil {
		t.Fatalf("Pack accepted a missing root")
	}
}

// TestHashMatchesPack: Hash(file) is what a receiver compares against after a
// chunked transfer, so it must equal the Manifest.Hash the sender reported.
func TestHashMatchesPack(t *testing.T) {
	root := writeTree(t, map[string]string{"a.txt": "content"})
	stored := filepath.Join(t.TempDir(), "artifact.tar.gz")
	f, err := os.Create(stored)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	m, err := Pack(root, f)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	got, err := Hash(stored)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if got != m.Hash {
		t.Fatalf("Hash(file) = %s, want the manifest's %s", got, m.Hash)
	}
}

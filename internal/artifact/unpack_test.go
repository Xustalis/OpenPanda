package artifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
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
	_, err := Unpack(bytes.NewReader(buf.Bytes()), dst)
	if err == nil {
		t.Fatalf("unpack accepted an archive declaring more than %d bytes", MaxBytes)
	}
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("error = %v, want ErrTooLarge", err)
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

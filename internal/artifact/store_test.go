package artifact

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStoreRoundTrip is the pool's contract end to end, in the shape the
// orchestrator uses it: a producing node packs its output tree, the archive is
// named by its hash, and a consuming node materializes the same tree from it.
func TestStoreRoundTrip(t *testing.T) {
	tree := writeTree(t, map[string]string{
		"weights.bin":  "trained\x00bytes",
		"src/train.py": "print('train')",
	})
	src := NewStore(filepath.Join(t.TempDir(), "pool"))

	m, err := src.PackDir(tree)
	if err != nil {
		t.Fatalf("pack dir: %v", err)
	}
	if size, ok := src.Has(m.Hash); !ok || size != m.Size {
		t.Fatalf("Has(%s) = %d, %v; want %d, true", m.Hash, size, ok, m.Size)
	}
	if hashes, err := src.List(); err != nil {
		t.Fatalf("list: %v", err)
	} else if len(hashes) != 1 || hashes[0] != m.Hash {
		t.Fatalf("List = %v, want [%s]", hashes, m.Hash)
	}

	// The consuming node's pool: it receives the bytes and verifies them before
	// the artifact is reachable under its hash.
	dstPool := NewStore(filepath.Join(t.TempDir(), "pool"))
	f, err := src.Open(m.Hash)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	if _, err := dstPool.Put(m.Hash, f); err != nil {
		t.Fatalf("put: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "input")
	if _, err := dstPool.Extract(m.Hash, dst); err != nil {
		t.Fatalf("extract: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "weights.bin"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "trained\x00bytes" {
		t.Fatalf("weights.bin = %q after the round trip", got)
	}
}

// TestStorePutRejectsWrongHash is the check that makes content addressing mean
// something. An artifact stored under a hash it does not have would be consumed
// by the next stage as verified input.
func TestStorePutRejectsWrongHash(t *testing.T) {
	tree := writeTree(t, map[string]string{"a.txt": "real"})
	src := NewStore(filepath.Join(t.TempDir(), "pool"))
	m, err := src.PackDir(tree)
	if err != nil {
		t.Fatalf("pack dir: %v", err)
	}

	dst := NewStore(filepath.Join(t.TempDir(), "pool"))
	// Substituted content under the legitimate hash.
	if _, err := dst.Put(m.Hash, strings.NewReader("not the artifact")); err == nil {
		t.Fatalf("Put accepted content that does not hash to its name")
	}
	if _, ok := dst.Has(m.Hash); ok {
		t.Fatalf("a rejected artifact is reachable under %s", m.Hash)
	}
	if hashes, err := dst.List(); err != nil {
		t.Fatalf("list: %v", err)
	} else if len(hashes) != 0 {
		t.Fatalf("pool holds %v after a rejected Put", hashes)
	}

	// A hash-shaped name is required: the value arrives from a peer and becomes
	// a filename.
	for _, bad := range []string{"", "../../etc/passwd", "GGGG", strings.Repeat("a", 63)} {
		if _, err := dst.Put(bad, strings.NewReader("x")); err == nil {
			t.Fatalf("Put accepted %q as a hash", bad)
		}
		if _, ok := dst.Has(bad); ok {
			t.Fatalf("Has accepted %q as a hash", bad)
		}
	}
}

// TestStoreReadAtChunks pins the primitive the bus transfer sits on: sequential
// offset reads reassemble the archive exactly, and the final read reports EOF so
// the sender knows when to stop.
func TestStoreReadAtChunks(t *testing.T) {
	// Content larger than one chunk, and deliberately not a chunk multiple.
	body := strings.Repeat("payload-", 40_000) // ~320 KiB, compresses well but not to nothing
	tree := writeTree(t, map[string]string{"big.bin": body, "small.txt": "s"})
	s := NewStore(filepath.Join(t.TempDir(), "pool"))
	m, err := s.PackDir(tree)
	if err != nil {
		t.Fatalf("pack dir: %v", err)
	}

	const chunk = 4096
	var assembled bytes.Buffer
	buf := make([]byte, chunk)
	var off int64
	for reads := 0; ; reads++ {
		if reads > 10_000 {
			t.Fatalf("chunked read did not terminate")
		}
		n, eof, err := s.ReadAt(m.Hash, off, buf)
		if err != nil {
			t.Fatalf("read at %d: %v", off, err)
		}
		assembled.Write(buf[:n])
		off += int64(n)
		if eof {
			break
		}
		if n == 0 {
			t.Fatalf("read 0 bytes at %d without reporting EOF", off)
		}
	}
	if int64(assembled.Len()) != m.Size {
		t.Fatalf("reassembled %d bytes, want %d", assembled.Len(), m.Size)
	}

	// The reassembled stream must unpack: chunking is not allowed to reorder or
	// drop anything.
	out := filepath.Join(t.TempDir(), "restored")
	got, err := Unpack(bytes.NewReader(assembled.Bytes()), out)
	if err != nil {
		t.Fatalf("unpack reassembled: %v", err)
	}
	if got.Hash != m.Hash {
		t.Fatalf("reassembled hash %s != %s", got.Hash, m.Hash)
	}

	// An offset past the end is a caller bug, not a silent empty read.
	if _, _, err := s.ReadAt(m.Hash, m.Size+1, buf); err == nil {
		t.Fatalf("ReadAt accepted an offset past the end")
	}
}

// TestStoreMissingArtifact: a fetch for something this node does not hold has to
// say so distinguishably, so the caller can ask a peer for it instead of
// treating the stage as broken.
func TestStoreMissingArtifact(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "pool"))
	absent := strings.Repeat("ab", 32)
	if _, err := s.Open(absent); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open error = %v, want ErrNotFound", err)
	}
	if _, err := s.Extract(absent, t.TempDir()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Extract error = %v, want ErrNotFound", err)
	}
	if hashes, err := s.List(); err != nil || len(hashes) != 0 {
		t.Fatalf("List on an empty pool = %v, %v", hashes, err)
	}
	// Removing what is not there is what the caller wanted to be true.
	if err := s.Remove(absent); err != nil {
		t.Fatalf("Remove of an absent artifact: %v", err)
	}
}

// TestStoreExtractDetectsOnDiskDamage: the pool is a plain directory, and a file
// can rot or be edited after it arrived. Extract re-verifies rather than trusting
// the filename.
func TestStoreExtractDetectsOnDiskDamage(t *testing.T) {
	tree := writeTree(t, map[string]string{"a.txt": strings.Repeat("content", 200)})
	s := NewStore(filepath.Join(t.TempDir(), "pool"))
	m, err := s.PackDir(tree)
	if err != nil {
		t.Fatalf("pack dir: %v", err)
	}

	raw, err := os.ReadFile(s.Path(m.Hash))
	if err != nil {
		t.Fatalf("read stored: %v", err)
	}
	raw[len(raw)/2] ^= 0xff
	if err := os.WriteFile(s.Path(m.Hash), raw, 0o644); err != nil {
		t.Fatalf("write damaged: %v", err)
	}

	if _, err := s.Extract(m.Hash, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatalf("Extract accepted a damaged artifact under a verified name")
	}
}

// TestStorePackDirIsIdempotent: re-packing the same tree lands on the same name,
// so a repeated stage costs one file, not two.
func TestStorePackDirIsIdempotent(t *testing.T) {
	tree := writeTree(t, map[string]string{"a.txt": "x", "b/c.txt": "y"})
	s := NewStore(filepath.Join(t.TempDir(), "pool"))
	first, err := s.PackDir(tree)
	if err != nil {
		t.Fatalf("pack dir: %v", err)
	}
	second, err := s.PackDir(tree)
	if err != nil {
		t.Fatalf("pack dir again: %v", err)
	}
	if first.Hash != second.Hash {
		t.Fatalf("two packs of one tree: %s vs %s", first.Hash, second.Hash)
	}
	hashes, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(hashes) != 1 {
		t.Fatalf("pool holds %v after packing the same tree twice", hashes)
	}
}

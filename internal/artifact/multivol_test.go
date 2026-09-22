package artifact

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withFakeFreespace swaps the volume probe for a per-path table; roots absent
// from the table report a fixed default so unrelated test dirs still probe.
func withFakeFreespace(t *testing.T, free map[string]int64, def int64) {
	t.Helper()
	orig := probeFreeBytes
	probeFreeBytes = func(dir string) (int64, bool) {
		if v, ok := free[dir]; ok {
			return v, true
		}
		return def, true
	}
	t.Cleanup(func() { probeFreeBytes = orig })
}

// The pool serves reads across every configured volume: an archive that
// landed on any root is reachable through Has/Open/Extract/List no matter
// which volume new writes would pick.
func TestStoreMultiVolumeReadsAcrossRoots(t *testing.T) {
	primary := t.TempDir()
	extra := t.TempDir()
	s := NewStore(primary, extra)

	m, data := stagedTree(t, 300<<10)
	// Land the archive on the extra volume by hand — as a commit from a push
	// that picked that volume would leave it.
	if err := os.WriteFile(filepath.Join(extra, m.Hash+".tar.gz"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, ok := s.Has(m.Hash); !ok {
		t.Fatal("Has misses an artifact on the extra volume")
	}
	if _, err := s.Open(m.Hash); err != nil {
		t.Fatalf("Open misses the extra volume: %v", err)
	}
	dst := t.TempDir()
	got, err := s.Extract(m.Hash, dst)
	if err != nil {
		t.Fatalf("Extract across volumes: %v", err)
	}
	if got.Hash != m.Hash {
		t.Fatalf("extracted hash %s, want %s", got.Hash, m.Hash)
	}
	if _, err := os.Stat(filepath.Join(dst, "blob.bin")); err != nil {
		t.Fatal("extracted tree is missing its file")
	}
	listed, err := s.List()
	if err != nil || len(listed) != 1 || listed[0] != m.Hash {
		t.Fatalf("List = %v err=%v, want [%s]", listed, err, m.Hash)
	}
	if err := s.Remove(m.Hash); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok := s.Has(m.Hash); ok {
		t.Fatal("Remove left the artifact reachable")
	}
}

// A new write lands on the volume with the most usable space — the extra
// disk wins over the primary when it reports more headroom.
func TestStoreStagesOnRoomiestVolume(t *testing.T) {
	primary := t.TempDir()
	extra := t.TempDir()
	withFakeFreespace(t, map[string]int64{
		primary: 1 << 30,
		extra:   8 << 30,
	}, 1<<30)

	s := NewStore(primary, extra)
	hash := strings.Repeat("ab", 32)
	through, err := s.StageChunk(hash, 0, []byte("chunk"), 100)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if through != 5 {
		t.Fatalf("through = %d, want 5", through)
	}
	if _, err := os.Stat(stagedDataPath(extra, hash)); err != nil {
		t.Fatalf("chunk did not land on the roomier volume: %v", err)
	}
	if _, err := os.Stat(stagedDataPath(primary, hash)); !os.IsNotExist(err) {
		t.Fatal("chunk leaked onto the smaller volume")
	}
}

// The watermark is honored per volume: a transfer that would push the best
// volume below its min-free mark is refused, not written.
func TestStoreWatermarkRefusesOverflow(t *testing.T) {
	dir := t.TempDir()
	withFakeFreespace(t, map[string]int64{dir: 1000}, 1000)

	s := NewStore(dir)
	s.SetMinFreeBytes(900) // only 100 bytes usable
	_, err := s.StageChunk(strings.Repeat("cd", 32), 0, []byte("x"), 500)
	if !errors.Is(err, ErrNoSpace) {
		t.Fatalf("err = %v, want ErrNoSpace", err)
	}
}

// ErrNoSpace is returned when no configured volume can hold the advertised
// total — even though each volume individually has some free space.
func TestStoreNoVolumeFits(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	withFakeFreespace(t, map[string]int64{a: 100, b: 200}, 0)

	s := NewStore(a, b)
	_, err := s.StageChunk(strings.Repeat("ef", 32), 0, []byte("x"), 1000)
	if !errors.Is(err, ErrNoSpace) {
		t.Fatalf("err = %v, want ErrNoSpace", err)
	}
}

// CommitStaged renames inside the volume the chunks chose — the artifact
// ends up in the pool under its hash and readable through the store.
func TestCommitStagedMultiVolume(t *testing.T) {
	primary := t.TempDir()
	extra := t.TempDir()
	s := NewStore(primary, extra)

	m, data := stagedTree(t, 300<<10)
	if _, err := s.StageChunk(m.Hash, 0, data, int64(len(data))); err != nil {
		t.Fatalf("stage: %v", err)
	}
	got, err := s.CommitStaged(m.Hash)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got.Hash != m.Hash {
		t.Fatalf("committed %s, want %s", got.Hash, m.Hash)
	}
	// Exactly one volume holds it, and the store finds it.
	homes := 0
	for _, root := range []string{primary, extra} {
		if _, err := os.Stat(filepath.Join(root, m.Hash+".tar.gz")); err == nil {
			homes++
		}
	}
	if homes != 1 {
		t.Fatalf("artifact found on %d volumes, want exactly 1", homes)
	}
	if _, ok := s.Has(m.Hash); !ok {
		t.Fatal("Has misses the committed artifact")
	}
	// Staging leftovers are gone from both volumes.
	for _, root := range []string{primary, extra} {
		if _, err := os.Stat(stagedMetaPath(root, m.Hash)); !os.IsNotExist(err) {
			t.Fatalf("sidecar left behind on %s", root)
		}
	}
}

// Passing the primary again as an extra path must not make the store walk
// it twice or double-list artifacts.
func TestStoreDedupesRoots(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, dir, "")
	if len(s.Roots()) != 1 {
		t.Fatalf("roots = %v, want the single deduplicated dir", s.Roots())
	}
}

// A staged artifact survives a store rebuild mid-flight and reports progress
// off whichever volume holds the sidecar — not just the primary.
func TestStagedProgressAcrossVolumes(t *testing.T) {
	primary := t.TempDir()
	extra := t.TempDir()
	withFakeFreespace(t, map[string]int64{
		primary: 1 << 20,
		extra:   9 << 20,
	}, 1<<20)

	hash := strings.Repeat("12", 32)
	s := NewStore(primary, extra)
	if _, err := s.StageChunk(hash, 0, []byte("partial"), 100); err != nil {
		t.Fatal(err)
	}
	// Rebuild the store — progress must come back off the extra volume.
	s2 := NewStore(primary, extra)
	info, ok := s2.StagedProgress(hash)
	if !ok || info.ReceivedThrough != 7 || info.Total != 100 {
		t.Fatalf("progress = %+v ok=%v", info, ok)
	}
	if _, err := os.Stat(stagedDataPath(extra, hash)); err != nil {
		t.Fatal("staging file not on the extra volume")
	}
}

// PackDir honors the same placement policy: with a too-small first volume
// the archive lands on the roomier root.
func TestPackDirPicksRoomiestVolume(t *testing.T) {
	primary := t.TempDir()
	extra := t.TempDir()
	withFakeFreespace(t, map[string]int64{
		primary: 1 << 20,
		extra:   9 << 20,
	}, 1<<20)

	tree := t.TempDir()
	if err := os.WriteFile(filepath.Join(tree, "f.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(primary, extra)
	m, err := s.PackDir(tree)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if _, err := os.Stat(filepath.Join(extra, m.Hash+".tar.gz")); err != nil {
		t.Fatalf("archive not on the roomier volume: %v", err)
	}
}

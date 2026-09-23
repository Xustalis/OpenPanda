package artifact

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stagedTree builds a tree whose archive is big enough to span several push
// chunks, packs it in a scratch store — never the one under test, so the
// pool starts empty — and returns the manifest plus the archive bytes the
// push protocol would carry.
func stagedTree(t *testing.T, bulk int) (Manifest, []byte) {
	t.Helper()
	dir := t.TempDir()
	blob := make([]byte, bulk)
	if _, err := rand.Read(blob); err != nil {
		t.Fatalf("fill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), blob, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	packer := NewStore(t.TempDir())
	m, err := packer.PackDir(dir)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	data, err := os.ReadFile(packer.Path(m.Hash))
	if err != nil {
		t.Fatalf("read packed: %v", err)
	}
	return m, data
}

func TestStageChunkOrderedDupGap(t *testing.T) {
	s := NewStore(t.TempDir())
	m, data := stagedTree(t, 300<<10)
	total := int64(len(data))
	chunk := 64 << 10

	push := func(off int64, b []byte) int64 {
		t.Helper()
		through, err := s.StageChunk(m.Hash, off, b, total)
		if err != nil {
			t.Fatalf("stage at %d: %v", off, err)
		}
		return through
	}

	if got := push(0, data[:chunk]); got != int64(chunk) {
		t.Fatalf("first chunk waterline = %d, want %d", got, chunk)
	}
	// A retransmit of the same bytes is a no-op on the waterline.
	if got := push(0, data[:chunk]); got != int64(chunk) {
		t.Fatalf("duplicate chunk moved waterline to %d", got)
	}
	// A chunk past the waterline is a gap — held nothing, reported truthfully.
	if got := push(int64(3*chunk), data[3*chunk:4*chunk]); got != int64(chunk) {
		t.Fatalf("gap chunk moved waterline to %d", got)
	}
	// Fill the gap; the previously-dropped chunk is resent by the sender.
	if got := push(int64(chunk), data[chunk:]); got != total {
		t.Fatalf("final waterline = %d, want %d", got, total)
	}
	info, ok := s.StagedProgress(m.Hash)
	if !ok || info.ReceivedThrough != total {
		t.Fatalf("staged progress = %+v ok=%v", info, ok)
	}
	if _, err := s.CommitStaged(m.Hash); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if size, ok := s.Has(m.Hash); !ok || size != total {
		t.Fatalf("pool holds %d ok=%v, want %d", size, ok, total)
	}
	if _, ok := s.StagedProgress(m.Hash); ok {
		t.Fatal("staging state survived the commit")
	}
}

// TestStageChunkOverlapTrimsToNewTail covers the retransmit shape a resumed
// sender produces: a chunk that starts below the waterline but runs past it
// carries bytes this side never landed — the uncovered tail is appended,
// not the whole chunk dropped as a duplicate.
func TestStageChunkOverlapTrimsToNewTail(t *testing.T) {
	s := NewStore(t.TempDir())
	m, data := stagedTree(t, 200<<10)
	total := int64(len(data))
	half := total / 2

	if through, err := s.StageChunk(m.Hash, 0, data[:half], total); err != nil || through != half {
		t.Fatalf("stage head: through=%d err=%v", through, err)
	}
	// A resend that straddles the waterline: the dup head is skipped and the
	// tail still lands, leaving exactly the archive on disk.
	if through, err := s.StageChunk(m.Hash, half/2, data[half/2:], total); err != nil || through != total {
		t.Fatalf("overlap chunk: through=%d err=%v", through, err)
	}
	if _, err := s.CommitStaged(m.Hash); err != nil {
		t.Fatalf("commit after overlapped resume: %v", err)
	}
	if size, ok := s.Has(m.Hash); !ok || size != total {
		t.Fatalf("pool holds %d ok=%v, want %d", size, ok, total)
	}
}

// TestStageChunkConcurrentTailAppends races two handlers on the same final
// chunk — the shape a replayed tail produces when the retransmit and the
// replay are processed back-to-back. The waterline must land on total once,
// not double-append and corrupt the archive.
func TestStageChunkConcurrentTailAppends(t *testing.T) {
	s := NewStore(t.TempDir())
	m, data := stagedTree(t, 200<<10)
	total := int64(len(data))
	half := total / 2
	if _, err := s.StageChunk(m.Hash, 0, data[:half], total); err != nil {
		t.Fatalf("stage head: %v", err)
	}
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := s.StageChunk(m.Hash, half, data[half:], total)
			done <- err
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent stage: %v", err)
		}
	}
	if _, err := s.CommitStaged(m.Hash); err != nil {
		t.Fatalf("commit after raced tail: %v", err)
	}
}

func TestStageChunkResumeAcrossRestart(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	m, data := stagedTree(t, 200<<10)
	total := int64(len(data))
	half := total / 2
	if _, err := s.StageChunk(m.Hash, 0, data[:half], total); err != nil {
		t.Fatalf("stage half: %v", err)
	}

	// A fresh Store on the same root is the restart: the sidecar must carry
	// the waterline forward so the sender resumes instead of restarting.
	s2 := NewStore(root)
	info, ok := s2.StagedProgress(m.Hash)
	if !ok || info.ReceivedThrough != half {
		t.Fatalf("resumed progress = %+v ok=%v, want %d", info, ok, half)
	}
	if _, err := s2.StageChunk(m.Hash, half, data[half:], total); err != nil {
		t.Fatalf("stage tail: %v", err)
	}
	if _, err := s2.CommitStaged(m.Hash); err != nil {
		t.Fatalf("commit after resume: %v", err)
	}
	if _, ok := s2.Has(m.Hash); !ok {
		t.Fatal("artifact missing after resumed push")
	}
}

func TestStageChunkRejectsTraversalAndBounds(t *testing.T) {
	s := NewStore(t.TempDir())
	s.SetMaxBytes(MaxBytes)
	if _, err := s.StageChunk("../escape", 0, []byte("x"), 1); err == nil {
		t.Fatal("path-traversal hash accepted")
	}
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if _, err := s.StageChunk(hash, 0, []byte("x"), MaxBytes+1); err == nil {
		t.Fatal("total beyond MaxBytes accepted")
	}
	if _, err := s.StageChunk(hash, 0, []byte("x"), 0); err == nil {
		t.Fatal("zero total accepted")
	}
	// Overflow past the advertised total must not grow the file.
	if _, err := s.StageChunk(hash, 0, []byte("abcd"), 2); err == nil {
		t.Fatal("overflowing chunk accepted")
	}
}

// TestStageChunkUnboundedRefusesMoreThanDisk: with no configured cap the
// filesystem's free space is the bound — a total no disk could hold is
// refused before the first byte lands, not after the disk fills.
func TestStageChunkUnboundedRefusesMoreThanDisk(t *testing.T) {
	s := NewStore(t.TempDir())
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	through, err := s.StageChunk(hash, 0, []byte("x"), 1<<60)
	if !errors.Is(err, ErrNoSpace) {
		t.Fatalf("unbounded stage: through=%d err=%v, want ErrNoSpace", through, err)
	}
	if _, ok := s.StagedProgress(hash); ok {
		t.Fatal("a refused advertisement left staging state behind")
	}
	// ...while a plausible advertisement is accepted — the cap is gone, not
	// the check.
	small := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := s.StageChunk(small, 0, []byte("x"), 4); err != nil {
		t.Fatalf("plausible total refused: %v", err)
	}
}

func TestStageChunkTotalMismatch(t *testing.T) {
	s := NewStore(t.TempDir())
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if _, err := s.StageChunk(hash, 0, []byte("aa"), 4); err != nil {
		t.Fatalf("first: %v", err)
	}
	// A second sender claiming a different size for the same hash is refused
	// rather than rewriting the file the first sender is filling.
	if through, err := s.StageChunk(hash, 2, []byte("bb"), 8); err == nil || through != 2 {
		t.Fatalf("mismatched total: through=%d err=%v", through, err)
	}
}

func TestCommitStagedRejectsCorruption(t *testing.T) {
	s := NewStore(t.TempDir())
	m, _ := stagedTree(t, 100<<10)
	// Stage garbage of the right length under the real hash.
	garbage := bytes.Repeat([]byte{0xAB}, int(m.Size))
	if _, err := s.StageChunk(m.Hash, 0, garbage, m.Size); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := s.CommitStaged(m.Hash); err == nil {
		t.Fatal("corrupt staged copy committed")
	}
	if _, ok := s.Has(m.Hash); ok {
		t.Fatal("corrupt bytes reached the pool")
	}
	// The staged files stay for the caller's retry-or-drop decision.
	if _, ok := s.StagedProgress(m.Hash); !ok {
		t.Fatal("staged state lost on failed commit")
	}
	if err := s.DropStaged(m.Hash); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, ok := s.StagedProgress(m.Hash); ok {
		t.Fatal("drop left staged state behind")
	}
}

// TestStageChunkCrashBeforeMetaWrite reproduces the mid-transfer crash the
// contiguous-append protocol must survive: the data file accepted bytes whose
// sidecar write never landed, so the file runs ahead of ReceivedThrough. On
// resume the sender restreams from the recorded waterline — the staged copy
// must end up byte-identical to the archive, not longer by the orphaned tail.
func TestStageChunkCrashBeforeMetaWrite(t *testing.T) {
	s := NewStore(t.TempDir())
	m, data := stagedTree(t, 200<<10)
	total := int64(len(data))
	half := total / 2
	if _, err := s.StageChunk(m.Hash, 0, data[:half], total); err != nil {
		t.Fatalf("stage head: %v", err)
	}
	// Simulate bytes that reached disk without their sidecar update: the file
	// carries a tail the waterline never recorded — what a crash between the
	// chunk write and writeStagedMeta leaves behind.
	f, err := os.OpenFile(stagedDataPath(s.Root(), m.Hash), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open staged data: %v", err)
	}
	tail := data[half : half+1024]
	if _, err := f.Write(tail); err != nil {
		f.Close()
		t.Fatalf("append unrecorded tail: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close staged data: %v", err)
	}
	// The sender resends from the recorded waterline; the stale tail must be
	// overwritten or dropped, never shifted into the stream.
	if through, err := s.StageChunk(m.Hash, half, data[half:], total); err != nil || through != total {
		t.Fatalf("resume chunk: through=%d err=%v", through, err)
	}
	if _, err := s.CommitStaged(m.Hash); err != nil {
		t.Fatalf("commit after crash-resume: %v", err)
	}
	if size, ok := s.Has(m.Hash); !ok || size != total {
		t.Fatalf("pool holds %d ok=%v, want %d", size, ok, total)
	}
}

// TestStageChunkMetaAheadOfFile is the mirror-image corruption: the sidecar
// claims bytes the data file lost (torn write, external truncation). The
// waterline must rewind to what the disk actually holds so the sender
// restreams the missing tail instead of appending past a hole.
func TestStageChunkMetaAheadOfFile(t *testing.T) {
	s := NewStore(t.TempDir())
	m, data := stagedTree(t, 200<<10)
	total := int64(len(data))
	half := total / 2
	if _, err := s.StageChunk(m.Hash, 0, data[:half], total); err != nil {
		t.Fatalf("stage head: %v", err)
	}
	lost := int64(1024)
	if err := os.Truncate(stagedDataPath(s.Root(), m.Hash), half-lost); err != nil {
		t.Fatalf("drop tail below waterline: %v", err)
	}
	// A chunk at the recorded waterline is a gap against the disk's truth:
	// the corrected reply must name the file's real length so the sender
	// restreams the lost region.
	through, err := s.StageChunk(m.Hash, half, data[half:], total)
	if err != nil {
		t.Fatalf("chunk over torn tail: %v", err)
	}
	if through != half-lost {
		t.Fatalf("waterline = %d, want rewound %d", through, half-lost)
	}
	if through, err := s.StageChunk(m.Hash, half-lost, data[half-lost:], total); err != nil || through != total {
		t.Fatalf("restream: through=%d err=%v", through, err)
	}
	if _, err := s.CommitStaged(m.Hash); err != nil {
		t.Fatalf("commit after rewound resume: %v", err)
	}
}

// TestPruneStagedOrphanDebris covers the files a crash can leave that have no
// sidecar at all: a meta .tmp torn mid-rename and a data file whose first
// sidecar never landed. Both outlive their transfer and must age out like
// any other dead staging state.
func TestPruneStagedOrphanDebris(t *testing.T) {
	s := NewStore(t.TempDir())
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	dir := stagedDir(s.Root())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}
	old := time.Now().Add(-2 * time.Hour)
	for _, name := range []string{hash, hash + ".json.tmp"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("debris"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatalf("age %s: %v", name, err)
		}
	}
	if _, err := s.PruneStaged(time.Hour, time.Now()); err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, name := range []string{hash, hash + ".json.tmp"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("orphan %s survived pruning", name)
		}
	}
}

func TestPruneStagedAgesOutDeadPushes(t *testing.T) {
	s := NewStore(t.TempDir())
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if _, err := s.StageChunk(hash, 0, []byte("partial"), 100); err != nil {
		t.Fatalf("stage: %v", err)
	}
	// Fresh staging is never pruned.
	if pruned, err := s.PruneStaged(time.Hour, time.Now()); err != nil || len(pruned) != 0 {
		t.Fatalf("fresh staging pruned: %v %v", pruned, err)
	}
	// Age the sidecar past the bound.
	old := time.Now().Add(-2 * time.Hour)
	meta := stagedMetaPath(s.Root(), hash)
	if err := os.Chtimes(meta, old, old); err != nil {
		t.Fatalf("age sidecar: %v", err)
	}
	pruned, err := s.PruneStaged(time.Hour, time.Now())
	if err != nil || len(pruned) != 1 || pruned[0] != hash {
		t.Fatalf("pruned = %v err=%v", pruned, err)
	}
	if _, ok := s.StagedProgress(hash); ok {
		t.Fatal("staged state survived pruning")
	}
}

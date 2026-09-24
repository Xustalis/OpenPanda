// Staging is the on-disk landing zone for proactively pushed artifacts
// (whitepaper §8.3 fat-push). A pull transfer can hold its target in a temp
// file for the seconds a download lasts; a DTN push may span days of
// contact windows, so the in-flight bytes have to live somewhere that
// survives restarts — here, under <root>/.staging/<hash>, with a small JSON
// sidecar recording contiguous progress.
//
// The protocol is contiguous-append: the sender writes in order, the
// receiver appends in order, and the sidecar's received_through is the only
// resume point either side needs. A chunk below the waterline is a
// retransmit whose tail is trimmed to the uncovered bytes; a chunk above it
// is a gap (dropped — the status reply tells the sender where to pick up).
// Staging lives inside the volume the commit will rename into: os.Rename
// only works within one filesystem, so the .staging directory follows the
// chosen root, never a separate disk.
package artifact

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StagedInfo is the persisted state of one in-flight push.
type StagedInfo struct {
	Hash            string `json:"hash"`
	Total           int64  `json:"total"`
	ReceivedThrough int64  `json:"received_through"`
}

func stagedDir(root string) string { return filepath.Join(root, ".staging") }

func stagedDataPath(root, hash string) string {
	return filepath.Join(stagedDir(root), hash)
}

func stagedMetaPath(root, hash string) string {
	return filepath.Join(stagedDir(root), hash+".json")
}

// StagedProgress reports the contiguous state of an in-flight push, whichever
// volume it is staging on. ok is false when nothing has been staged for hash
// yet — the answer a fresh receiver gives so the sender starts at offset 0
// rather than at its own remembered position.
func (s *Store) StagedProgress(hash string) (info StagedInfo, ok bool) {
	s.stagingMu.Lock()
	defer s.stagingMu.Unlock()
	info, _, ok = s.stagedProgress(hash)
	return info, ok
}

// stagedProgress scans every volume's .staging for the sidecar, returning
// the info and the root it was found on. Callers holding stagingMu use this
// lock-free core.
func (s *Store) stagedProgress(hash string) (info StagedInfo, root string, ok bool) {
	if !validHash(hash) {
		return StagedInfo{}, "", false
	}
	for _, r := range s.roots {
		data, err := os.ReadFile(stagedMetaPath(r, hash))
		if err != nil {
			continue
		}
		if err := json.Unmarshal(data, &info); err != nil || info.Hash != hash {
			continue
		}
		return info, r, true
	}
	return StagedInfo{}, "", false
}

// StageChunk lands one pushed chunk and returns the new contiguous
// waterline. A chunk above the waterline is a gap and is dropped — the
// status reply tells the sender where to pick up. A chunk at or below it is
// appended from the uncovered byte onward: a retransmit whose tail carries
// bytes this side never landed still moves the frontier, and a fully
// covered chunk leaves the waterline (and the reply the caller sends)
// unchanged.
//
// Total is checked before the first byte is written: against the configured
// cap when one exists, else against the volumes' usable space — the
// advertised archive size is attacker input until the hash verifies, and
// these are the only early bounds on how large a staging file a peer can
// grow on this node's disks. The whole check-append-persist sequence runs
// under stagingMu: chunk handlers run concurrently per envelope, and a stale
// waterline check lets two bodies append the same tail and corrupt the
// file beyond repair.
func (s *Store) StageChunk(hash string, offset int64, data []byte, total int64) (int64, error) {
	if !validHash(hash) {
		// The hash becomes a filename below; anything that is not exactly a
		// lowercase SHA-256 hex must never reach the filesystem join.
		return 0, fmt.Errorf("artifact: %q is not an artifact hash", hash)
	}
	if total <= 0 {
		return 0, fmt.Errorf("%w: total %d", ErrInvalid, total)
	}
	if s.maxBytes > 0 && total > s.maxBytes {
		return 0, ErrTooLarge
	}
	s.stagingMu.Lock()
	defer s.stagingMu.Unlock()
	info, root, _ := s.stagedProgress(hash)
	if info.Hash == "" {
		// First byte of a new staged artifact picks the volume: the
		// configured cap was checked above, and unbounded pools refuse an
		// advertisement no volume could hold — total is fixed for the
		// transfer's lifetime, so one check at creation is enough.
		var ok bool
		root, ok = s.pickWriteRoot(total)
		if !ok {
			return 0, fmt.Errorf("%w: %d advertised, no volume can hold it", ErrNoSpace, total)
		}
		info = StagedInfo{Hash: hash, Total: total}
	}
	if total != info.Total {
		// Same hash cannot honestly describe two sizes; the second sender
		// is confused or hostile, and rewriting mid-file would corrupt the
		// bytes the first sender already got acknowledged.
		return info.ReceivedThrough, fmt.Errorf("artifact: staged total changed %d -> %d", info.Total, total)
	}
	// The sidecar's waterline is the resume contract with the sender; the
	// data file holds the bytes the hash will verify. A crash can leave them
	// disagreeing in three shapes, and disk wins each time:
	//   - file longer than the waterline (crash between the append and the
	//     meta write): the tail is bytes the sender never confirmed, and
	//     O_APPEND cannot skip it — every later write lands behind it, so it
	//     must be cut before the stream continues;
	//   - waterline longer than the file (torn write, or the file was lost):
	//     the recorded tail is gone, so the waterline rewinds and the sender
	//     restreams what is missing;
	//   - file exists with no sidecar at all (crash before the first meta
	//     write): an orphan, truncated to the fresh waterline of 0.
	dataPath := stagedDataPath(root, hash)
	size := int64(0)
	if fi, err := os.Stat(dataPath); err == nil {
		size = fi.Size()
	} else if !os.IsNotExist(err) {
		return info.ReceivedThrough, err
	}
	switch {
	case size > info.ReceivedThrough:
		if err := os.Truncate(dataPath, info.ReceivedThrough); err != nil {
			return info.ReceivedThrough, fmt.Errorf("artifact: drop stale staged tail: %w", err)
		}
	case size < info.ReceivedThrough:
		info.ReceivedThrough = size
		if err := writeStagedMeta(root, info); err != nil {
			return info.ReceivedThrough, fmt.Errorf("artifact: persist rewound waterline: %w", err)
		}
	}
	if offset > info.ReceivedThrough {
		return info.ReceivedThrough, nil // gap: the sender must fill below us first
	}
	if offset < info.ReceivedThrough {
		skip := info.ReceivedThrough - offset
		if skip >= int64(len(data)) {
			return info.ReceivedThrough, nil // fully covered retransmit
		}
		data = data[skip:]
		offset = info.ReceivedThrough
	}
	if info.ReceivedThrough+int64(len(data)) > info.Total {
		return info.ReceivedThrough, fmt.Errorf("artifact: staged overflow past total %d", info.Total)
	}
	if len(data) == 0 {
		return info.ReceivedThrough, nil
	}
	if err := os.MkdirAll(stagedDir(root), 0o700); err != nil {
		return info.ReceivedThrough, err
	}
	f, err := os.OpenFile(stagedDataPath(root, hash), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return info.ReceivedThrough, err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		return info.ReceivedThrough, werr
	}
	if cerr != nil {
		return info.ReceivedThrough, cerr
	}
	info.ReceivedThrough += int64(len(data))
	if err := writeStagedMeta(root, info); err != nil {
		return info.ReceivedThrough, err
	}
	return info.ReceivedThrough, nil
}

func writeStagedMeta(root string, info StagedInfo) error {
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	tmp := stagedMetaPath(root, info.Hash) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, stagedMetaPath(root, info.Hash))
}

// CommitStaged verifies a fully-staged archive and moves it into the pool,
// reusing the same trust rule as Put: the filename claims a hash, the bytes
// must prove it. The rename stays inside the volume the chunks landed on —
// that is why staging never crosses filesystems. On mismatch the staging
// files are left in place — a corrupt tail can still be right if the sender
// restreams the last chunk, and the caller decides whether to keep or
// DropStaged.
func (s *Store) CommitStaged(hash string) (Manifest, error) {
	var m Manifest
	if !validHash(hash) {
		return m, fmt.Errorf("artifact: %q is not an artifact hash", hash)
	}
	s.stagingMu.Lock()
	defer s.stagingMu.Unlock()
	info, root, ok := s.stagedProgress(hash)
	if !ok {
		return m, errors.New("artifact: no staged copy")
	}
	if info.ReceivedThrough < info.Total {
		return m, fmt.Errorf("artifact: staged copy incomplete %d/%d", info.ReceivedThrough, info.Total)
	}
	if s.maxBytes > 0 && info.Total > s.maxBytes {
		return m, ErrTooLarge
	}
	// Bytes past the advertised end are a stale tail a crash left behind,
	// not content — StageChunk's reconcile normally cuts them, but a copy
	// completed and then left untouched still verifies against Total alone.
	staged := stagedDataPath(root, hash)
	if fi, err := os.Stat(staged); err == nil && fi.Size() > info.Total {
		if err := os.Truncate(staged, info.Total); err != nil {
			return m, fmt.Errorf("artifact: drop stale staged tail: %w", err)
		}
	}
	got, err := Hash(staged)
	if err != nil {
		return m, err
	}
	if got != hash {
		return m, fmt.Errorf("artifact: staged hash mismatch %s: received %s", hash, got)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return m, err
	}
	if err := os.Rename(staged, filepath.Join(root, got+".tar.gz")); err != nil {
		return m, err
	}
	_ = os.Remove(stagedMetaPath(root, hash))
	return Manifest{Hash: got, Size: info.Total}, nil
}

// DropStaged removes every staged byte for hash — used when the receiver
// abandons a push (rejected, corrupted past repair, or the task died).
func (s *Store) DropStaged(hash string) error {
	s.stagingMu.Lock()
	defer s.stagingMu.Unlock()
	return s.dropStaged(hash)
}

// dropStaged is DropStaged's lock-free core for callers holding stagingMu.
func (s *Store) dropStaged(hash string) error {
	if !validHash(hash) {
		return fmt.Errorf("artifact: %q is not an artifact hash", hash)
	}
	var first error
	for _, root := range s.roots {
		for _, p := range []string{stagedDataPath(root, hash), stagedMetaPath(root, hash), stagedMetaPath(root, hash) + ".tmp"} {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) && first == nil {
				first = err
			}
		}
	}
	return first
}

// PruneStaged drops staging state whose sidecar is older than maxAge — the
// sender's task died or its outbox expired, so the partial copy will never
// complete. Sidecar mtime advances with every landed chunk, so an active
// transfer is never pruned mid-flight. now is injected for testability.
func (s *Store) PruneStaged(maxAge time.Duration, now time.Time) ([]string, error) {
	s.stagingMu.Lock()
	defer s.stagingMu.Unlock()
	var pruned []string
	for _, root := range s.roots {
		dir := stagedDir(root)
		ents, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return pruned, err
		}
		// Three shapes of debris can outlive their transfer: the sidecar
		// (the usual case — it drives dropStaged), a meta .tmp torn off
		// mid-rename, and a bare data file whose first sidecar write never
		// landed. Anything outside the 64-hex hash naming is left alone.
		names := make(map[string]bool, len(ents))
		for _, e := range ents {
			names[e.Name()] = true
		}
		for _, e := range ents {
			name := e.Name()
			fi, err := e.Info()
			if err != nil || now.Sub(fi.ModTime()) < maxAge {
				continue
			}
			switch {
			case strings.HasSuffix(name, ".json.tmp"):
				// A torn writeStagedMeta: debris dropStaged does not name.
				_ = os.Remove(filepath.Join(dir, name))
			case strings.HasSuffix(name, ".json"):
				hash := strings.TrimSuffix(name, ".json")
				if err := s.dropStaged(hash); err == nil {
					pruned = append(pruned, hash)
				}
			case validHash(name) && !names[name+".json"]:
				// A data file with no sidecar is an orphan — when the pair
				// exists, the .json entry above already covers both.
				if err := s.dropStaged(name); err == nil {
					pruned = append(pruned, name)
				}
			}
		}
		// Best-effort cleanup of the staging dir itself once empty.
		if ents, err := os.ReadDir(dir); err == nil && len(ents) == 0 {
			_ = os.Remove(dir)
		}
	}
	return pruned, nil
}

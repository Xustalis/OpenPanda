package artifact

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
)

// ErrNotFound is returned when a store holds no artifact under the given hash.
var ErrNotFound = errors.New("artifact: not found")

// Store is the node-local artifact pool: a flat directory in which each packed
// artifact is named by its own hash. Content addressing is what makes the data
// plane idempotent — a node that already holds an artifact recognizes it by name
// and skips the transfer, and a re-sent artifact overwrites itself harmlessly.
//
// The pool is deliberately not ctxstore and not SQLite. ctxstore's LRU keeps as
// few as five entries on a Micro node (MaxEntriesForResourceClass), so a build
// output waiting for its successor stage would be evicted between hops; a
// SQLite blob would mean holding a multi-GB model in memory to read it out.
type Store struct {
	// roots are the pool's volumes, in configured order: [0] is the primary
	// path every caller of Root() has always meant. Writes pick a root by
	// policy — the one with the most usable space that still fits the
	// advertised size — so a second disk is real capacity, not a replica.
	roots []string

	// maxBytes bounds any single artifact, compressed or decompressed. Zero
	// means the pool accepts whatever a filesystem can hold — the free-space
	// checks at receive time are the bound then, not a fixed constant. A peer
	// is authenticated before it can push anything, so the real risk the cap
	// guarded against was always disk exhaustion, which the disk itself now
	// reports more accurately than any constant.
	maxBytes int64

	// minFree is the watermark every write keeps between the last byte and a
	// full disk: an artifact may be arbitrarily large, but it may not push a
	// volume below minFree. A full filesystem takes SQLite's WAL down with
	// it — a rejected artifact is a far better failure than a wedged node.
	// Zero disables the watermark.
	minFree int64

	// stagingMu serializes the read-progress/append/write-progress critical
	// section of chunked pushes. Two chunk handlers for the same hash can run
	// on different dispatch goroutines; without the lock both can pass the
	// offset check on a stale waterline and double-append, and the corrupt
	// tail then fails the commit hash and poisons a transfer that had in fact
	// already landed every byte.
	stagingMu sync.Mutex
}

// NewStore returns a Store rooted at dir plus any extra volumes, with no size
// cap beyond what the filesystems can hold. The directories are created
// lazily on first write, so constructing a Store for a node that never
// produces artifacts costs nothing.
func NewStore(dir string, extra ...string) *Store {
	roots := []string{dir}
	for _, e := range extra {
		if e = filepath.Clean(e); e != "" && e != "." && !slices.Contains(roots, e) {
			roots = append(roots, e)
		}
	}
	return &Store{roots: roots}
}

// SetMaxBytes reimposes a per-artifact bound; zero or negative removes it,
// which is also the NewStore default. It exists for operators who would
// rather refuse a huge transfer than fill a small disk — the transport's
// chunked protocol is unchanged either way.
func (s *Store) SetMaxBytes(n int64) { s.maxBytes = n }

// Limit reports the configured per-artifact bound, or 0 when the pool is
// unbounded. Callers that preflight a transfer use it to refuse early.
func (s *Store) Limit() int64 { return s.maxBytes }

// SetMinFreeBytes sets the per-volume headroom every write must preserve —
// the disk-filling equivalent of a circuit breaker. Zero disables it.
func (s *Store) SetMinFreeBytes(n int64) { s.minFree = n }

// MinFree reports the configured free-space watermark in bytes.
func (s *Store) MinFree() int64 { return s.minFree }

// Root returns the pool's primary directory — the path callers configured as
// artifact_path. Use Roots for the full volume list.
func (s *Store) Root() string { return s.roots[0] }

// Roots returns every volume the pool writes to, primary first.
func (s *Store) Roots() []string { return slices.Clone(s.roots) }

// probeFreeBytes is the volume-space probe pickWriteRoot and MaxStorable
// consult — a var so tests can simulate volumes with different headroom on
// one filesystem.
var probeFreeBytes = FreeBytes

// pickWriteRoot chooses the volume for a new write of need bytes: the root
// with the most usable space among those that can hold it, where "usable"
// is free space minus the minFree watermark. A need of 0 (unknown size —
// the pull path's stream) picks the roomiest root and lets ENOSPC stay the
// hard bound. ok is false when no volume qualifies, which on a sized write
// means the transfer can be refused before a byte moves.
func (s *Store) pickWriteRoot(need int64) (string, bool) {
	best := ""
	bestUsable := int64(-1)
	for _, root := range s.roots {
		avail, ok := probeFreeBytes(root)
		if !ok {
			continue
		}
		usable := avail - s.minFree
		if usable > bestUsable {
			best, bestUsable = root, usable
		}
	}
	if best == "" {
		return "", false
	}
	if need > 0 && bestUsable < need {
		return "", false
	}
	return best, true
}

// MaxStorable reports the largest artifact the pool could accept right now:
// the most usable space on any single volume. It answers the question the
// receive path asks — "is there anywhere this could land" — not a sum across
// volumes, because one artifact must fit on one filesystem. ok is false when
// no volume's space can be probed.
func (s *Store) MaxStorable() (int64, bool) {
	best := int64(-1)
	known := false
	for _, root := range s.roots {
		if avail, ok := probeFreeBytes(root); ok {
			known = true
			if usable := avail - s.minFree; usable > best {
				best = usable
			}
		}
	}
	return best, known
}

// findPath locates the volume holding hash, or "" — the read-side half of
// multi-root: writes pick a root by policy, so a reader cannot assume the
// primary holds it.
func (s *Store) findPath(hash string) string {
	for _, root := range s.roots {
		p := filepath.Join(root, hash+".tar.gz")
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// PackDir packs tree into the store and returns its manifest. The archive is
// written to a temp file and renamed to its final hash-name only once the hash
// is known — the name cannot be chosen before the content is hashed, and a
// half-written file must never sit at a name that means "verified".
func (s *Store) PackDir(tree string) (Manifest, error) {
	// Placement is chosen before packing starts: the walked content size is
	// the conservative bound for the archive, and a volume that cannot hold
	// it is skipped rather than filled mid-pack.
	ents, err := walk(tree, s.maxBytes)
	if err != nil {
		return Manifest{}, err
	}
	var total int64
	for _, e := range ents {
		total += e.size
	}
	root, ok := s.pickWriteRoot(total)
	if !ok {
		return Manifest{}, fmt.Errorf("%w: %d bytes of content fit no pool volume", ErrNoSpace, total)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Manifest{}, fmt.Errorf("artifact: create store: %w", err)
	}
	tmp, err := os.CreateTemp(root, ".packing-*")
	if err != nil {
		return Manifest{}, fmt.Errorf("artifact: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	m, err := packEntries(ents, tree, tmp, s.maxBytes)
	if err != nil {
		tmp.Close()
		return Manifest{}, err
	}
	if err := tmp.Close(); err != nil {
		return Manifest{}, fmt.Errorf("artifact: close temp: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(root, m.Hash+".tar.gz")); err != nil {
		return Manifest{}, fmt.Errorf("artifact: store %s: %w", m.Hash, err)
	}
	return m, nil
}

// Path returns where the artifact named by hash would live on the primary
// volume. Read paths should use Open/Has/Extract, which search every volume;
// Path exists for callers that need a name, not a lookup.
func (s *Store) Path(hash string) string { return filepath.Join(s.roots[0], hash+".tar.gz") }

// Has reports whether this node already holds the artifact on any volume,
// and its size. The receiving side of a transfer checks this first: an
// artifact already in the pool needs no bytes on the wire at all.
func (s *Store) Has(hash string) (int64, bool) {
	if !validHash(hash) {
		return 0, false
	}
	p := s.findPath(hash)
	if p == "" {
		return 0, false
	}
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return 0, false
	}
	return fi.Size(), true
}

// Open returns a reader over the stored archive, whichever volume holds it.
// The caller closes it.
func (s *Store) Open(hash string) (*os.File, error) {
	if !validHash(hash) {
		return nil, fmt.Errorf("%w: %q is not an artifact hash", ErrNotFound, hash)
	}
	p := s.findPath(hash)
	if p == "" {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, hash)
	}
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, hash)
	}
	return f, err
}

// ReadAt copies up to len(buf) bytes of the stored archive starting at off,
// returning the bytes read and whether the read reached the end. It is the
// primitive the chunked bus transfer sits on: a sender streams one chunk per
// request without ever holding the whole artifact in memory, and a receiver that
// lost a chunk resumes from its own offset.
func (s *Store) ReadAt(hash string, off int64, buf []byte) (int, bool, error) {
	f, err := s.Open(hash)
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, false, err
	}
	if off < 0 || off > fi.Size() {
		return 0, false, fmt.Errorf("artifact: offset %d outside %s (%d bytes)", off, hash, fi.Size())
	}
	n, err := f.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, false, err
	}
	return n, off+int64(n) >= fi.Size(), nil
}

// Put stores the bytes streamed from r, verifying that they hash to want before
// the artifact is given its name. A mismatch leaves the pool untouched and
// returns an error: an artifact that failed verification must not be reachable
// under a hash that promises it is intact, or the next stage would consume
// corrupt or substituted input believing it verified.
func (s *Store) Put(want string, r io.Reader) (Manifest, error) {
	if !validHash(want) {
		return Manifest{}, fmt.Errorf("artifact: %q is not an artifact hash", want)
	}
	// The stream's size is unknown until it ends, so placement picks the
	// roomiest volume up front; ENOSPC remains the honest bound mid-write.
	root, ok := s.pickWriteRoot(0)
	if !ok {
		return Manifest{}, fmt.Errorf("%w: no pool volume can be probed for space", ErrNoSpace)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Manifest{}, fmt.Errorf("artifact: create store: %w", err)
	}
	tmp, err := os.CreateTemp(root, ".incoming-*")
	if err != nil {
		return Manifest{}, fmt.Errorf("artifact: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	// When a cap is configured the reader is limited one byte past it so an
	// oversized stream is detected, not truncated silently. Uncapped pools
	// copy until the stream ends or the disk does.
	var src io.Reader = r
	if s.maxBytes > 0 {
		src = io.LimitReader(r, s.maxBytes+1)
	}
	n, err := io.Copy(tmp, src)
	if err != nil {
		tmp.Close()
		return Manifest{}, fmt.Errorf("artifact: receive %s: %w", want, err)
	}
	if err := tmp.Close(); err != nil {
		return Manifest{}, fmt.Errorf("artifact: close temp: %w", err)
	}
	if s.maxBytes > 0 && n > s.maxBytes {
		return Manifest{}, fmt.Errorf("%w: %s is larger than %d bytes", ErrTooLarge, want, s.maxBytes)
	}
	got, err := Hash(tmpName)
	if err != nil {
		return Manifest{}, err
	}
	if got != want {
		return Manifest{}, fmt.Errorf("artifact: hash mismatch for %s: received %s", want, got)
	}
	if err := os.Rename(tmpName, filepath.Join(root, want+".tar.gz")); err != nil {
		return Manifest{}, fmt.Errorf("artifact: store %s: %w", want, err)
	}
	return Manifest{Hash: want, Size: n}, nil
}

// Extract unpacks a stored artifact into dst and verifies that what it read
// hashes to the name it was stored under. The check is not redundant with Put's:
// an artifact may have been packed locally, or the file may have been damaged on
// disk since it arrived.
func (s *Store) Extract(hash, dst string) (Manifest, error) {
	f, err := s.Open(hash)
	if err != nil {
		return Manifest{}, err
	}
	defer f.Close()
	m, err := unpack(f, dst, s.maxBytes, s.minFree)
	if err != nil {
		return Manifest{}, err
	}
	if m.Hash != hash {
		return Manifest{}, fmt.Errorf("artifact: %s unpacked to hash %s: the stored file is damaged", hash, m.Hash)
	}
	return m, nil
}

// Remove deletes a stored artifact from whichever volume holds it. A missing
// artifact is not an error: removal is what a caller wants to be true, and
// it already is.
func (s *Store) Remove(hash string) error {
	if !validHash(hash) {
		return fmt.Errorf("artifact: %q is not an artifact hash", hash)
	}
	for _, root := range s.roots {
		p := filepath.Join(root, hash+".tar.gz")
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// List returns the hashes the pool holds across all volumes, sorted. Temp
// files from interrupted transfers are ignored — they carry no valid hash
// name.
func (s *Store) List() ([]string, error) {
	seen := map[string]bool{}
	for _, root := range s.roots {
		ents, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".tar.gz")
			if name != e.Name() && validHash(name) {
				seen[name] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	sort.Strings(out)
	return out, nil
}

// validHash accepts exactly a lowercase hex SHA-256. It is a path-safety check
// as much as a format check: the hash arrives from a peer and is used to build a
// filename, so anything containing a separator or ".." must never get that far.
func validHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	for i := 0; i < len(hash); i++ {
		c := hash[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

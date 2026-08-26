package artifact

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
type Store struct{ root string }

// NewStore returns a Store rooted at dir. The directory is created lazily on
// first write, so constructing a Store for a node that never produces artifacts
// costs nothing.
func NewStore(dir string) *Store { return &Store{root: dir} }

// Root returns the pool's directory.
func (s *Store) Root() string { return s.root }

// PackDir packs tree into the store and returns its manifest. The archive is
// written to a temp file and renamed to its final hash-name only once the hash
// is known — the name cannot be chosen before the content is hashed, and a
// half-written file must never sit at a name that means "verified".
func (s *Store) PackDir(tree string) (Manifest, error) {
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return Manifest{}, fmt.Errorf("artifact: create store: %w", err)
	}
	tmp, err := os.CreateTemp(s.root, ".packing-*")
	if err != nil {
		return Manifest{}, fmt.Errorf("artifact: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	m, err := Pack(tree, tmp)
	if err != nil {
		tmp.Close()
		return Manifest{}, err
	}
	if err := tmp.Close(); err != nil {
		return Manifest{}, fmt.Errorf("artifact: close temp: %w", err)
	}
	if err := os.Rename(tmpName, s.Path(m.Hash)); err != nil {
		return Manifest{}, fmt.Errorf("artifact: store %s: %w", m.Hash, err)
	}
	return m, nil
}

// Path returns where the artifact named by hash lives, whether or not it exists.
func (s *Store) Path(hash string) string { return filepath.Join(s.root, hash+".tar.gz") }

// Has reports whether this node already holds the artifact, and its size. The
// receiving side of a transfer checks this first: an artifact already in the
// pool needs no bytes on the wire at all.
func (s *Store) Has(hash string) (int64, bool) {
	if !validHash(hash) {
		return 0, false
	}
	fi, err := os.Stat(s.Path(hash))
	if err != nil || !fi.Mode().IsRegular() {
		return 0, false
	}
	return fi.Size(), true
}

// Open returns a reader over the stored archive. The caller closes it.
func (s *Store) Open(hash string) (*os.File, error) {
	if !validHash(hash) {
		return nil, fmt.Errorf("%w: %q is not an artifact hash", ErrNotFound, hash)
	}
	f, err := os.Open(s.Path(hash))
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
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return Manifest{}, fmt.Errorf("artifact: create store: %w", err)
	}
	tmp, err := os.CreateTemp(s.root, ".incoming-*")
	if err != nil {
		return Manifest{}, fmt.Errorf("artifact: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	n, err := io.Copy(tmp, io.LimitReader(r, MaxBytes+1))
	if err != nil {
		tmp.Close()
		return Manifest{}, fmt.Errorf("artifact: receive %s: %w", want, err)
	}
	if err := tmp.Close(); err != nil {
		return Manifest{}, fmt.Errorf("artifact: close temp: %w", err)
	}
	if n > MaxBytes {
		return Manifest{}, fmt.Errorf("%w: %s is larger than %d bytes", ErrTooLarge, want, MaxBytes)
	}
	got, err := Hash(tmpName)
	if err != nil {
		return Manifest{}, err
	}
	if got != want {
		return Manifest{}, fmt.Errorf("artifact: hash mismatch for %s: received %s", want, got)
	}
	if err := os.Rename(tmpName, s.Path(want)); err != nil {
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
	m, err := Unpack(f, dst)
	if err != nil {
		return Manifest{}, err
	}
	if m.Hash != hash {
		return Manifest{}, fmt.Errorf("artifact: %s unpacked to hash %s: the stored file is damaged", hash, m.Hash)
	}
	return m, nil
}

// Remove deletes a stored artifact. A missing artifact is not an error: removal
// is what a caller wants to be true, and it already is.
func (s *Store) Remove(hash string) error {
	if !validHash(hash) {
		return fmt.Errorf("artifact: %q is not an artifact hash", hash)
	}
	if err := os.Remove(s.Path(hash)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// List returns the hashes the pool holds, sorted. Temp files from interrupted
// transfers are ignored — they carry no valid hash name.
func (s *Store) List() ([]string, error) {
	ents, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".tar.gz")
		if name != e.Name() && validHash(name) {
			out = append(out, name)
		}
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

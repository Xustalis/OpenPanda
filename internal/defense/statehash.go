package defense

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// HashDir produces a deterministic content hash of the regular files under
// root — the file-tree equivalent of the whitepaper's normalized project AST
// fingerprint (§6.2): two trees with identical relative paths and identical
// content hash identically, regardless of mtimes or host. It feeds the
// monotonic-progress check that detects "logical oscillation" — a mesh that
// keeps revisiting a tree it already produced (agent B reverting agent A's
// fix and calling it progress).
//
// Unlike SnapshotDir's size+mtime stamp, only content participates: a rewrite
// that preserves bytes is genuinely no change. The walk applies the same
// prune rules (snapshotPruneDirs, SQLite transient suffixes) so vendored
// noise does not drown the signal. maxFiles bounds the walk's cost: past it
// the function stops early and reports the count, and callers skip the check
// rather than hashing a tree the size of a model zoo. The returned nfiles is
// the count actually seen — over the cap means "unbounded, not hashed".
func HashDir(root string, maxFiles int) (hash string, nfiles int, err error) {
	type entry struct {
		rel  string
		path string
	}
	var files []entry
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return "", 0, nil
		}
		return "", 0, err
	}
	if !info.IsDir() {
		return "", 0, nil
	}
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if snapshotPruneDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), "-wal") || strings.HasSuffix(d.Name(), "-shm") || strings.HasSuffix(d.Name(), "-journal") {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if maxFiles > 0 && len(files) >= maxFiles {
			return errTooManyFiles
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		files = append(files, entry{rel: filepath.ToSlash(rel), path: p})
		return nil
	})
	if walkErr == errTooManyFiles {
		return "", len(files) + 1, nil
	}
	if walkErr != nil {
		return "", 0, walkErr
	}
	if len(files) == 0 {
		return "", 0, nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	h := sha256.New()
	fh := sha256.New()
	buf := make([]byte, 1<<20)
	for _, f := range files {
		fh.Reset()
		fp, err := os.Open(f.path)
		if err != nil {
			return "", 0, err
		}
		_, copyErr := io.CopyBuffer(fh, fp, buf)
		fp.Close()
		if copyErr != nil {
			return "", 0, copyErr
		}
		h.Write([]byte(f.rel))
		h.Write([]byte{0})
		h.Write(fh.Sum(nil))
	}
	return hex.EncodeToString(h.Sum(nil)), len(files), nil
}

// errTooManyFiles bails the walk past maxFiles. A private sentinel rather
// than fs.SkipAll: WalkDir swallows SkipAll into a nil return, which would
// silently hash the truncated file set as if it were the whole tree.
var errTooManyFiles = errors.New("defense: file count over hash cap")

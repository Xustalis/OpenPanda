package defense

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Snapshot is a point-in-time record of the regular files under a directory,
// keyed by slash-separated path relative to the root and fingerprinted by size
// plus mtime. It is the deterministic basis for scope-drift detection (design
// §14.2 signal A): diffing a before and after snapshot reveals what an agent
// changed.
type Snapshot struct {
	files map[string]fileStamp
}

// fileStamp fingerprints one file. Size + mtime is a cheap deterministic
// heuristic; it can miss a rewrite that preserves both, which is acceptable
// for an MVP that errs on the side of "detect the obvious drift".
type fileStamp struct {
	size    int64
	modNano int64
}

// SnapshotDir walks root and records every regular file under it. Directories
// and the root itself are not recorded — only files can drift. A missing root
// is treated as empty (nothing to change), not an error, so an agent that has
// not created its working directory yet simply starts from an empty tree.
func SnapshotDir(root string) (Snapshot, error) {
	s := Snapshot{files: make(map[string]fileStamp)}
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	if !info.IsDir() {
		return s, nil
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// SQLite journal files (-wal/-shm) are transient host machine state: any
		// live SQLite connection in WAL mode writes them continuously, including
		// this node's own process while it runs the task. They carry no signal
		// about what the agent changed, so they are excluded from drift detection.
		if strings.HasSuffix(d.Name(), "-wal") || strings.HasSuffix(d.Name(), "-shm") {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		s.files[filepath.ToSlash(rel)] = fileStamp{size: fi.Size(), modNano: fi.ModTime().UnixNano()}
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

// Changed returns the slash-separated relative paths that differ between s and
// after: files added, removed, or modified (size or mtime changed).
func (s Snapshot) Changed(after Snapshot) []string {
	var out []string
	seen := make(map[string]bool, len(after.files))
	for p, st := range after.files {
		seen[p] = true
		if before, ok := s.files[p]; !ok || before != st {
			out = append(out, p)
		}
	}
	for p := range s.files {
		if !seen[p] {
			out = append(out, p) // removed
		}
	}
	return out
}

// SPDX-License-Identifier: AGPL-3.0-or-later

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
	// skipped counts entries the walk could not read (permission errors,
	// files deleted mid-walk). They are omitted from the diff, so a skipped
	// path is a blind spot, not a failure.
	skipped int
	// truncated reports that recording stopped at snapshotMaxFiles. WalkDir
	// visits entries in lexical order, so before/after snapshots truncate at
	// the same boundary and the recorded prefix still diffs honestly.
	truncated bool
}

// snapshotMaxFiles bounds how many files one snapshot records. Past the cap
// the walk stops entirely: supervision diffs the deterministic lexical prefix
// both snapshots share while the cost of watching a pathological workdir (an
// adopted project rooted at a whole home directory, say) stays finite. A var
// rather than a const so tests can shrink it without staging 100k files.
var snapshotMaxFiles = 100_000

// Skipped reports how many entries the walk could not stat.
func (s Snapshot) Skipped() int { return s.skipped }

// Truncated reports whether the walk stopped at the file cap.
func (s Snapshot) Truncated() bool { return s.truncated }

// fileStamp fingerprints one file. Size + mtime is a cheap deterministic
// heuristic; it can miss a rewrite that preserves both, which is acceptable
// for an MVP that errs on the side of "detect the obvious drift".
type fileStamp struct {
	size    int64
	modNano int64
}

// snapshotPruneDirs names directories skipped entirely during a snapshot walk.
// These hold vendored or machine-generated content that dwarfs the agent's own
// work — node_modules routinely carries tens of thousands of files — so walking
// them makes every supervision round pay an O(vendored-tree) tax for drift
// signal that is not actionable anyway: an agent that rewrites node_modules
// during a code task is a nuance this heuristic layer was never precise enough
// to adjudicate. Keeping the set conservative (dependency trees and Python
// caches only, NOT .git or build output) preserves the meaningful signals —
// an agent rewriting git history or build artifacts IS drift worth flagging.
var snapshotPruneDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"__pycache__":  true,
	".venv":        true,
	"venv":         true,
	// .panda-shadow holds the §5.2 preemption copies SaveShadow parks inside
	// the work tree. Hashing them would double-count the agent's own work,
	// and diffing them would flag bookkeeping as drift.
	".panda-shadow": true,
}

// SnapshotDir walks root and records every regular file under it. Directories
// and the root itself are not recorded — only files can drift. A missing root
// is treated as empty (nothing to change), not an error, so an agent that has
// not created its working directory yet simply starts from an empty tree.
// Directories named in snapshotPruneDirs are skipped wholesale: their contents
// are vendored/generated state, not agent work product.
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
			// An unreadable subtree or an entry deleted mid-walk must not
			// blind the whole check — on a permission-mixed tree (a project
			// adopted over a home directory hits .Trash immediately) the
			// first error otherwise discards every readable sibling.
			s.skipped++
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if snapshotPruneDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		// SQLite transient files (-wal/-shm/-journal) are host machine state: any
		// live SQLite connection writes them continuously, including this node's
		// own process while it runs the task. They carry no signal about what the
		// agent changed, so they are excluded from drift detection.
		if strings.HasSuffix(d.Name(), "-wal") || strings.HasSuffix(d.Name(), "-shm") || strings.HasSuffix(d.Name(), "-journal") {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			s.skipped++
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		s.files[filepath.ToSlash(rel)] = fileStamp{size: fi.Size(), modNano: fi.ModTime().UnixNano()}
		if len(s.files) >= snapshotMaxFiles {
			s.truncated = true
			return fs.SkipAll
		}
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

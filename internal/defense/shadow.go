package defense

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// shadow.go implements the §5.2 "影子工作副本" (shadow work copy): when
// arbitration preempts a task mid-edit, the files it owns — the ones inside
// its declared scope — are copied aside before the cancel lands. On the next
// run the shadow is merged back over whatever the winning task produced:
// files the winner never touched come back verbatim, files both sides wrote
// stay with the winner and are reported as conflicts for the resumed agent
// to re-apply. It is file-level three-way merge with the preempted tree as
// "ours", the live tree as "theirs", and no common base — the scope roots
// are what make "ours" decidable at all.

// shadowCaps bound what a preemption may park: a scoped tree bigger than
// this is left to die with the task rather than paying the copy, because a
// scope of that size is almost never what an agent edited.
const (
	shadowMaxFiles = 2000
	shadowMaxBytes = 64 << 20 // 64 MiB
)

// ShadowRoot is the per-task shadow directory inside workDir. It sits inside
// the tree (not a sibling) so a stage's isolated work dir keeps its own
// shadow, and it is in snapshotPruneDirs so neither drift detection nor the
// oscillation hash sees it.
func ShadowRoot(workDir, taskID string) string {
	return filepath.Join(workDir, ".panda-shadow", taskID)
}

// inScope reports whether a slash-relative path sits under any of the
// declared scope roots. An empty root set means "everything" — the caller
// only reaches SaveShadow for scoped tasks, but an unset scope must not
// silently shadow the whole tree.
func inScope(rel string, roots []string) bool {
	if len(roots) == 0 {
		return false
	}
	for _, r := range roots {
		r = strings.Trim(strings.TrimSpace(r), "/")
		if r == "" {
			continue
		}
		if rel == r || strings.HasPrefix(rel, r+"/") {
			return true
		}
	}
	return false
}

// SaveShadow copies every scoped regular file under workDir into
// ShadowRoot(workDir, taskID). Anything outside roots is ignored — the
// shadow exists to preserve what arbitration was fighting over, not to
// clone the machine. Over the file/byte caps the copy aborts and the shadow
// is removed: a partial shadow merged later would silently resurrect half a
// change set, which is worse than none.
func SaveShadow(workDir, taskID string, roots []string) error {
	dst := ShadowRoot(workDir, taskID)
	_ = os.RemoveAll(dst) // a stale shadow from an older preemption dies first
	if len(roots) == 0 {
		return nil
	}
	var files, bytes int64
	err := filepath.WalkDir(workDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if snapshotPruneDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(workDir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !inScope(rel, roots) {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		files++
		bytes += fi.Size()
		if files > shadowMaxFiles || bytes > shadowMaxBytes {
			return errShadowTooBig
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return copyFile(p, target, fi.Mode().Perm())
	})
	if err != nil {
		_ = os.RemoveAll(dst)
		return err
	}
	return nil
}

// MergeShadow folds a saved shadow back into workDir and reports what it
// did: restored lists paths that came back verbatim, conflicts lists paths
// where the live tree diverged from the shadow — the winner's version is
// kept and the path is surfaced so the resumed agent knows its edit there
// was lost and must be re-applied. The shadow directory is removed either
// way: a merge that half-failed must not be replayed, since a second merge
// could not tell shadow bytes from winner bytes anymore.
func MergeShadow(workDir, taskID string) (restored, conflicts []string, err error) {
	src := ShadowRoot(workDir, taskID)
	defer os.RemoveAll(src)
	info, err := os.Stat(src)
	if err != nil || !info.IsDir() {
		return nil, nil, nil
	}
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		live := filepath.Join(workDir, filepath.FromSlash(rel))
		same, cerr := sameFile(p, live)
		if cerr != nil && !os.IsNotExist(cerr) {
			return cerr
		}
		switch {
		case cerr == nil && same:
			// The winner left this path alone (or wrote identical bytes):
			// nothing to bring back.
		case os.IsNotExist(cerr):
			// Absent now — the preempted work returns.
			if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
				return err
			}
			fi, err := d.Info()
			if err != nil {
				return err
			}
			if err := copyFile(p, live, fi.Mode().Perm()); err != nil {
				return err
			}
			restored = append(restored, rel)
		default:
			// Present but different: the winning task owns the merge on
			// contested lines. Keep its bytes; the conflict list tells the
			// resumed agent which of its edits died.
			conflicts = append(conflicts, rel)
		}
		return nil
	})
	return restored, conflicts, err
}

// sameFile compares two paths by content hash. The error distinguishes
// "live missing" (os.IsNotExist) from "both read, differ" (nil error,
// same=false), which is what MergeShadow's three-way switch keys on.
func sameFile(a, b string) (bool, error) {
	ha, err := hashFile(a)
	if err != nil {
		return false, err
	}
	hb, err := hashFile(b)
	if err != nil {
		return false, err
	}
	return ha == hb, nil
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// errShadowTooBig bails the shadow walk past the file/byte caps — a scoped
// tree that large is left to die with the task rather than pay the copy.
var errShadowTooBig = errors.New("defense: shadow over size cap")

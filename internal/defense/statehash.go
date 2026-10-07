// SPDX-License-Identifier: AGPL-3.0-or-later

package defense

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
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

// errNotGitRepo is returned by GitFingerprint when root is not inside a git
// work tree — the caller then falls back to skipping the oscillation check,
// exactly as it does for an oversized non-repo tree.
var errNotGitRepo = errors.New("defense: not a git work tree")

// gitFingerprintDiffCap bounds each diff stream fed into the fingerprint.
// Past it the stream is marked truncated and cut: a deterministic prefix
// still separates A→B→A regressions without hashing an unbounded diff.
const gitFingerprintDiffCap = 16 << 20

// GitFingerprint fingerprints a git work tree for the §6.2 oscillation check
// when the directory walk exceeds maxFiles. It reads git's own index instead
// of every file — HEAD, the full porcelain status listing (-uall names every
// untracked file), and the staged + unstaged diff streams, so the actual
// content of tracked edits participates and an agent reverting earlier work
// still reproduces the earlier fingerprint. Untracked file contents are
// named but not hashed: a rewrite confined to untracked paths can fingerprint
// identically, the deliberate cost bound — the alternative today is skipping
// the check outright on any large repository.
func GitFingerprint(ctx context.Context, root string) (string, error) {
	gitExe, err := exec.LookPath("git")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	write := func(tag string, data []byte) {
		h.Write([]byte(tag))
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
	}
	// rev-parse doubles as the repo test and normalizes a nested workDir to
	// the work-tree top, so a task running in a subdirectory fingerprints
	// the same repo state as one at the root.
	top, err := gitOutput(ctx, gitExe, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", errNotGitRepo
	}
	write("top", top)
	// HEAD may not exist (unborn branch): the diffs below still cover index
	// and worktree against the empty tree.
	head, err := gitOutput(ctx, gitExe, root, "rev-parse", "--verify", "HEAD")
	if err != nil {
		head = nil
	}
	write("head", head)
	status, err := gitOutput(ctx, gitExe, root, "status", "--porcelain=v1", "-uall")
	if err != nil {
		return "", err
	}
	write("status", status)
	if err := gitDiffInto(ctx, gitExe, root, h, "cached", "diff", "--cached", "--"); err != nil {
		return "", err
	}
	if err := gitDiffInto(ctx, gitExe, root, h, "worktree", "diff", "--"); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// gitOutput runs git with args in dir and returns stdout.
func gitOutput(ctx context.Context, gitExe, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, gitExe, args...)
	cmd.Dir = dir
	return cmd.Output()
}

// gitDiffInto streams one git diff into h under tag, capping the bytes read
// and marking truncation so a huge diff stays a stable-but-bounded input.
func gitDiffInto(ctx context.Context, gitExe, dir string, h io.Writer, tag string, args ...string) error {
	cmd := exec.CommandContext(ctx, gitExe, args...)
	cmd.Dir = dir
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	h.Write([]byte(tag))
	h.Write([]byte{0})
	n, copyErr := io.Copy(h, io.LimitReader(out, gitFingerprintDiffCap))
	if n == gitFingerprintDiffCap {
		// Mark the cut, then drain the rest so git is never left blocked on
		// a full pipe when Wait runs.
		h.Write([]byte("\x00TRUNCATED\x00"))
		_, _ = io.Copy(io.Discard, out)
	}
	waitErr := cmd.Wait()
	if copyErr != nil {
		return copyErr
	}
	return waitErr
}

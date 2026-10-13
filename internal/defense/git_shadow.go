// SPDX-License-Identifier: AGPL-3.0-or-later

package defense

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var safeBranchRE = regexp.MustCompile(`[^a-zA-Z0-9_\-\.]`)

// GitShadowBranchName returns the sanitized shadow branch name for taskID.
func GitShadowBranchName(taskID string) string {
	clean := safeBranchRE.ReplaceAllString(taskID, "-")
	return "panda-shadow-" + clean
}

// GitBaselineBranchName returns the sanitized pre-task baseline branch name.
// The baseline is the "undo point" for an agent run on a real checkout: where
// the shadow captures interrupted work mid-flight, the baseline captures the
// clean before-state so a finished-but-wrong run can be rolled back.
func GitBaselineBranchName(taskID string) string {
	clean := safeBranchRE.ReplaceAllString(taskID, "-")
	return "panda-base-" + clean
}

// ErrNoGitBaseline reports that no pre-task baseline ref exists for the task —
// either it never ran on a git checkout, or the baseline was already consumed.
var ErrNoGitBaseline = errors.New("defense: no git baseline for task")

// IsGitRepo reports whether dir is inside or contains a valid Git repository.
func IsGitRepo(dir string) bool {
	if dir == "" {
		return false
	}
	gitPath := filepath.Join(dir, ".git")
	fi, err := os.Stat(gitPath)
	if err != nil {
		return false
	}
	return fi.IsDir() || fi.Mode().IsRegular() // regular file for git worktree/submodule
}

// gitAdminDir resolves the real git directory for workDir — including linked
// worktrees and submodules whose .git is a pointer file — so the scratch index
// used by the plumbing snapshots always lands somewhere writable. Returns ""
// when the layout cannot be resolved.
func gitAdminDir(gitExe, workDir string) string {
	out, err := exec.Command(gitExe, "-C", workDir, "rev-parse", "--absolute-git-dir").Output()
	if err == nil {
		if d := strings.TrimSpace(string(out)); d != "" {
			return d
		}
	}
	// --absolute-git-dir needs git ≥ 2.13; fall back to the classic layout.
	dotGit := filepath.Join(workDir, ".git")
	if fi, err := os.Stat(dotGit); err == nil && fi.IsDir() {
		return dotGit
	}
	return ""
}

// SaveGitShadow snapshots the scoped working tree changes into a Git shadow
// branch (refs/heads/panda-shadow-<taskID>) using Git plumbing. This isolates
// work mid-edit during preemption/negotiation without modifying the main index,
// working tree checkout, or active branch pointer.
func SaveGitShadow(workDir, taskID string, roots []string) (string, error) {
	return saveGitRef(workDir, taskID, GitShadowBranchName(taskID), "panda shadow snapshot for task "+taskID, roots)
}

// SaveGitBaseline snapshots the ENTIRE working tree state (tracked, staged and
// untracked files alike) into refs/heads/panda-base-<taskID> before an agent
// run begins. Unlike the shadow — which records half-done scoped work to
// survive a preemption — the baseline is the undo point a `panda task revert`
// restores: it must cover the whole tree, because scope declarations are
// advisory and an agent that drifts outside them should still be reversible.
// Re-running it for the same task id just moves the ref forward, so a retry
// re-baselines from the current state.
func SaveGitBaseline(workDir, taskID string) (string, error) {
	return saveGitRef(workDir, taskID, GitBaselineBranchName(taskID), "panda baseline snapshot for task "+taskID, nil)
}

// saveGitRef is the shared plumbing core of SaveGitShadow/SaveGitBaseline: it
// stages the working tree into a scratch index (leaving the user's real index
// untouched), writes a tree object, commits it on top of HEAD, and points the
// given ref at the result.
func saveGitRef(workDir, taskID, branch, msg string, roots []string) (string, error) {
	if !IsGitRepo(workDir) {
		return "", nil
	}
	gitExe, err := exec.LookPath("git")
	if err != nil {
		return "", nil
	}

	gitDir := gitAdminDir(gitExe, workDir)
	if gitDir == "" {
		// Unresolvable git layout (e.g. GIT_DIR pointing elsewhere); skip
		// plumbing rather than writing an index into the wrong place.
		return "", nil
	}
	tmpIndex := filepath.Join(gitDir, "panda-idx-"+safeBranchRE.ReplaceAllString(taskID, "-"))
	defer os.Remove(tmpIndex)

	// Seed the temporary index from HEAD before staging: an empty index would
	// make write-tree produce a tree containing ONLY the staged roots, and
	// MergeGitShadow's `diff HEAD ref` would then report every out-of-scope
	// repository file as deleted — flooding the merge with false conflicts.
	// On an unborn HEAD there is nothing to seed; the add below still captures
	// the working files.
	headCmd := exec.Command(gitExe, "-C", workDir, "rev-parse", "--verify", "HEAD")
	if headOut, herr := headCmd.Output(); herr == nil && len(bytes.TrimSpace(headOut)) > 0 {
		seedCmd := exec.Command(gitExe, "-C", workDir, "read-tree", "HEAD")
		seedCmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+tmpIndex)
		if out, serr := seedCmd.CombinedOutput(); serr != nil {
			return "", fmt.Errorf("git read-tree into shadow index: %w (%s)", serr, strings.TrimSpace(string(out)))
		}
	}

	// Stage changes into the temporary index file
	args := []string{"-C", workDir, "add", "-A"}
	if len(roots) > 0 {
		args = append(args, "--")
		args = append(args, roots...)
	} else {
		args = append(args, "--", ".")
	}
	cmd := exec.Command(gitExe, args...)
	cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+tmpIndex)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git add to shadow index: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	// Write the tree object from the temporary index
	writeCmd := exec.Command(gitExe, "-C", workDir, "write-tree")
	writeCmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+tmpIndex)
	treeOut, err := writeCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git write-tree: %w (%s)", err, strings.TrimSpace(string(treeOut)))
	}
	treeSHA := strings.TrimSpace(string(treeOut))
	if len(treeSHA) != 40 && len(treeSHA) != 64 {
		return "", fmt.Errorf("invalid tree SHA: %s", treeSHA)
	}

	// Resolve HEAD commit if available
	revCmd := exec.Command(gitExe, "-C", workDir, "rev-parse", "HEAD")
	headOut, err := revCmd.Output()
	var parentArgs []string
	if err == nil {
		parent := strings.TrimSpace(string(headOut))
		if len(parent) == 40 || len(parent) == 64 {
			parentArgs = []string{"-p", parent}
		}
	}

	// Create commit object
	commitMsg := fmt.Sprintf("panda shadow snapshot for task %s", taskID)
	commitArgs := append([]string{"-C", workDir, "-c", "user.name=OpenPanda", "-c", "user.email=openpanda@localhost", "commit-tree", treeSHA}, parentArgs...)
	commitArgs = append(commitArgs, "-m", commitMsg)
	commitCmd := exec.Command(gitExe, commitArgs...)
	commitOut, err := commitCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git commit-tree: %w (%s)", err, strings.TrimSpace(string(commitOut)))
	}
	commitSHA := strings.TrimSpace(string(commitOut))

	// Update ref to point to this commit
	refCmd := exec.Command(gitExe, "-C", workDir, "update-ref", "refs/heads/"+branch, commitSHA)
	if out, err := refCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git update-ref: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	return branch, nil
}

// MergeGitShadow restores files from the Git shadow branch upon resume,
// detecting any paths modified concurrently on HEAD as conflicts.
func MergeGitShadow(workDir, taskID string) (restored, conflicts []string, err error) {
	if !IsGitRepo(workDir) {
		return nil, nil, nil
	}
	gitExe, err := exec.LookPath("git")
	if err != nil {
		return nil, nil, nil
	}

	branch := GitShadowBranchName(taskID)
	ref := "refs/heads/" + branch

	// Check if the shadow ref exists
	verifyCmd := exec.Command(gitExe, "-C", workDir, "rev-parse", "--verify", ref)
	if err := verifyCmd.Run(); err != nil {
		return nil, nil, nil // No shadow branch found
	}

	// Clean up the shadow branch — but only when nothing was contested: a
	// conflicted path's shadow version lives solely on this ref, and deleting
	// it here would discard that copy before anyone can resolve the merge.
	defer func() {
		if len(conflicts) == 0 {
			_ = exec.Command(gitExe, "-C", workDir, "update-ref", "-d", ref).Run()
		}
	}()

	// Compare changed files between HEAD and the shadow branch
	diffCmd := exec.Command(gitExe, "-C", workDir, "diff", "--name-only", "HEAD", ref)
	diffOut, err := diffCmd.Output()
	if err != nil {
		return nil, nil, fmt.Errorf("git diff against shadow: %w", err)
	}

	// One porcelain scan for the whole tree replaces the old per-path probe —
	// a shadow with N contested files used to spawn N git processes.
	dirty, err := porcelainEntries(gitExe, workDir)
	if err != nil {
		return nil, nil, fmt.Errorf("git status for shadow merge: %w", err)
	}

	lines := bytes.Split(bytes.TrimSpace(diffOut), []byte("\n"))
	for _, line := range lines {
		path := strings.TrimSpace(string(line))
		if path == "" {
			continue
		}

		if _, contested := dirty[path]; contested {
			// File is currently modified or contested by another agent
			conflicts = append(conflicts, path)
		} else {
			// Safe to restore from shadow branch
			checkoutCmd := exec.Command(gitExe, "-C", workDir, "checkout", ref, "--", path)
			if err := checkoutCmd.Run(); err == nil {
				restored = append(restored, path)
			} else {
				conflicts = append(conflicts, path)
			}
		}
	}

	return restored, conflicts, nil
}

// porcelainEntries returns every path `git status --porcelain` reports for
// workDir mapped to its two-letter XY code — modified, staged, renamed and
// untracked ("??") alike. -z keeps filenames verbatim (no C-style quoting) so
// paths with spaces or non-ASCII bytes survive the round trip.
func porcelainEntries(gitExe, workDir string) (map[string]string, error) {
	// -uall expands untracked directories to their files. The default
	// collapse-to-dir mode is wrong for both callers: a `?? out/` entry
	// hides every contested file inside it (MergeGitShadow would clobber
	// them), and os.Remove on the dir entry itself fails on a non-empty
	// tree, silently leaking a task's whole output directory on rollback.
	out, err := exec.Command(gitExe, "-C", workDir, "status", "--porcelain", "-z", "-uall").Output()
	if err != nil {
		return nil, err
	}
	dirty := map[string]string{}
	fields := bytes.Split(out, []byte{0})
	for i := 0; i < len(fields); i++ {
		rec := fields[i]
		if len(rec) < 4 {
			continue
		}
		dirty[string(rec[3:])] = string(rec[:2])
		// Rename/copy records carry a second NUL-separated path (the source);
		// consume it so it is not mistaken for a record of its own.
		if rec[0] == 'R' || rec[0] == 'C' || rec[1] == 'R' || rec[1] == 'C' {
			i++
		}
	}
	return dirty, nil
}

// RollbackGitBaseline restores the working tree to the pre-task baseline saved
// by SaveGitBaseline: every path the ref records is written back over the
// worktree (modifications AND deletions), the index is reset to match, and
// files created after the baseline — absent from its tree — are deleted. The
// baseline ref is consumed on success so a second revert is a reported no-op
// rather than a silent one.
//
// Returns the relisted paths: restored = files written back from the ref,
// removed = post-baseline files deleted. A missing ref yields ErrNoGitBaseline
// so the CLI can say "nothing to roll back" instead of pretending success.
func RollbackGitBaseline(workDir, taskID string) (restored, removed []string, err error) {
	if !IsGitRepo(workDir) {
		return nil, nil, ErrNoGitBaseline
	}
	gitExe, err := exec.LookPath("git")
	if err != nil {
		return nil, nil, ErrNoGitBaseline
	}
	ref := "refs/heads/" + GitBaselineBranchName(taskID)
	if err := exec.Command(gitExe, "-C", workDir, "rev-parse", "--verify", ref).Run(); err != nil {
		return nil, nil, ErrNoGitBaseline
	}

	// The baseline tree is the complete pre-task file list (the snapshot's
	// `git add -A` covered untracked files too), so it is the membership test
	// for the report split below: a dirty path the baseline carries gets
	// written back (restored); an untracked one it does not is post-baseline
	// output (removed). The dirty set must be read BEFORE the restore —
	// afterwards every baseline path is clean and the signal is gone.
	baseSet := map[string]bool{}
	if out, lerr := exec.Command(gitExe, "-C", workDir, "ls-tree", "-r", "-z", "--name-only", ref).Output(); lerr == nil {
		for _, p := range bytes.Split(out, []byte{0}) {
			if len(p) > 0 {
				baseSet[string(p)] = true
			}
		}
	}
	dirty, err := porcelainEntries(gitExe, workDir)
	if err != nil {
		return nil, nil, fmt.Errorf("git status for baseline rollback: %w", err)
	}

	// Reset index+worktree to the baseline tree. `-- .` scopes the restore to
	// the whole tree; paths the ref does not know are untouched here and
	// handled by the untracked sweep below.
	restore := exec.Command(gitExe, "-C", workDir, "restore", "--source="+ref, "--staged", "--worktree", "--", ".")
	if out, rerr := restore.CombinedOutput(); rerr != nil {
		return nil, nil, fmt.Errorf("git restore from baseline: %w (%s)", rerr, strings.TrimSpace(string(out)))
	}

	for p, code := range dirty {
		if baseSet[p] {
			restored = append(restored, p)
			continue
		}
		if code != "??" {
			// A tracked-state entry absent from the baseline is a pre-baseline
			// deletion (already gone) — nothing to remove.
			continue
		}
		abs := filepath.Join(workDir, filepath.FromSlash(p))
		if rmErr := os.Remove(abs); rmErr == nil {
			removed = append(removed, p)
			// The file's death may empty the directory chain the task made
			// for it — a leftover `out/` shell reads like the rollback
			// missed it. os.Remove on a non-empty dir errors and stops the
			// climb, so restored baseline content up the chain is safe.
			for dir := filepath.Dir(abs); dir != workDir && strings.HasPrefix(dir, workDir); dir = filepath.Dir(dir) {
				if err := os.Remove(dir); err != nil {
					break
				}
			}
		}
	}
	sort.Strings(restored)
	sort.Strings(removed)

	_ = exec.Command(gitExe, "-C", workDir, "update-ref", "-d", ref).Run()
	return restored, removed, nil
}

// CleanupGitRefs drops every panda-owned ref a task may have left — the
// preemption shadow and the pre-task baseline. Best-effort: a missing ref, a
// non-repo dir, or an absent git binary all quietly no-op, so callers can run
// it unconditionally on task/session teardown.
func CleanupGitRefs(workDir, taskID string) {
	if !IsGitRepo(workDir) {
		return
	}
	gitExe, err := exec.LookPath("git")
	if err != nil {
		return
	}
	for _, branch := range []string{GitShadowBranchName(taskID), GitBaselineBranchName(taskID)} {
		_ = exec.Command(gitExe, "-C", workDir, "update-ref", "-d", "refs/heads/"+branch).Run()
	}
}

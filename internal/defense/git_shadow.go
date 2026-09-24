package defense

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var safeBranchRE = regexp.MustCompile(`[^a-zA-Z0-9_\-\.]`)

// GitShadowBranchName returns the sanitized shadow branch name for taskID.
func GitShadowBranchName(taskID string) string {
	clean := safeBranchRE.ReplaceAllString(taskID, "-")
	return "panda-shadow-" + clean
}

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

// SaveGitShadow snapshots the scoped working tree changes into a Git shadow
// branch (refs/heads/panda-shadow-<taskID>) using Git plumbing. This isolates
// work mid-edit during preemption/negotiation without modifying the main index,
// working tree checkout, or active branch pointer.
func SaveGitShadow(workDir, taskID string, roots []string) (string, error) {
	if !IsGitRepo(workDir) {
		return "", nil
	}
	gitExe, err := exec.LookPath("git")
	if err != nil {
		return "", nil
	}

	branch := GitShadowBranchName(taskID)
	dotGit := filepath.Join(workDir, ".git")
	if fi, err := os.Stat(dotGit); err != nil || !fi.IsDir() {
		// Not a standard .git dir (submodule/worktree); skip plumbing for safety
		return "", nil
	}

	tmpIndex := filepath.Join(dotGit, "panda-idx-"+safeBranchRE.ReplaceAllString(taskID, "-"))
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
	commitArgs := append([]string{"-C", workDir, "commit-tree", treeSHA}, parentArgs...)
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

	lines := bytes.Split(bytes.TrimSpace(diffOut), []byte("\n"))
	for _, line := range lines {
		path := strings.TrimSpace(string(line))
		if path == "" {
			continue
		}

		// Check if file was modified locally in working directory or differs from HEAD
		statusCmd := exec.Command(gitExe, "-C", workDir, "status", "--porcelain", "--", path)
		statOut, _ := statusCmd.Output()
		if len(bytes.TrimSpace(statOut)) > 0 {
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

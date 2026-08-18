// Git worktree management for sessions: each session that will touch files
// gets its own worktree + branch (panda/<session-id>) carved out of HEAD, so
// agent work is isolated from the user's checkout and reviewable as a diff —
// the model codex and claude code use for sandboxed work.
package sessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/executil"
)

// worktreeDir is the directory (inside the repo) where session worktrees
// live. It is added to .git/info/exclude so it never pollutes git status.
const worktreeDir = ".openpanda/worktrees"

// ErrNotARepo is returned when the work path is not inside a git work tree;
// the caller degrades to running without isolation.
var ErrNotARepo = errors.New("sessions: work path is not a git repository")

// ErrMergeConflict is returned by Merge when the session branch cannot be
// merged cleanly; the merge is aborted and the repository left untouched.
var ErrMergeConflict = errors.New("sessions: merge conflict (aborted, your checkout is unchanged)")

// maxDiffBytes caps the diff payload returned to the panel — a huge generated
// diff would otherwise stall the browser.
const maxDiffBytes = 128 * 1024

// Worktrees manages session worktrees under one repository.
type Worktrees struct {
	repo string // absolute path to the repository
}

// OpenWorktrees returns a Worktrees for repo, or ErrNotARepo when repo is not
// a git work tree.
func OpenWorktrees(repo string) (*Worktrees, error) {
	abs, err := filepath.Abs(repo)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("git", "-C", abs, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return nil, ErrNotARepo
	}
	return &Worktrees{repo: abs}, nil
}

// Branch is the isolation branch name for a session id.
func Branch(id string) string { return "panda/" + id }

// Path is the worktree filesystem path for a session id.
func (w *Worktrees) Path(id string) string {
	return filepath.Join(w.repo, worktreeDir, id)
}

// Ensure creates the session's worktree if absent and returns its path.
// The branch is created from the repository's current HEAD.
func (w *Worktrees) Ensure(ctx context.Context, id string) (string, error) {
	path := w.Path(id)
	if st, err := os.Stat(filepath.Join(path, ".git")); err == nil && !st.IsDir() {
		// git worktrees use a .git *file* pointing at the admin dir
		return path, nil
	} else if err == nil {
		return path, nil
	}
	if err := w.excludeWorktreeDir(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	cmd := executil.CommandContext(ctx, "git", "-C", w.repo, "worktree", "add", "-b", Branch(id), path, "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		// The branch may already exist from an earlier session run — attach a
		// worktree to it instead of failing.
		retry := executil.CommandContext(ctx, "git", "-C", w.repo, "worktree", "add", path, Branch(id))
		if out2, err2 := retry.CombinedOutput(); err2 != nil {
			return "", fmt.Errorf("sessions: git worktree add: %v / %s / %v", err, strings.TrimSpace(string(out)), wrapErr(err2, out2))
		}
	}
	return path, nil
}

func wrapErr(err error, out []byte) string {
	msg := strings.TrimSpace(string(out))
	if msg != "" {
		return msg
	}
	return err.Error()
}

// Change is one modified path in a session worktree relative to its HEAD:
// Status is the porcelain code (M/A/D/??…), Path is repo-relative.
type Change struct {
	Status string `json:"status"`
	Path   string `json:"path"`
}

// Status lists every change in the session worktree — staged, unstaged, and
// untracked — so the panel can show "what did this session do" before merging.
// Returns an empty slice (not nil-safe error) for a session without changes.
func (w *Worktrees) Status(ctx context.Context, id string) ([]Change, error) {
	if err := w.ensureExists(id); err != nil {
		return nil, err
	}
	cmd := executil.CommandContext(ctx, "git", "-C", w.Path(id), "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("sessions: git status: %w", err)
	}
	var changes []Change
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		changes = append(changes, Change{
			Status: strings.TrimSpace(line[:2]),
			Path:   line[3:],
		})
	}
	return changes, nil
}

// Diff returns the unified diff of the worktree's working tree (tracked and
// untracked files) against its HEAD, capped at maxDiffBytes.
func (w *Worktrees) Diff(ctx context.Context, id string) (string, error) {
	if err := w.ensureExists(id); err != nil {
		return "", err
	}
	wt := w.Path(id)
	// Stage nothing permanently — use a scratch index? Simpler and safe here:
	// `git add -A` is idempotent for diffing purposes and Merge re-adds anyway.
	// But avoid mutating state on a read path: use `git diff HEAD` plus
	// per-untracked-file diffs against /dev/null.
	buf := &strings.Builder{}
	if out, err := executil.CommandContext(ctx, "git", "-C", wt, "diff", "HEAD", "--no-color").Output(); err == nil {
		buf.Write(out)
	}
	untracked, err := executil.CommandContext(ctx, "git", "-C", wt, "ls-files", "--others", "--exclude-standard").Output()
	if err != nil {
		return "", fmt.Errorf("sessions: git ls-files: %w", err)
	}
	for _, f := range strings.Split(strings.TrimSpace(string(untracked)), "\n") {
		if f == "" {
			continue
		}
		out, err := executil.CommandContext(ctx, "git", "-C", wt, "diff", "--no-color", "--no-index", "--", "/dev/null", f).Output()
		if err != nil && len(out) == 0 { // diff exits 1 when files differ; output is still valid
			return "", fmt.Errorf("sessions: git diff --no-index %s: %w", f, err)
		}
		buf.Write(out)
	}
	diff := buf.String()
	if len(diff) > maxDiffBytes {
		diff = diff[:maxDiffBytes] + "\n… (diff truncated)"
	}
	return diff, nil
}

// Merge lands the session's work on the repository's current branch: any
// uncommitted changes in the worktree are committed on the session branch,
// then the branch is merged into HEAD. On conflict the merge is aborted and
// ErrMergeConflict returned — the checkout is left exactly as it was. The
// return value is the merge commit's subject ("" for an up-to-date no-op).
func (w *Worktrees) Merge(ctx context.Context, id string, message string) (string, error) {
	if err := w.ensureExists(id); err != nil {
		return "", err
	}
	wt := w.Path(id)
	// Commit uncommitted session work on its branch first. The commit identity
	// is fixed via -c flags so a machine without git user.name configured can
	// still merge.
	add := executil.CommandContext(ctx, "git", "-C", wt, "add", "-A")
	if out, err := add.CombinedOutput(); err != nil {
		return "", fmt.Errorf("sessions: git add: %s", wrapErr(err, out))
	}
	staged := executil.CommandContext(ctx, "git", "-C", wt, "diff", "--cached", "--quiet", "HEAD")
	if err := staged.Run(); err != nil { // exit 1 = something staged, commit it
		if message == "" {
			message = "openpanda session " + id
		}
		commit := executil.CommandContext(ctx, "git", "-C", wt,
			"-c", "user.name=OpenPanda", "-c", "user.email=openpanda@localhost",
			"commit", "-m", message)
		if out, err := commit.CombinedOutput(); err != nil {
			return "", fmt.Errorf("sessions: git commit: %s", wrapErr(err, out))
		}
	}
	merge := executil.CommandContext(ctx, "git", "-C", w.repo, "merge", "--no-edit", Branch(id))
	if out, err := merge.CombinedOutput(); err != nil {
		abort := executil.CommandContext(ctx, "git", "-C", w.repo, "merge", "--abort")
		_, _ = abort.CombinedOutput()
		if strings.Contains(strings.ToUpper(string(out)), "CONFLICT") ||
			strings.Contains(string(out), "nothing to commit") {
			return "", ErrMergeConflict
		}
		return "", fmt.Errorf("sessions: git merge: %s", wrapErr(err, out))
	}
	subject, err := executil.CommandContext(ctx, "git", "-C", w.repo, "log", "-1", "--format=%s").Output()
	if err != nil {
		return "", nil // merge succeeded; subject is cosmetic
	}
	return strings.TrimSpace(string(subject)), nil
}

// ensureExists verifies the session worktree is present (created by Ensure
// during the session's first ask).
func (w *Worktrees) ensureExists(id string) error {
	if _, err := os.Stat(w.Path(id)); err != nil {
		return fmt.Errorf("sessions: no worktree for session %s (run a prompt first)", id)
	}
	return nil
}

// Remove deletes the session's worktree and its branch. Uncommitted changes
// are discarded (the worktree was the session's sandbox).
func (w *Worktrees) Remove(ctx context.Context, id string) error {
	path := w.Path(id)
	if _, err := os.Stat(path); err == nil {
		cmd := executil.CommandContext(ctx, "git", "-C", w.repo, "worktree", "remove", "--force", path)
		if _, err := cmd.CombinedOutput(); err != nil {
			// Fall back to pruning a half-deleted worktree.
			_ = os.RemoveAll(path)
			prune := executil.CommandContext(ctx, "git", "-C", w.repo, "worktree", "prune")
			_, _ = prune.CombinedOutput()
		}
	}
	del := executil.CommandContext(ctx, "git", "-C", w.repo, "branch", "-D", Branch(id))
	_, _ = del.CombinedOutput()
	return nil
}

// excludeWorktreeDir appends the worktree directory to .git/info/exclude (not
// the user's .gitignore) so it stays out of git status without touching
// tracked files.
func (w *Worktrees) excludeWorktreeDir() error {
	admin, err := exec.Command("git", "-C", w.repo, "rev-parse", "--git-dir").Output()
	if err != nil {
		return fmt.Errorf("sessions: rev-parse --git-dir: %w", err)
	}
	gitDir := strings.TrimSpace(string(admin))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(w.repo, gitDir)
	}
	excludePath := filepath.Join(gitDir, "info", "exclude")
	existing, _ := os.ReadFile(excludePath)
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(line) == worktreeDir+"/" {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(f, "# openpanda session worktrees\n%s/\n", worktreeDir)
	return err
}

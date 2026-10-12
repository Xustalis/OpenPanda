// SPDX-License-Identifier: AGPL-3.0-or-later

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
	"regexp"
	"strconv"
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
	return w.EnsureFrom(ctx, id, "HEAD")
}

// EnsureFrom is Ensure with an explicit base ref: a forked session branches
// its worktree off the parent's branch so it inherits the parent's code
// state, while a fresh session branches off HEAD.
func (w *Worktrees) EnsureFrom(ctx context.Context, id, base string) (string, error) {
	if !ValidID(id) {
		return "", ErrNotFound
	}
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
	if base == "" {
		base = "HEAD"
	}
	cmd := executil.CommandContext(ctx, "git", "-C", w.repo, "worktree", "add", "-b", Branch(id), path, base)
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
// then the branch is merged into HEAD. The merge is always a real merge
// commit (--no-ff): a fast-forward would smear the session's commits into the
// mainline with no boundary to undo, and Revert relies on that boundary. On
// conflict the merge is aborted and ErrMergeConflict returned — the checkout
// is left exactly as it was. The return value is the merge commit's subject
// ("" for an up-to-date no-op).
//
// Every merge that moves HEAD records two refs for Revert:
// refs/openpanda/merged/<id> (the merge commit) and
// refs/openpanda/premerge/<id> (the pre-merge HEAD). A no-op merge touches
// neither — overwriting premerge with the previous merge commit would erase
// the only copy of the rollback target.
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
	preSHA := w.revParse(ctx, "HEAD")
	merge := executil.CommandContext(ctx, "git", "-C", w.repo, "-c", "user.name=OpenPanda", "-c", "user.email=openpanda@localhost", "merge", "--no-ff", "--no-edit", Branch(id))
	if out, err := merge.CombinedOutput(); err != nil {
		abort := executil.CommandContext(ctx, "git", "-C", w.repo, "merge", "--abort")
		_, _ = abort.CombinedOutput()
		if strings.Contains(strings.ToUpper(string(out)), "CONFLICT") ||
			strings.Contains(string(out), "nothing to commit") {
			return "", ErrMergeConflict
		}
		return "", fmt.Errorf("sessions: git merge: %s", wrapErr(err, out))
	}
	if mergedSHA := w.revParse(ctx, "HEAD"); mergedSHA != "" && mergedSHA != preSHA {
		w.writeRef(ctx, mergedRef(id), mergedSHA)
		if preSHA != "" {
			w.writeRef(ctx, premergeRef(id), preSHA)
		}
	}
	subject, err := executil.CommandContext(ctx, "git", "-C", w.repo, "log", "-1", "--format=%s").Output()
	if err != nil {
		return "", nil // merge succeeded; subject is cosmetic
	}
	return strings.TrimSpace(string(subject)), nil
}

// mergedRef / premergeRef are the rollback bookkeeping refs a successful
// Merge writes — see Merge's doc. They live under refs/openpanda/ so `git
// branch -a` stays clean while gc still treats them as reachable roots.
func mergedRef(id string) string   { return "refs/openpanda/merged/" + safeRefPart(id) }
func premergeRef(id string) string { return "refs/openpanda/premerge/" + safeRefPart(id) }

var unsafeRefChars = regexp.MustCompile(`[^a-zA-Z0-9_\-]`)

func safeRefPart(id string) string { return unsafeRefChars.ReplaceAllString(id, "-") }

// ErrNothingToRevert is Revert's answer when no recorded merge exists for the
// session — never merged, already reverted, or merged before the bookkeeping
// refs existed.
var ErrNothingToRevert = errors.New("sessions: no recorded merge to revert")

// ErrRevertDirty is Revert's refusal when the checkout has uncommitted work:
// both rollback paths (reset --hard, git revert) would entangle it.
var ErrRevertDirty = errors.New("sessions: checkout has uncommitted changes — commit or stash them first")

// Revert undoes a previously recorded session Merge. When HEAD is still the
// merge commit the rollback is a clean `git reset --hard <pre-merge>`; when
// later commits sit on top it becomes a `git revert -m 1` of the merge commit
// instead, so intervening work survives. Returns the resulting HEAD subject.
func (w *Worktrees) Revert(ctx context.Context, id string) (string, error) {
	if !ValidID(id) {
		return "", ErrNotFound
	}
	merged := w.revParse(ctx, mergedRef(id))
	if merged == "" {
		return "", ErrNothingToRevert
	}
	dirty, err := executil.CommandContext(ctx, "git", "-C", w.repo, "status", "--porcelain").Output()
	if err == nil && strings.TrimSpace(string(dirty)) != "" {
		return "", ErrRevertDirty
	}
	head := w.revParse(ctx, "HEAD")
	pre := w.revParse(ctx, premergeRef(id))
	if pre == "" {
		// Bookkeeping from an older build: the merge commit's first parent IS
		// the pre-merge state by definition.
		pre = w.revParse(ctx, merged+"^1")
	}
	switch {
	case head == merged && pre != "":
		// Nothing landed on top of the merge — erasing it is exact.
		if out, err := executil.CommandContext(ctx, "git", "-C", w.repo, "reset", "--hard", pre).CombinedOutput(); err != nil {
			return "", fmt.Errorf("sessions: git reset: %s", wrapErr(err, out))
		}
	case w.isAncestor(ctx, merged, "HEAD"):
		// Later work sits on top: revert the merge commit so history and the
		// follow-up commits stay intact. A non-merge commit (a fast-forward
		// recorded before --no-ff) reverts plainly.
		argv := []string{"-C", w.repo, "-c", "user.name=OpenPanda", "-c", "user.email=openpanda@localhost", "revert", "--no-edit"}
		if len(w.parents(ctx, merged)) > 1 {
			argv = append(argv, "-m", "1")
		}
		argv = append(argv, merged)
		if out, err := executil.CommandContext(ctx, "git", argv...).CombinedOutput(); err != nil {
			return "", fmt.Errorf("sessions: git revert: %s", wrapErr(err, out))
		}
	default:
		return "", errors.New("sessions: merge commit is no longer on this branch — history was rewritten; revert by hand")
	}
	_ = executil.CommandContext(ctx, "git", "-C", w.repo, "update-ref", "-d", mergedRef(id)).Run()
	_ = executil.CommandContext(ctx, "git", "-C", w.repo, "update-ref", "-d", premergeRef(id)).Run()
	subject := ""
	if out, err := executil.CommandContext(ctx, "git", "-C", w.repo, "log", "-1", "--format=%s").Output(); err == nil {
		subject = strings.TrimSpace(string(out))
	}
	return subject, nil
}

func (w *Worktrees) revParse(ctx context.Context, rev string) string {
	out, err := executil.CommandContext(ctx, "git", "-C", w.repo, "rev-parse", "--verify", rev).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (w *Worktrees) writeRef(ctx context.Context, ref, sha string) {
	_ = executil.CommandContext(ctx, "git", "-C", w.repo, "update-ref", ref, sha).Run()
}

func (w *Worktrees) isAncestor(ctx context.Context, anc, desc string) bool {
	return executil.CommandContext(ctx, "git", "-C", w.repo, "merge-base", "--is-ancestor", anc, desc).Run() == nil
}

func (w *Worktrees) parents(ctx context.Context, rev string) []string {
	out, err := executil.CommandContext(ctx, "git", "-C", w.repo, "rev-list", "--parents", "-n", "1", rev).Output()
	if err != nil {
		return nil
	}
	if f := strings.Fields(string(out)); len(f) > 1 {
		return f[1:]
	}
	return nil
}

// DiffStat summarizes what a session's worktree would land on merge: the
// numstat roll-up plus the risk signals a reviewer wants BEFORE merging —
// sensitive paths (.env, keys), binary files line counts can't describe, and
// deletions, which are the changes that hurt most when wrong.
type DiffStat struct {
	FilesChanged int      `json:"files_changed"`
	Insertions   int      `json:"insertions"`
	Deletions    int      `json:"deletions"`
	Untracked    int      `json:"untracked"`
	Binary       []string `json:"binary,omitempty"`
	Sensitive    []string `json:"sensitive,omitempty"`
}

// sensitiveFileNames flags exact basenames whose merge should never be silent:
// credential material and environment files that agents love to "helpfully"
// touch.
var sensitiveFileNames = map[string]bool{
	".env": true, ".env.local": true, ".envrc": true,
	"id_rsa": true, "id_dsa": true, "id_ecdsa": true, "id_ed25519": true,
	".netrc": true, ".kubeconfig": true, ".htpasswd": true,
	"credentials": true, "secrets": true, "shadow": true,
}

// sensitiveFileExts flags extension-based credential material.
var sensitiveFileExts = map[string]bool{
	".pem": true, ".key": true, ".p12": true, ".pfx": true,
	".jks": true, ".keystore": true, ".kdbx": true,
}

// isSensitivePath reports whether path names a file a merge should flag:
// an exact sensitive basename, a credential extension, or an ".env" variant
// (.env.production, prod.env).
func isSensitivePath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if sensitiveFileNames[base] || sensitiveFileExts[filepath.Ext(base)] {
		return true
	}
	return strings.HasPrefix(base, ".env.") || strings.HasSuffix(base, ".env")
}

// Stat computes the merge-preview summary for the session worktree: changed
// tracked files with +/- counts, untracked file count, and the flagged paths.
// It reads exactly what Merge would commit — `git add -A` semantics — so the
// preview is honest about untracked files, which plain `git diff` hides.
func (w *Worktrees) Stat(ctx context.Context, id string) (DiffStat, error) {
	var st DiffStat
	if err := w.ensureExists(id); err != nil {
		return st, err
	}
	wt := w.Path(id)
	out, err := executil.CommandContext(ctx, "git", "-C", wt, "diff", "HEAD", "--numstat", "-z").Output()
	if err != nil {
		return st, fmt.Errorf("sessions: git numstat: %w", err)
	}
	seen := map[string]bool{}
	fields := strings.Split(string(out), "\x00")
	for i := 0; i < len(fields); i++ {
		rec := fields[i]
		if rec == "" {
			continue
		}
		f := strings.SplitN(rec, "\t", 3)
		if len(f) != 3 {
			continue
		}
		path := f[2]
		if path == "" {
			// -z rename record: "<add>\t<del>\t\0<new>\0<old>" — the pathname
			// pair follows as the next two fields.
			if i+1 < len(fields) {
				path = fields[i+1]
			}
			i += 2
		}
		if path == "" {
			continue
		}
		st.FilesChanged++
		seen[path] = true
		if f[0] == "-" { // binary files report -/-
			st.Binary = append(st.Binary, path)
		} else {
			if n, _ := strconv.Atoi(f[0]); n > 0 {
				st.Insertions += n
			}
			if n, _ := strconv.Atoi(f[1]); n > 0 {
				st.Deletions += n
			}
		}
		if isSensitivePath(path) {
			st.Sensitive = append(st.Sensitive, path)
		}
	}
	untracked, err := executil.CommandContext(ctx, "git", "-C", wt, "ls-files", "-z", "--others", "--exclude-standard").Output()
	if err != nil {
		return st, fmt.Errorf("sessions: git ls-files: %w", err)
	}
	for _, p := range strings.Split(string(untracked), "\x00") {
		if p == "" || seen[p] {
			continue
		}
		st.Untracked++
		if isSensitivePath(p) {
			st.Sensitive = append(st.Sensitive, p)
		}
	}
	return st, nil
}

// ensureExists verifies the session worktree is present (created by Ensure
// during the session's first ask).
func (w *Worktrees) ensureExists(id string) error {
	if !ValidID(id) {
		return ErrNotFound
	}
	if _, err := os.Stat(w.Path(id)); err != nil {
		return fmt.Errorf("sessions: no worktree for session %s (run a prompt first)", id)
	}
	return nil
}

// Remove deletes the session's worktree and its branch. Uncommitted changes
// are discarded (the worktree was the session's sandbox).
func (w *Worktrees) Remove(ctx context.Context, id string) error {
	if !ValidID(id) {
		return ErrNotFound
	}
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
	// Rollback bookkeeping must not outlive the session it describes.
	_ = executil.CommandContext(ctx, "git", "-C", w.repo, "update-ref", "-d", mergedRef(id)).Run()
	_ = executil.CommandContext(ctx, "git", "-C", w.repo, "update-ref", "-d", premergeRef(id)).Run()
	return nil
}

// excludeWorktreeDir appends the worktree directory to .git/info/exclude (not
// the user's .gitignore) so it stays out of git status without touching
// tracked files.
func (w *Worktrees) excludeWorktreeDir() error {
	// --git-common-dir, not --git-dir: inside a linked worktree the latter is
	// the worktree's own admin dir, whose info/exclude does not apply.
	admin, err := exec.Command("git", "-C", w.repo, "rev-parse", "--git-common-dir").Output()
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

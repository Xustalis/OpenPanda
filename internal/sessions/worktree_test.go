// SPDX-License-Identifier: AGPL-3.0-or-later

package sessions

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newGitRepo creates a temporary git repository with one commit and a git
// identity configured for the test process.
func newGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"git", "init", "-q", "-b", "main", dir},
		{"git", "-C", dir, "-c", "user.name=T", "-c", "user.email=t@t", "commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestWorktreeStatusDiffMerge(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := newGitRepo(t)
	w, err := OpenWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// A session worktree exists after Ensure and starts clean.
	id := "test-session"
	if _, err := w.Ensure(ctx, id); err != nil {
		t.Fatal(err)
	}
	changes, err := w.Status(ctx, id)
	if err != nil || len(changes) != 0 {
		t.Fatalf("fresh worktree changes = %v, err = %v", changes, err)
	}

	// Untracked + modified files appear in Status and Diff.
	wt := w.Path(id)
	if err := os.WriteFile(filepath.Join(wt, "new.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("doc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changes, err = w.Status(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %v, want 2 entries", changes)
	}
	patch, err := w.Diff(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(patch, "+hello") || !strings.Contains(patch, "new.txt") {
		t.Fatalf("patch missing new file content:\n%s", patch)
	}

	// Merge lands the session work on the repository branch.
	subject, err := w.Merge(ctx, id, "test merge")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if subject == "" {
		t.Fatal("merge subject is empty")
	}
	if _, err := os.Stat(filepath.Join(repo, "new.txt")); err != nil {
		t.Fatal("merged file missing from repository checkout")
	}
	// After merge the worktree is clean again.
	changes, err = w.Status(ctx, id)
	if err != nil || len(changes) != 0 {
		t.Fatalf("post-merge changes = %v, err = %v", changes, err)
	}
	// And merging again is a clean no-op.
	if _, err := w.Merge(ctx, id, ""); err != nil {
		t.Fatalf("second merge should be a no-op, got %v", err)
	}
}

// TestWorktreeMergeRevert exercises the rollback contract end to end: merge
// records its refs, head-on-the-merge resets cleanly to pre-merge HEAD, and a
// second commit on top switches Revert to `git revert -m 1` so intervening
// work survives.
func TestWorktreeMergeRevert(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := newGitRepo(t)
	w, err := OpenWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := "test-revert"

	if _, err := w.Ensure(ctx, id); err != nil {
		t.Fatal(err)
	}
	wt := w.Path(id)
	if err := os.WriteFile(filepath.Join(wt, "session.txt"), []byte("session work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Merge(ctx, id, "session merge"); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "session.txt")); err != nil {
		t.Fatal("merged file missing")
	}

	// Reverting at the merge tip is an exact rollback to pre-merge HEAD.
	subject, err := w.Revert(ctx, id)
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "session.txt")); !os.IsNotExist(err) {
		t.Fatal("reverted merge left session.txt behind")
	}
	gitLog := func() string {
		out, _ := exec.Command("git", "-C", repo, "log", "-1", "--format=%s").Output()
		return strings.TrimSpace(string(out))
	}
	if subject == "" || gitLog() == "" {
		t.Fatal("revert returned empty subject")
	}

	// Merge again, then land an unrelated commit on top: the revert must keep
	// the new commit and still remove the session's work.
	if err := os.WriteFile(filepath.Join(wt, "session2.txt"), []byte("again\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Merge(ctx, id, "session merge 2"); err != nil {
		t.Fatalf("merge 2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "after.txt"), []byte("later work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("add", "after.txt")
	run("-c", "user.name=T", "-c", "user.email=t@t", "commit", "-m", "after")
	if _, err := w.Revert(ctx, id); err != nil {
		t.Fatalf("revert with commits on top: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "after.txt")); err != nil {
		t.Fatal("revert dropped the intervening commit's file")
	}
	if _, err := os.Stat(filepath.Join(repo, "session2.txt")); !os.IsNotExist(err) {
		t.Fatal("revert left session2.txt behind")
	}

	// A third revert is a reported no-op, not silent success.
	if _, err := w.Revert(ctx, id); !errors.Is(err, ErrNothingToRevert) {
		t.Fatalf("third revert err = %v, want ErrNothingToRevert", err)
	}
}

// TestWorktreeStat covers the merge preview: line counts, sensitive-path
// flags and untracked accounting.
func TestWorktreeStat(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := newGitRepo(t)
	w, err := OpenWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// One tracked file to diff against: the fixture commits empty, so commit
	// tracked.txt before the worktree is carved.
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "tracked.txt"}, {"-c", "user.name=T", "-c", "user.email=t@t", "commit", "-m", "add tracked"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	id := "test-stat"
	if _, err := w.Ensure(ctx, id); err != nil {
		t.Fatal(err)
	}
	wt := w.Path(id)
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(wt, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("code.go", "package x\nfunc F() {}\n")
	write(".env", "SECRET=hunter2\n")
	write("tracked.txt", "v2\n")

	st, err := w.Stat(ctx, id)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.FilesChanged != 1 || st.Untracked != 2 {
		t.Fatalf("stat = %+v, want 1 changed + 2 untracked", st)
	}
	if st.Insertions == 0 {
		t.Fatalf("stat = %+v, want insertions", st)
	}
	found := false
	for _, p := range st.Sensitive {
		if p == ".env" {
			found = true
		}
	}
	if !found {
		t.Fatalf("sensitive = %v, want .env flagged", st.Sensitive)
	}
}

// TestEnsureRecoversStaleRegistration covers the crash debris case: the
// worktree directory is lost (or half-deleted) while git still holds its
// .git/worktrees/<id> registration and the panda/<id> branch. Every add
// fails until the registration is pruned — Ensure must recover on its own
// instead of refusing the session forever.
func TestEnsureRecoversStaleRegistration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := newGitRepo(t)
	w, err := OpenWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := "stale-session"

	path, err := w.Ensure(ctx, id)
	if err != nil {
		t.Fatalf("initial ensure: %v", err)
	}

	// Crash simulation one: the directory is deleted outright but the
	// worktree registration and branch survive.
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Ensure(ctx, id); err != nil {
		t.Fatalf("ensure after directory loss: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		t.Fatalf("worktree not recreated: %v", err)
	}

	// Crash simulation two: the directory is half-deleted — debris without
	// a .git marker — on top of the same stale registration.
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "debris.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Ensure(ctx, id); err != nil {
		t.Fatalf("ensure over debris dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		t.Fatalf("worktree not recreated over debris: %v", err)
	}
	// The debris is gone — it was crash residue, never session state.
	if _, err := os.Stat(filepath.Join(path, "debris.txt")); !os.IsNotExist(err) {
		t.Fatal("debris survived the recreation")
	}
}

func TestWorktreeStatusWithoutWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	w, err := OpenWorktrees(newGitRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Status(context.Background(), "never-created"); err == nil {
		t.Fatal("Status on a missing worktree should fail")
	}
}

func TestWorktreeRejectsTraversalIDs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	w, err := OpenWorktrees(newGitRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range []string{"..", "../escape", "a/b"} {
		if _, err := w.Ensure(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Ensure(%q) err = %v, want ErrNotFound", id, err)
		}
		if err := w.Remove(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Remove(%q) err = %v, want ErrNotFound", id, err)
		}
		if _, err := w.Status(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Status(%q) err = %v, want ErrNotFound", id, err)
		}
	}
}

// TestWorktreeExcludesOpenPandaRuntime pins the merge boundary: composer
// uploads live under .openpanda/uploads inside the worktree, and Merge runs
// `git add -A`. Only .openpanda/worktrees used to be excluded, so a pasted
// screenshot or secret was committed and merged into the user's branch.
func TestWorktreeExcludesOpenPandaRuntime(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := newGitRepo(t)
	w, err := OpenWorktrees(repo)
	if err != nil {
		t.Fatalf("open worktrees: %v", err)
	}
	ctx := context.Background()
	wt, err := w.Ensure(ctx, "excl-runtime")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	exclude, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("read exclude: %v", err)
	}
	if !strings.Contains(string(exclude), ".openpanda/") {
		t.Fatalf("runtime tree not excluded: %s", exclude)
	}
	uploads := filepath.Join(wt, ".openpanda", "uploads")
	if err := os.MkdirAll(uploads, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uploads, "secret.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := w.Status(ctx, "excl-runtime")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, c := range st {
		if strings.Contains(c.Path, ".openpanda") {
			t.Fatalf("runtime file reported as a session change: %+v", c)
		}
	}
}

// gitIn runs one git command in dir with a fixed identity, so a machine
// without a global git user can still commit inside a test repository.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	all := append([]string{"-C", dir, "-c", "user.name=T", "-c", "user.email=t@t"}, args...)
	cmd := exec.Command("git", all...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestWorktreeForkCarriesUncommittedWork pins what a fork inherits. Branching
// the child off the parent's branch carried only committed work: a reader who
// forked while the agent's edits were still uncommitted — the usual moment to
// fork — got a transcript describing files the child could not see.
func TestWorktreeForkCarriesUncommittedWork(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := newGitRepo(t)
	w, err := OpenWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	parent := "fork-parent"
	parentPath, err := w.Ensure(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"tracked.txt": "baseline\n", "gone.txt": "delete me\n"} {
		if err := os.WriteFile(filepath.Join(parentPath, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, parentPath, "add", "-A")
	gitIn(t, parentPath, "commit", "-qm", "baseline")

	// The state a fork is made from: one modified file, one deletion, one
	// brand-new file the index has never seen.
	if err := os.WriteFile(filepath.Join(parentPath, "tracked.txt"), []byte("uncommitted edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(parentPath, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parentPath, "new.txt"), []byte("brand new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	childPath, err := w.Fork(ctx, parent, "fork-child", Branch(parent))
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(childPath, "tracked.txt")); err != nil || string(got) != "uncommitted edit\n" {
		t.Fatalf("modified file not inherited: %q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(childPath, "new.txt")); err != nil || string(got) != "brand new\n" {
		t.Fatalf("untracked file not inherited: %q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(childPath, "gone.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted file came back in the fork: err=%v", err)
	}
	// The changes are uncommitted in the child too: the diff drawer and Merge
	// read them from the working tree, not from the branch.
	changes, err := w.Status(ctx, "fork-child")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	seen := map[string]string{}
	for _, c := range changes {
		seen[c.Path] = c.Status
	}
	for path, want := range map[string]string{"tracked.txt": "M", "new.txt": "??", "gone.txt": "D"} {
		if seen[path] != want {
			t.Fatalf("child status %q = %q, want %q (all: %+v)", path, seen[path], want, changes)
		}
	}

	// Positive control: once the parent commits, the branch base alone carries
	// the same files — the copy above is what the fork was missing, not a
	// replacement for branch inheritance. Line endings are compared loosely
	// here: this path goes through a checkout, which honours core.autocrlf,
	// while the byte copy above does not.
	gitIn(t, parentPath, "add", "-A")
	gitIn(t, parentPath, "commit", "-qm", "parent edits")
	committedPath, err := w.Fork(ctx, parent, "fork-child-committed", Branch(parent))
	if err != nil {
		t.Fatalf("fork after commit: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(committedPath, "tracked.txt")); err != nil || normalizeEOL(string(got)) != "uncommitted edit\n" {
		t.Fatalf("committed parent state lost: %q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(committedPath, "new.txt")); err != nil || normalizeEOL(string(got)) != "brand new\n" {
		t.Fatalf("committed new file lost: %q err=%v", got, err)
	}
}

// normalizeEOL compares text the way git does under core.autocrlf.
func normalizeEOL(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

// TestWorktreeForkSkipsRuntimeTree: .openpanda/ holds the parent's uploads and
// task scratch. Those bytes belong to the run that made them, not to the code
// state a fork continues, and copying them would put one session's pasted
// files inside another's worktree.
func TestWorktreeForkSkipsRuntimeTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := newGitRepo(t)
	w, err := OpenWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	parentPath, err := w.Ensure(ctx, "fork-runtime-parent")
	if err != nil {
		t.Fatal(err)
	}
	uploads := filepath.Join(parentPath, ".openpanda", "uploads")
	if err := os.MkdirAll(uploads, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uploads, "secret.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	childPath, err := w.Fork(ctx, "fork-runtime-parent", "fork-runtime-child", "HEAD")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if _, err := os.Stat(filepath.Join(childPath, ".openpanda", "uploads", "secret.png")); !os.IsNotExist(err) {
		t.Fatalf("fork copied the parent's runtime tree: err=%v", err)
	}
}

// TestWorktreeForkReplacesStaleSymlink pins the inode boundary, not just the
// path string: the parent replaced a tracked symlink with a regular file, so
// the child's checkout still holds the old link. Writing the parent's bytes
// through it lands on whatever the link points at — a file outside the
// repository — which is why the destination entry is removed before the write.
func TestWorktreeForkReplacesStaleSymlink(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := newGitRepo(t)
	outside := filepath.Join(t.TempDir(), "canary.txt")
	if err := os.WriteFile(outside, []byte("must stay unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "link.txt")); err != nil {
		t.Skipf("host cannot create symlinks: %v", err)
	}
	gitIn(t, repo, "config", "core.symlinks", "true")
	gitIn(t, repo, "add", "link.txt")
	gitIn(t, repo, "commit", "-qm", "baseline symlink")

	w, err := OpenWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	parentPath, err := w.Ensure(context.Background(), "symlink-parent")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(parentPath, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parentPath, "link.txt"), []byte("parent regular file"), 0o600); err != nil {
		t.Fatal(err)
	}

	childPath, err := w.Fork(context.Background(), "symlink-parent", "symlink-child", Branch("symlink-parent"))
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "must stay unchanged" {
		t.Fatalf("fork wrote through the stale link: canary=%q err=%v", got, err)
	}
	fi, err := os.Lstat(filepath.Join(childPath, "link.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("child kept the stale symlink instead of the parent's regular file")
	}
	if got, err := os.ReadFile(filepath.Join(childPath, "link.txt")); err != nil || string(got) != "parent regular file" {
		t.Fatalf("child file = %q err=%v", got, err)
	}
}

// TestWorktreeForkReplacesLinkedAncestor covers the same escape one level up:
// the child's checkout has a *directory* symlink where the parent has a real
// directory, so every file below it would be written outside the worktree.
func TestWorktreeForkReplacesLinkedAncestor(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := newGitRepo(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, "pkg")); err != nil {
		t.Skipf("host cannot create symlinks: %v", err)
	}
	gitIn(t, repo, "config", "core.symlinks", "true")
	gitIn(t, repo, "add", "pkg")
	gitIn(t, repo, "commit", "-qm", "baseline directory link")

	w, err := OpenWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	parentPath, err := w.Ensure(context.Background(), "ancestor-parent")
	if err != nil {
		t.Fatal(err)
	}
	// The parent turned the link into a real directory with a file in it.
	if err := os.Remove(filepath.Join(parentPath, "pkg")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(parentPath, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parentPath, "pkg", "file.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}

	childPath, err := w.Fork(context.Background(), "ancestor-parent", "ancestor-child", Branch("ancestor-parent"))
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "file.txt")); !os.IsNotExist(err) {
		t.Fatalf("fork wrote through the linked ancestor: err=%v", err)
	}
	if got, err := os.ReadFile(filepath.Join(childPath, "pkg", "file.txt")); err != nil || string(got) != "inside" {
		t.Fatalf("child file = %q err=%v", got, err)
	}
}

// TestWorktreeForkReplacesDirectoryWithFile: a valid directory-to-file change
// used to abort the whole copy — the stale empty directory blocked the write
// and every later edit, including files the parent created after it, stayed
// behind. The conversion is legal; the fork has to carry it.
func TestWorktreeForkReplacesDirectoryWithFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := newGitRepo(t)
	if err := os.Mkdir(filepath.Join(repo, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "pkg", "old.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-qm", "baseline directory")

	w, err := OpenWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	parentPath, err := w.Ensure(context.Background(), "typechange-parent")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(parentPath, "pkg", "old.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(parentPath, "pkg")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parentPath, "pkg"), []byte("replacement file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parentPath, "z-new.txt"), []byte("must also inherit"), 0o600); err != nil {
		t.Fatal(err)
	}

	childPath, err := w.Fork(context.Background(), "typechange-parent", "typechange-child", Branch("typechange-parent"))
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(childPath, "pkg")); err != nil || string(got) != "replacement file" {
		t.Fatalf("directory-to-file change not carried: %q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(childPath, "z-new.txt")); err != nil || string(got) != "must also inherit" {
		t.Fatalf("later new file lost after the conversion: %q err=%v", got, err)
	}
}

// TestWorktreeForkStagedLinkDoesNotDeleteOutside: the parent replaced a
// directory with a symlink and staged it, so the child gets the link first
// (git sorts "pkg" before "pkg/old.txt") and only then the deletion of the
// file that used to live under it. Removing through that link would delete a
// file outside the repository — the deletion path has to prove its ancestors
// are real directories, exactly like the write path does.
func TestWorktreeForkStagedLinkDoesNotDeleteOutside(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := newGitRepo(t)
	if err := os.Mkdir(filepath.Join(repo, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "pkg", "old.txt"), []byte("tracked baseline"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-qm", "baseline directory")
	gitIn(t, repo, "config", "core.symlinks", "true")

	w, err := OpenWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	parentPath, err := w.Ensure(context.Background(), "staged-link-parent")
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	canary := filepath.Join(outside, "old.txt")
	if err := os.WriteFile(canary, []byte("external canary must remain"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(parentPath, "pkg", "old.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(parentPath, "pkg")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(parentPath, "pkg")); err != nil {
		t.Skipf("host cannot create directory symlinks: %v", err)
	}
	gitIn(t, parentPath, "add", "-A")

	childPath, err := w.Fork(context.Background(), "staged-link-parent", "staged-link-child", Branch("staged-link-parent"))
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if got, err := os.ReadFile(canary); err != nil || string(got) != "external canary must remain" {
		t.Fatalf("fork deleted or changed a file outside the child: canary=%q err=%v", got, err)
	}
	// The child's shape follows the parent's: the directory is now a link.
	fi, err := os.Lstat(filepath.Join(childPath, "pkg"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("child kept the old directory instead of the parent's symlink")
	}
}

// TestWorktreeForkContinuesAfterFailedEntry: one path that cannot be carried
// must not cost the rest of the fork. The nested repository below is listed by
// git as a single untracked directory, so reading it as a file fails — and the
// file after it still has to arrive.
func TestWorktreeForkContinuesAfterFailedEntry(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := newGitRepo(t)
	w, err := OpenWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	parentPath, err := w.Ensure(context.Background(), "continue-parent")
	if err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(parentPath, "a-nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, nested, "init", "-q")
	gitIn(t, nested, "commit", "--allow-empty", "-qm", "nested fixture")
	if err := os.WriteFile(filepath.Join(parentPath, "z-good.txt"), []byte("must copy after error"), 0o600); err != nil {
		t.Fatal(err)
	}

	childPath, forkErr := w.Fork(context.Background(), "continue-parent", "continue-child", Branch("continue-parent"))
	if forkErr == nil {
		t.Fatal("fixture did not produce a per-entry failure")
	}
	if got, err := os.ReadFile(filepath.Join(childPath, "z-good.txt")); err != nil || string(got) != "must copy after error" {
		t.Fatalf("later file lost after a failed entry: %q err=%v (forkErr=%v)", got, err, forkErr)
	}
}

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

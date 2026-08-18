package sessions

import (
	"context"
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

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
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

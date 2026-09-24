package defense

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initTestGitRepo(t *testing.T) string {
	t.Helper()
	gitExe, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed on host, skipping git shadow test")
	}

	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command(gitExe, append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v (%s)", strings.Join(args, " "), err, string(out))
		}
	}

	run("init")
	run("config", "user.name", "TestUser")
	run("config", "user.email", "test@example.com")

	// Create an initial commit
	initFile := filepath.Join(dir, "README.md")
	if err := os.WriteFile(initFile, []byte("initial repo content\n"), 0o644); err != nil {
		t.Fatalf("write init file: %v", err)
	}
	run("add", "README.md")
	run("commit", "-m", "initial commit")

	return dir
}

func TestIsGitRepo(t *testing.T) {
	nonGit := t.TempDir()
	if IsGitRepo(nonGit) {
		t.Fatalf("expected nonGit to report false")
	}
	if IsGitRepo("") {
		t.Fatalf("expected empty string to report false")
	}

	gitDir := initTestGitRepo(t)
	if !IsGitRepo(gitDir) {
		t.Fatalf("expected gitDir to report true")
	}
}

func TestGitShadowSaveAndMerge(t *testing.T) {
	repo := initTestGitRepo(t)
	taskID := "task-xyz-123"

	// Modify existing file and create new file in working directory
	editFile := filepath.Join(repo, "worker.go")
	if err := os.WriteFile(editFile, []byte("package main\n\nfunc Work() {}\n"), 0o644); err != nil {
		t.Fatalf("write worker.go: %v", err)
	}

	// Save shadow branch
	branch, err := SaveGitShadow(repo, taskID, nil)
	if err != nil {
		t.Fatalf("SaveGitShadow: %v", err)
	}
	if branch != "panda-shadow-task-xyz-123" {
		t.Fatalf("unexpected branch name: %s", branch)
	}

	// Verify the ref exists in git
	revCmd := exec.Command("git", "-C", repo, "rev-parse", "--verify", "refs/heads/"+branch)
	if err := revCmd.Run(); err != nil {
		t.Fatalf("shadow ref refs/heads/%s does not exist", branch)
	}

	// Now discard uncommitted changes in working directory (as if task was preempted)
	_ = os.Remove(editFile)

	// Merge shadow back
	restored, conflicts, err := MergeGitShadow(repo, taskID)
	if err != nil {
		t.Fatalf("MergeGitShadow: %v", err)
	}
	if len(restored) != 1 || restored[0] != "worker.go" {
		t.Fatalf("expected restored worker.go, got: %v", restored)
	}
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts, got: %v", conflicts)
	}

	// Verify worker.go was restored
	content, err := os.ReadFile(editFile)
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if !strings.Contains(string(content), "func Work()") {
		t.Fatalf("restored content wrong: %s", string(content))
	}

	// Verify shadow branch was deleted after merge
	if err := exec.Command("git", "-C", repo, "rev-parse", "--verify", "refs/heads/"+branch).Run(); err == nil {
		t.Fatalf("expected shadow branch to be cleaned up after merge")
	}
}

// TestGitShadowScopedRootsDontConflict is the scoped-snapshot regression: a
// shadow seeded from an EMPTY index holds only the staged roots, so
// `diff HEAD ref` reports every untouched repository file as deleted and the
// merge would flood the report with false conflicts. Seeding the temp index
// from HEAD first keeps out-of-scope files identical between HEAD and ref.
func TestGitShadowScopedRootsDontConflict(t *testing.T) {
	repo := initTestGitRepo(t)
	taskID := "task-scoped"

	// A second committed file outside the scope roots.
	other := filepath.Join(repo, "other.txt")
	if err := os.WriteFile(other, []byte("untouched\n"), 0o644); err != nil {
		t.Fatalf("write other.txt: %v", err)
	}
	run := func(args ...string) {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v (%s)", strings.Join(args, " "), err, out)
		}
	}
	run("add", "other.txt")
	run("commit", "-m", "add other")

	// Scoped work: a new file under src/ only.
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	scoped := filepath.Join(repo, "src", "worker.go")
	if err := os.WriteFile(scoped, []byte("package src\n"), 0o644); err != nil {
		t.Fatalf("write scoped file: %v", err)
	}
	if _, err := SaveGitShadow(repo, taskID, []string{"src"}); err != nil {
		t.Fatalf("SaveGitShadow: %v", err)
	}

	// Preempted: the scoped file vanishes from the worktree.
	if err := os.Remove(scoped); err != nil {
		t.Fatalf("remove scoped file: %v", err)
	}

	restored, conflicts, err := MergeGitShadow(repo, taskID)
	if err != nil {
		t.Fatalf("MergeGitShadow: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("out-of-scope files reported as conflicts: %v", conflicts)
	}
	if len(restored) != 1 || restored[0] != "src/worker.go" {
		t.Fatalf("expected restored src/worker.go, got: %v", restored)
	}
}

// TestGitShadowConflictKeepsBranch: when a path is contested (modified both
// in the shadow and the live worktree), the shadow ref must survive the merge
// — it is the only copy of the shadow-side content needed for a manual
// resolution.
func TestGitShadowConflictKeepsBranch(t *testing.T) {
	repo := initTestGitRepo(t)
	taskID := "task-conflict"

	victim := filepath.Join(repo, "README.md")
	if err := os.WriteFile(victim, []byte("shadow version\n"), 0o644); err != nil {
		t.Fatalf("write shadow version: %v", err)
	}
	branch, err := SaveGitShadow(repo, taskID, nil)
	if err != nil {
		t.Fatalf("SaveGitShadow: %v", err)
	}

	// The preempting task edits the same file — contested.
	if err := os.WriteFile(victim, []byte("live version\n"), 0o644); err != nil {
		t.Fatalf("write live version: %v", err)
	}

	restored, conflicts, err := MergeGitShadow(repo, taskID)
	if err != nil {
		t.Fatalf("MergeGitShadow: %v", err)
	}
	if len(restored) != 0 {
		t.Fatalf("contested file must not be restored, got: %v", restored)
	}
	if len(conflicts) != 1 || conflicts[0] != "README.md" {
		t.Fatalf("expected README.md conflict, got: %v", conflicts)
	}
	// The shadow version is still recoverable from the kept branch.
	if err := exec.Command("git", "-C", repo, "rev-parse", "--verify", "refs/heads/"+branch).Run(); err != nil {
		t.Fatalf("shadow branch must survive a contested merge")
	}
	if out, err := exec.Command("git", "-C", repo, "show", branch+":README.md").Output(); err != nil ||
		!strings.Contains(string(out), "shadow version") {
		t.Fatalf("kept branch lost the shadow version: %v %s", err, out)
	}
}

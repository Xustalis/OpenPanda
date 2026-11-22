// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/artifact"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/projects"
)

// writeFile is the test-local convenience for planting a file with content.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestSetupIsolatedWorkDirCopyFallback covers the non-git path: a plain
// directory becomes a full copy (derived dirs skipped), and the baseline
// manifest lands inside the workspace.
func TestSetupIsolatedWorkDirCopyFallback(t *testing.T) {
	ctx := context.Background()
	c := NewCore(openTestDB(t), "iso-node", ledger.Card{Device: "iso-node"}, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "main.go"), "package main\n")
	writeFile(t, filepath.Join(src, "pkg", "a.go"), "package pkg\n")
	writeFile(t, filepath.Join(src, "node_modules", "dep.js"), "should not copy\n")
	writeFile(t, filepath.Join(src, ".openpanda", "scratch.txt"), "should not copy\n")

	task := Task{TaskID: "01a151d0-0000-7000-8000-000000000001", Project: "proj"}
	dir, err := c.setupIsolatedWorkDir(ctx, task, src)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "main.go")); got != "package main\n" {
		t.Fatalf("main.go copied as %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "pkg", "a.go")); got != "package pkg\n" {
		t.Fatalf("pkg/a.go copied as %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules")); !os.IsNotExist(err) {
		t.Fatal("node_modules must not be copied")
	}
	if _, err := os.Stat(filepath.Join(dir, ".openpanda")); !os.IsNotExist(err) {
		t.Fatal(".openpanda must not be copied")
	}
	baseline, err := loadIsolatedBaseline(dir)
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if len(baseline) != 2 {
		t.Fatalf("baseline = %v, want 2 entries", baseline)
	}
	// Reuse: a second setup returns the same dir without repopulating.
	again, err := c.setupIsolatedWorkDir(ctx, task, src)
	if err != nil || again != dir {
		t.Fatalf("reuse: dir=%q err=%v, want %q", again, err, dir)
	}
}

// TestSetupIsolatedWorkDirGitWorktree covers the git path when git is
// available: a worktree is carved out of HEAD and the source's uncommitted
// delta is overlaid, so the child sees the tree as it is right now.
func TestSetupIsolatedWorkDirGitWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx := context.Background()
	c := NewCore(openTestDB(t), "iso-git-node", ledger.Card{Device: "iso-git-node"}, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())

	src := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", src}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	writeFile(t, filepath.Join(src, "committed.txt"), "from head\n")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	// Uncommitted delta: a modified tracked file, an untracked one, and an
	// untracked *directory* — porcelain's default collapse would report it
	// as one "?? dir/" entry the file overlay skips unless -uall expands it.
	writeFile(t, filepath.Join(src, "committed.txt"), "dirty edit\n")
	writeFile(t, filepath.Join(src, "untracked.txt"), "new file\n")
	writeFile(t, filepath.Join(src, "newdir", "nested.txt"), "nested new\n")

	task := Task{TaskID: "01a151d0-0000-7000-8000-000000000002", Project: "proj"}
	dir, err := c.setupIsolatedWorkDir(ctx, task, src)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if st, err := os.Stat(filepath.Join(dir, ".git")); err != nil || st.IsDir() {
		t.Fatalf("expected a linked worktree (.git file): err=%v", err)
	}
	if got := readFile(t, filepath.Join(dir, "committed.txt")); got != "dirty edit\n" {
		t.Fatalf("overlay of modified file = %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "untracked.txt")); got != "new file\n" {
		t.Fatalf("overlay of untracked file = %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "newdir", "nested.txt")); got != "nested new\n" {
		t.Fatalf("overlay of untracked dir contents = %q", got)
	}
	// Cleanup removes the worktree through git (registration included).
	c.cleanupIsolatedDir(ctx, dir)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("worktree still present after cleanup: %v", err)
	}
	out, _ := exec.Command("git", "-C", src, "worktree", "list").Output()
	if strings.Count(string(out), "\n") > 1 {
		t.Fatalf("worktree registration leaked: %s", out)
	}
}

// TestMergeIsolatedWorkThreeWay pins the merge semantics: child-only changes
// apply, identical landings are no-ops, deletions apply, and a path both
// sides changed is a reported conflict that leaves the main tree's bytes.
func TestMergeIsolatedWorkThreeWay(t *testing.T) {
	c := NewCore(openTestDB(t), "merge-node", ledger.Card{Device: "merge-node"}, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())

	iso := t.TempDir()
	main := t.TempDir()
	writeFile(t, filepath.Join(iso, "modified.txt"), "child version\n")
	writeFile(t, filepath.Join(iso, "added.txt"), "brand new\n")
	writeFile(t, filepath.Join(iso, "conflict.txt"), "child version\n")
	writeFile(t, filepath.Join(iso, "same.txt"), "identical landing\n")
	writeFile(t, filepath.Join(iso, "kept.txt"), "untouched by child\n")
	// Baseline = what the fork looked like.
	writeFile(t, filepath.Join(iso, isolatedBaselineFile), `{
		"modified.txt": "BASE-MOD",
		"deleted.txt": "BASE-DEL",
		"conflict.txt": "BASE-CONF",
		"same.txt": "BASE-SAME",
		"kept.txt": "BASE-KEPT"
	}`)
	// Main tree: modified.txt untouched since fork; conflict.txt changed
	// differently; same.txt already at the child's bytes; deleted.txt present.
	writeFile(t, filepath.Join(main, "modified.txt"), "old version\n")
	writeFile(t, filepath.Join(main, "conflict.txt"), "main diverged\n")
	writeFile(t, filepath.Join(main, "same.txt"), "identical landing\n")
	writeFile(t, filepath.Join(main, "deleted.txt"), "to be deleted\n")
	writeFile(t, filepath.Join(main, "kept.txt"), "untouched by child\n")

	// Baseline hashes are fabricated, so the comparison must be against the
	// real hashes of the fork state: rewrite the baseline with real hashes of
	// a snapshot the merge can match. Simplest honest construction: baseline
	// records the main tree's pre-fork bytes for untouched paths and the
	// child's pre-edit bytes elsewhere — approximated here by hashing files
	// we plant to represent the fork state.
	fork := t.TempDir()
	writeFile(t, filepath.Join(fork, "modified.txt"), "old version\n")    // = main's current bytes
	writeFile(t, filepath.Join(fork, "deleted.txt"), "to be deleted\n")   // = main's current bytes
	writeFile(t, filepath.Join(fork, "conflict.txt"), "fork version\n")   // neither side kept
	writeFile(t, filepath.Join(fork, "same.txt"), "fork version\n")       // both sides moved identically
	writeFile(t, filepath.Join(fork, "kept.txt"), "untouched by child\n") // child untouched
	hashes, err := c.hashWorkTree(fork)
	if err != nil {
		t.Fatalf("hash fork: %v", err)
	}
	// Child deletions: deleted.txt is absent from iso and its baseline hash
	// must be the fork's.
	if err := writeIsolatedBaseline(iso, hashes); err != nil {
		t.Fatalf("baseline: %v", err)
	}

	merged, conflicts, err := c.mergeIsolatedWork(iso, main)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	// modified.txt: applied. added.txt: applied. deleted.txt: deleted.
	if got := readFile(t, filepath.Join(main, "modified.txt")); got != "child version\n" {
		t.Fatalf("modified.txt = %q, want the child's bytes", got)
	}
	if got := readFile(t, filepath.Join(main, "added.txt")); got != "brand new\n" {
		t.Fatalf("added.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(main, "deleted.txt")); !os.IsNotExist(err) {
		t.Fatal("deleted.txt should have been deleted by the merge")
	}
	if got := readFile(t, filepath.Join(main, "kept.txt")); got != "untouched by child\n" {
		t.Fatalf("kept.txt disturbed: %q", got)
	}
	if merged != 3 {
		t.Fatalf("merged = %d, want 3 (modified, added, deleted)", merged)
	}
	if len(conflicts) != 1 || conflicts[0] != "conflict.txt" {
		t.Fatalf("conflicts = %v, want [conflict.txt]", conflicts)
	}
	if got := readFile(t, filepath.Join(main, "conflict.txt")); got != "main diverged\n" {
		t.Fatalf("conflicting path must keep the main bytes, got %q", got)
	}
	// same.txt: child changed it to the bytes main already has — no-op, not
	// a conflict and not counted as merged.
	if got := readFile(t, filepath.Join(main, "same.txt")); got != "identical landing\n" {
		t.Fatalf("same.txt = %q", got)
	}
}

// TestIsolatedChildrenRunInParallelAndMerge is the end-to-end proof: two
// isolated children of one project run their native work SIMULTANEOUSLY (no
// shared locks at all), and each child's files land on the shared project
// tree when it finishes.
func TestIsolatedChildrenRunInParallelAndMerge(t *testing.T) {
	db := openTestDB(t)
	card := ledger.Card{
		Device:        "iso-e2e",
		ResourceClass: "Standard",
		Native: []ledger.NativeAbility{
			{ID: "sys:slow-a", Command: "sh", Args: []string{"-c", "sleep 1; echo a > a.txt"}},
			{ID: "sys:slow-b", Command: "sh", Args: []string{"-c", "sleep 1; echo b > b.txt"}},
		},
		Capacity: ledger.Capacity{CPUCores: 4, RAMGB: 8, MaxConcurrent: 4},
	}
	c := NewCore(db, "iso-e2e", card, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())
	// The shared project tree the children fork from and merge back into: a
	// project registered in the store, so projectWorkDir resolves to it.
	projDir := filepath.Join(t.TempDir(), "proj")
	writeFile(t, filepath.Join(projDir, "README.md"), "shared tree\n")
	ps := projects.NewStore(db)
	if _, err := ps.Create("proj", projDir, "iso e2e"); err != nil {
		t.Fatalf("create project: %v", err)
	}
	c.SetProjectStores(ps, filepath.Join(t.TempDir(), "projects-root"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.StartQueueScheduler(ctx)

	enqueue := func(title, ability string) string {
		t.Helper()
		in := TaskInput{
			Title: title, Intent: "x", Requires: []string{ability},
			Authorized: true, Project: "proj", Isolate: true,
		}
		tk, _, _, err := c.createTask(ctx, in)
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		if err := c.store.SetQueueMeta(ctx, tk.TaskID, PriorityNormal, "", "", childResourceKeys(tk.TaskID, in)); err != nil {
			t.Fatalf("queue meta %s: %v", title, err)
		}
		if err := c.store.SetIsolate(ctx, tk.TaskID, true); err != nil {
			t.Fatalf("set isolate %s: %v", title, err)
		}
		if err := c.store.Queue(ctx, tk.TaskID, c.nodeID); err != nil {
			t.Fatalf("queue %s: %v", title, err)
		}
		return tk.TaskID
	}
	state := func(id string) string {
		t.Helper()
		got, err := c.store.Get(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		return got.State
	}

	a := enqueue("iso-a", "sys:slow-a")
	b := enqueue("iso-b", "sys:slow-b")

	deadline := time.Now().Add(5 * time.Second)
	overlapped := false
	for time.Now().Before(deadline) {
		sa, sb := state(a), state(b)
		if sa == StateRunning && sb == StateRunning {
			overlapped = true
			break
		}
		if (sa == StateDone || sa == StateFailed) && (sb == StateDone || sb == StateFailed) {
			t.Fatal("isolated children finished but never overlapped")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !overlapped {
		t.Fatal("isolated children never overlapped within deadline")
	}
	waitLocalTasksSettled(t, c)
	if got := state(a); got != StateDone {
		t.Fatalf("a final state = %s", got)
	}
	if got := state(b); got != StateDone {
		t.Fatalf("b final state = %s", got)
	}
	// Both children's files merged into the shared tree.
	if got := readFile(t, filepath.Join(projDir, "a.txt")); !strings.Contains(got, "a") {
		t.Fatalf("a.txt = %q", got)
	}
	if got := readFile(t, filepath.Join(projDir, "b.txt")); !strings.Contains(got, "b") {
		t.Fatalf("b.txt = %q", got)
	}
	// The fork's file is untouched; the workspaces are cleaned up.
	if got := readFile(t, filepath.Join(projDir, "README.md")); got != "shared tree\n" {
		t.Fatalf("README.md = %q", got)
	}
	for _, id := range []string{a, b} {
		dir, _ := c.attachedWorkDir(id)
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("isolated dir for %s not cleaned: %v", id, err)
		}
	}
}

// TestIsolatedWorkspacePreservesSymlinks covers the non-git copy path's
// symlink fidelity: links survive the copy as links (a flattened copy
// would rewrite every link into its target's bytes at merge), and the
// three-way sees link creation, retarget, and deletion exactly like file
// changes.
func TestIsolatedWorkspacePreservesSymlinks(t *testing.T) {
	ctx := context.Background()
	c := NewCore(openTestDB(t), "iso-link", ledger.Card{Device: "iso-link"}, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "lib", "real.txt"), "v1\n")
	writeFile(t, filepath.Join(src, "lib", "moved.txt"), "v2\n")
	if err := os.Symlink("lib/real.txt", filepath.Join(src, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink("lib", filepath.Join(src, "libdir")); err != nil {
		t.Fatalf("symlink libdir: %v", err)
	}

	task := Task{TaskID: "01a151d0-0000-7000-8000-000000000003", Project: "proj"}
	dir, err := c.setupIsolatedWorkDir(ctx, task, src)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	// A non-git tree took the copy path; every link must arrive as a link.
	for _, p := range []string{"link.txt", "libdir"} {
		st, err := os.Lstat(filepath.Join(dir, p))
		if err != nil {
			t.Fatalf("lstat %s: %v", p, err)
		}
		if st.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s flattened to mode %v in the copy", p, st.Mode())
		}
	}
	// Windows normalizes the stored target to backslashes at creation —
	// compare the canonical slash form so the check is platform-stable.
	if target, err := os.Readlink(filepath.Join(dir, "link.txt")); err != nil || filepath.ToSlash(target) != "lib/real.txt" {
		t.Fatalf("link.txt target = %q err=%v, want lib/real.txt", target, err)
	}

	// The child's run retargets a link, deletes one, and adds one — the
	// untouched main tree (src itself) must receive all three as links.
	if err := os.Remove(filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("lib/moved.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "libdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("lib/real.txt", filepath.Join(dir, "newlink")); err != nil {
		t.Fatal(err)
	}
	merged, conflicts, err := c.mergeIsolatedWork(dir, src)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %v, want none", conflicts)
	}
	if merged != 3 {
		t.Fatalf("merged = %d, want 3 (retarget, delete, create)", merged)
	}
	if target, err := os.Readlink(filepath.Join(src, "link.txt")); err != nil || filepath.ToSlash(target) != "lib/moved.txt" {
		t.Fatalf("merged link.txt = %q err=%v, want lib/moved.txt", target, err)
	}
	if target, err := os.Readlink(filepath.Join(src, "newlink")); err != nil || filepath.ToSlash(target) != "lib/real.txt" {
		t.Fatalf("merged newlink = %q err=%v, want lib/real.txt", target, err)
	}
	if _, err := os.Lstat(filepath.Join(src, "libdir")); !os.IsNotExist(err) {
		t.Fatal("deleted libdir still present in the main tree")
	}
	if got := readFile(t, filepath.Join(src, "lib", "real.txt")); got != "v1\n" {
		t.Fatalf("link target bytes disturbed: %q", got)
	}
}

// TestPackStageOutputSkipsIsolationBookkeeping locks the wire hygiene an
// isolated remote child needs: the packed output must not carry the merge
// baseline or the worktree's .git pointer — extracting either on the origin
// would plant bookkeeping (or a dead git marker) inside the checkout.
func TestPackStageOutputSkipsIsolationBookkeeping(t *testing.T) {
	c := NewCore(openTestDB(t), "pack-node", ledger.Card{Device: "pack-node"}, 5, testLogger(), config.ModelConfig{})
	c.artifacts = artifact.NewStore(t.TempDir())
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, "src", "app.go"), "package main\n")
	writeFile(t, filepath.Join(ws, ".git"), "gitdir: /tmp/openpanda/tasks/x\n")
	writeFile(t, filepath.Join(ws, isolatedBaselineFile), "{}\n")
	task, _, _, err := c.createTask(context.Background(), TaskInput{Title: "iso-pack"})
	if err != nil {
		t.Fatalf("createTask: %v", err)
	}
	task.Isolate = true
	hash, err := c.packStageOutput(context.Background(), task, ws)
	if err != nil {
		t.Fatalf("packStageOutput: %v", err)
	}
	entries, err := c.artifacts.Entries(hash)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	var paths []string
	for _, e := range entries {
		paths = append(paths, e.Path)
	}
	joined := strings.Join(paths, ",")
	if strings.Contains(joined, ".git") || strings.Contains(joined, isolatedBaselineFile) {
		t.Fatalf("bookkeeping leaked into pack: %v", paths)
	}
	if !strings.Contains(joined, "app.go") {
		t.Fatalf("content missing from pack: %v", paths)
	}
}

// TestPackStageOutputSkipsRegenerableDirs pins the output-side weight fix:
// a delegated run that installs its own environment must not pack and ship
// node_modules/.venv/__pycache__/target-cache back upstream — inbound trees
// never carry them, so nothing on the wire can depend on receiving them.
// Build products (dist/, target/) remain deliverables and must survive.
func TestPackStageOutputSkipsRegenerableDirs(t *testing.T) {
	c := NewCore(openTestDB(t), "pack-node2", ledger.Card{Device: "pack-node2"}, 5, testLogger(), config.ModelConfig{})
	c.artifacts = artifact.NewStore(t.TempDir())
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, "src", "app.go"), "package main\n")
	writeFile(t, filepath.Join(ws, "node_modules", "dep", "index.js"), "module.exports=1\n")
	writeFile(t, filepath.Join(ws, ".venv", "lib", "python.py"), "x=1\n")
	writeFile(t, filepath.Join(ws, "__pycache__", "mod.cpython-312.pyc"), "pyc\n")
	writeFile(t, filepath.Join(ws, "dist", "bundle.js"), "built output\n")
	writeFile(t, filepath.Join(ws, "target", "release", "app"), "binary\n")
	writeFile(t, filepath.Join(ws, ".git", "config"), "[remote]\n")
	task, _, _, err := c.createTask(context.Background(), TaskInput{Title: "prune-pack"})
	if err != nil {
		t.Fatalf("createTask: %v", err)
	}
	hash, err := c.packStageOutput(context.Background(), task, ws)
	if err != nil {
		t.Fatalf("packStageOutput: %v", err)
	}
	entries, err := c.artifacts.Entries(hash)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	var paths []string
	for _, e := range entries {
		paths = append(paths, e.Path)
	}
	joined := strings.Join(paths, ",")
	for _, dropped := range []string{"node_modules", ".venv", "__pycache__", ".git"} {
		if strings.Contains(joined, dropped) {
			t.Fatalf("regenerable dir %q shipped in output pack: %v", dropped, paths)
		}
	}
	for _, kept := range []string{"dist/bundle.js", "target/release/app", "src/app.go"} {
		if !strings.Contains(joined, kept) {
			t.Fatalf("deliverable %q missing from output pack: %v", kept, paths)
		}
	}
}

func TestIsolatedWorkspaceInsideSource(t *testing.T) {
	c := NewCore(openTestDB(t), "iso-node", ledger.Card{Device: "iso-node"}, 5, testLogger(), config.ModelConfig{})
	root := t.TempDir()
	c.SetWorkDir(root)
	writeFile(t, filepath.Join(root, "README.md"), "source")
	writeFile(t, filepath.Join(root, "tasks", "sibling", "output"), "another task")
	writeFile(t, filepath.Join(root, "docs", "tasks", "guide"), "project docs")
	dir, err := c.setupIsolatedWorkDir(t.Context(), Task{TaskID: "01a151d0-0000-7000-8000-000000000001", Project: "project"}, root)
	if err != nil {
		t.Fatalf("nested setup: %v", err)
	}
	if readFile(t, filepath.Join(dir, "README.md")) != "source" || readFile(t, filepath.Join(dir, "docs", "tasks", "guide")) != "project docs" {
		t.Fatal("project content missing")
	}
	if _, err := os.Stat(filepath.Join(dir, "tasks")); !os.IsNotExist(err) {
		t.Fatalf("managed task directories copied: %v", err)
	}
}

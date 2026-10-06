package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// writeRepoTree materializes a repo-shaped fixture: a .git marker plus a
// source file and the derived trees the attach must prune.
func writeRepoTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mk := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	mk(".git/HEAD", "ref: refs/heads/main")
	mk(".git/config", "[remote \"origin\"]\n\turl = https://token@example/r.git")
	mk("app/main.go", "package main\n")
	mk("node_modules/pkg/index.js", "module.exports = {}\n")
	mk("vendor/lib/lib.go", "package lib\n")
	return dir
}

// TestLooksLikeRepo pins the gate that decides whether a directory may ship to
// a peer: only version-controlled trees (the dir or a near ancestor) count, so
// a bare ask from $HOME never uploads the user's home directory.
func TestLooksLikeRepo(t *testing.T) {
	repo := writeRepoTree(t)
	if !looksLikeRepo(repo) {
		t.Fatalf("repo dir not recognized")
	}
	sub := filepath.Join(repo, "app")
	if !looksLikeRepo(sub) {
		t.Fatalf("nested dir inside a repo not recognized")
	}
	if looksLikeRepo(t.TempDir()) {
		t.Fatalf("plain temp dir reported as repo")
	}
}

// TestAttachWorktreePacksRepo covers the delegation-side attach: a file task in
// a repo packs its tree into the artifact pool and rides as an input, while the
// skip set keeps derived content (and .git — a credential carrier) off the wire.
func TestAttachWorktreePacksRepo(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "attach-src", "")
	pool := withArtifactPool(t, c)
	repo := writeRepoTree(t)

	p := bus.TaskDelegatePayload{TaskID: "t-wt-1"}
	c.attachWorktree(ctx, &p, TaskInput{ContextType: "file", WorkDir: repo})

	if len(p.Inputs) != 1 || p.Inputs[0].Stage != worktreeArtifactStage {
		t.Fatalf("inputs = %+v, want one __worktree__ ref", p.Inputs)
	}
	sz, ok := pool.Has(p.Inputs[0].Hash)
	if !ok {
		t.Fatalf("worktree artifact %q missing from pool", p.Inputs[0].Hash)
	}
	if sz == 0 {
		t.Fatalf("empty worktree artifact")
	}
	// The packed tree must contain the source file and none of the skipped dirs.
	out := t.TempDir()
	if _, err := pool.Extract(p.Inputs[0].Hash, out); err != nil {
		t.Fatalf("extract: %v", err)
	}
	for _, want := range []string{"app/main.go"} {
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(want))); err != nil {
			t.Fatalf("packed tree missing %s", want)
		}
	}
	for _, skip := range []string{".git", "node_modules", "vendor"} {
		if _, err := os.Stat(filepath.Join(out, skip)); !os.IsNotExist(err) {
			t.Fatalf("skipped dir %s shipped in worktree", skip)
		}
	}

	// Guards: project tasks and non-repo dirs attach nothing.
	p2 := bus.TaskDelegatePayload{TaskID: "t-wt-2"}
	c.attachWorktree(ctx, &p2, TaskInput{ContextType: "file", Project: "p", WorkDir: repo})
	p3 := bus.TaskDelegatePayload{TaskID: "t-wt-3"}
	c.attachWorktree(ctx, &p3, TaskInput{ContextType: "file", WorkDir: t.TempDir()})
	p4 := bus.TaskDelegatePayload{TaskID: "t-wt-4"}
	c.attachWorktree(ctx, &p4, TaskInput{ContextType: "hardware", WorkDir: repo})
	for i, q := range []*bus.TaskDelegatePayload{&p2, &p3, &p4} {
		if len(q.Inputs) != 0 {
			t.Fatalf("guard case %d attached inputs: %+v", i, q.Inputs)
		}
	}
}

// newCoreWithAgentListen builds a listening Core whose card advertises one
// agent ability — the leaf half of a delegation e2e.
func newCoreWithAgentListen(t *testing.T, id, addr string) *Core {
	t.Helper()
	db := openTestDB(t)
	card := ledger.Card{
		Device:        id,
		ResourceClass: "Standard",
		Agents: map[string]ledger.Agent{
			"claude_code": {Adapter: "claude_code.py", Capabilities: []string{"code:modify"}, Tier: 1},
		},
		Capacity: ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
	c := NewCore(db, id, card, 5, testLogger(), config.ModelConfig{})
	c.router.SetAgentProber(func(string, ledger.Agent) bool { return true })
	c.SetSharedSecret(testSharedSecret)
	return c
}

// TestFileTaskWorktreeTravelsRoundTrip is the delegation-quality flagship for
// ad-hoc file tasks: the repo rides as an artifact input, the remote agent runs
// inside the real tree (not an empty dir), the change list reports back on the
// wire, and the produced tree returns over the origin directory.
func TestFileTaskWorktreeTravelsRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	root := newCore(t, "wt-root", "127.0.0.1:18010")
	leaf := newCoreWithAgentListen(t, "wt-leaf", "127.0.0.1:18011")
	withArtifactPool(t, root)
	withArtifactPool(t, leaf)
	leaf.SetWorkDir(filepath.Join(t.TempDir(), "leafwork"))
	startPair(t, ctx, root, leaf, "127.0.0.1:18010", "127.0.0.1:18011")

	repo := writeRepoTree(t)

	// The stub agent asserts it is looking at the REAL shipped tree, then
	// edits it — the round-trip proof that context travelled.
	leaf.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		if _, err := os.Stat(filepath.Join(cwd, "app", "main.go")); err != nil {
			return commander.AgentResult{OK: false, Stderr: "shipped tree missing app/main.go: " + err.Error(), ExitCode: 1}
		}
		if _, err := os.Stat(filepath.Join(cwd, "node_modules")); !os.IsNotExist(err) {
			return commander.AgentResult{OK: false, Stderr: "node_modules leaked onto the wire", ExitCode: 1}
		}
		if err := os.WriteFile(filepath.Join(cwd, "fixed.txt"), []byte("fix"), 0o644); err != nil {
			return commander.AgentResult{OK: false, Stderr: err.Error(), ExitCode: 1}
		}
		return commander.AgentResult{OK: true, Result: "fixed", ExitCode: 0}
	})

	task, result, err := root.Submit(ctx, TaskInput{
		Title:       "fix main",
		ContextType: "file",
		WorkDir:     repo,
		RepoPath:    repo,
		Intent:      "fix the bug in main.go",
		Requires:    []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !result.OK {
		t.Fatalf("result failed: %+v", result)
	}
	if len(result.FilesChanged) == 0 {
		t.Fatalf("result carries no files_changed: %+v", result)
	}
	found := false
	for _, f := range result.FilesChanged {
		if f == "fixed.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("files_changed missing fixed.txt: %v", result.FilesChanged)
	}

	// The return leg: the produced tree adopts back over the origin repo.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(repo, "fixed.txt")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("edited file never adopted back into origin repo")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The leaf row reports the task done in its private task dir, not in the
	// node's shared work dir.
	lt, err := leaf.store.Get(ctx, task.TaskID)
	if err != nil || lt.State != StateDone {
		t.Fatalf("leaf task = %+v err %v, want done", lt, err)
	}
}

// TestScheduledFileTaskShipsWorktree covers the queue path's half of the
// attach contract: a file task submitted through Enqueue (the panel path)
// delegates with its repo tree as an input instead of being pinned local
// under the old "work dir means local" rule. The peer's row must carry the
// __worktree__ input — the proof the tree actually rode the wire.
func TestScheduledFileTaskShipsWorktree(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	root := newCore(t, "q-root", "127.0.0.1:18120")
	leaf := newCoreWithAgentListen(t, "q-leaf", "127.0.0.1:18121")
	withArtifactPool(t, root)
	withArtifactPool(t, leaf)
	leaf.SetWorkDir(filepath.Join(t.TempDir(), "leafwork"))
	startPair(t, ctx, root, leaf, "127.0.0.1:18120", "127.0.0.1:18121")
	leaf.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		return commander.AgentResult{OK: true, Result: "done", ExitCode: 0}
	})

	repo := writeRepoTree(t)
	got, err := root.Enqueue(ctx, TaskInput{
		Title:       "queued fix",
		ContextType: "file",
		Intent:      "fix the bug",
		Requires:    []string{"code:modify"},
	}, QueueSpec{Priority: PriorityNormal, WorkDir: repo})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	row, err := root.store.Get(ctx, got.TaskID)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	if !root.forwardScheduled(ctx, row) {
		t.Fatal("a repo-backed file task with a capable peer stayed local")
	}

	// The peer's row must carry the worktree input — the tree rode the wire.
	// The row is visible to Get the moment CreateWithID lands, but its
	// inputs column is filled a few statements later inside handleDelegate —
	// poll until they appear rather than racing that gap.
	deadline := time.Now().Add(10 * time.Second)
	for {
		lt, gerr := leaf.store.Get(ctx, got.TaskID)
		if gerr == nil && len(lt.Inputs) > 0 {
			if lt.Inputs[0].Stage != worktreeArtifactStage {
				t.Fatalf("input stage = %q, want %q", lt.Inputs[0].Stage, worktreeArtifactStage)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("peer task never gained its worktree input (err=%v inputs=%+v)", gerr, lt.Inputs)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestScheduledFileTaskNonRepoStaysLocal keeps the other half of the queue
// rule: a file task whose directory is not a repo has nothing to ship, and a
// blind forward still loses to just running it here.
func TestScheduledFileTaskNonRepoStaysLocal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	root := newCore(t, "q-root2", "127.0.0.1:18122")
	leaf := newCoreWithAgentListen(t, "q-leaf2", "127.0.0.1:18123")
	withArtifactPool(t, root)
	withArtifactPool(t, leaf)
	startPair(t, ctx, root, leaf, "127.0.0.1:18122", "127.0.0.1:18123")

	got, err := root.Enqueue(ctx, TaskInput{
		Title:       "scratch work",
		ContextType: "file",
		Intent:      "edit scratch files",
		Requires:    []string{"code:modify"},
	}, QueueSpec{Priority: PriorityNormal, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	row, err := root.store.Get(ctx, got.TaskID)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	if root.forwardScheduled(ctx, row) {
		t.Error("a file task with no shippable tree forwarded blind to a peer")
	}
}

// TestAttachWorktreePinsAdoptDir guards the round-trip's load-bearing detail:
// the directory the tree packed from is what the produced tree extracts back
// over — the row's work_dir must name it, or adoptWorktreeOutput has no
// honest landing spot.
func TestAttachWorktreePinsAdoptDir(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "pin-src", "")
	withArtifactPool(t, c)
	repo := writeRepoTree(t)

	// A task whose TaskInput names RepoPath but no WorkDir: the attach must
	// still leave the row pointing at the packed dir for the return leg.
	task, err := c.store.Create(ctx, "", "", "repo-only task", c.nodeID, []string{c.nodeID})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	p := bus.TaskDelegatePayload{TaskID: task.TaskID}
	c.attachWorktree(ctx, &p, TaskInput{ContextType: "file", RepoPath: repo})
	if len(p.Inputs) != 1 {
		t.Fatalf("inputs = %+v, want one ref", p.Inputs)
	}
	row, err := c.store.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.WorkDir != repo {
		t.Fatalf("row work_dir = %q, want packed dir %q", row.WorkDir, repo)
	}
}

// TestFilesChangedReportedLocally verifies the result contract on the local
// path: an agent that edits its workdir reports exactly the paths it touched,
// and the row's result JSON carries the same list for `panda task` to render.
func TestFilesChangedReportedLocally(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgent(t, "fc-node")
	work := t.TempDir()
	c.SetWorkDir(work)
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("old"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	c.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		_ = os.WriteFile(filepath.Join(work, "a.txt"), []byte("new"), 0o644)
		_ = os.WriteFile(filepath.Join(work, "b.txt"), []byte("new file"), 0o644)
		return commander.AgentResult{OK: true, Result: "done", ExitCode: 0}
	})

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "edit files",
		ContextType: "file",
		Intent:      "update a.txt and add b.txt",
		Requires:    []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	want := map[string]bool{"a.txt": true, "b.txt": true}
	if len(result.FilesChanged) == 0 {
		t.Fatalf("no files_changed on result: %+v", result)
	}
	for _, f := range result.FilesChanged {
		delete(want, f)
	}
	if len(want) != 0 {
		t.Fatalf("files_changed missing %v (got %v)", want, result.FilesChanged)
	}

	// The persisted row carries the same list — `panda task` renders it.
	row, err := c.store.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var res struct {
		FilesChanged []string `json:"files_changed"`
	}
	if err := json.Unmarshal([]byte(row.ResultJSON), &res); err != nil {
		t.Fatalf("unmarshal result_json: %v", err)
	}
	if len(res.FilesChanged) == 0 {
		t.Fatalf("row result_json has no files_changed: %s", row.ResultJSON)
	}
}

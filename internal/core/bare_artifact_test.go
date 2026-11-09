// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/entry"
)

// dirHasFiles gates the bare-task artifact pack: a regular file anywhere in
// the tree counts, while an empty dir (or one holding only subdirs) must not
// become an empty archive.
func TestDirHasFiles(t *testing.T) {
	empty := t.TempDir()
	if dirHasFiles(empty) {
		t.Error("empty dir reported files")
	}
	subOnly := t.TempDir()
	if err := os.Mkdir(filepath.Join(subOnly, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if dirHasFiles(subOnly) {
		t.Error("dir holding only a subdir reported files")
	}
	withFile := t.TempDir()
	if err := os.WriteFile(filepath.Join(withFile, "screen.png"), []byte("png"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !dirHasFiles(withFile) {
		t.Error("dir holding a file reported none")
	}
	if dirHasFiles(filepath.Join(t.TempDir(), "missing")) {
		t.Error("nonexistent dir reported files")
	}
}

// runBareFinalize drives finalizeRun over a bare task (no plan, project, or
// pinned work dir) whose private work dir already holds the given files, and
// returns the result payload. The task row goes through the real
// submitted→dispatched→running path so the terminal Complete write is legal.
func runBareFinalize(t *testing.T, c *Core, files map[string]string) (Task, string) {
	t.Helper()
	ctx := context.Background()
	taskID := "bare-" + t.Name()
	if _, err := c.store.CreateWithID(ctx, taskID, "", "", "bare task", c.nodeID, nil, false); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := c.store.Queue(ctx, taskID, c.nodeID); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.Dispatch(ctx, taskID, c.nodeID, c.nodeID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := c.store.Accept(ctx, taskID, c.nodeID); err != nil {
		t.Fatalf("accept: %v", err)
	}
	workDir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	task, err := c.store.Get(ctx, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	res, err := c.finalizeRun(ctx, taskID, task,
		commander.Plan{Kind: "native", Tier: 1}, nil, task.AttemptID, workDir,
		commander.Result{ExitCode: 0, Stdout: "ok"}, nil, false,
		entry.SuperviseVerdict{}, false, "", "", nil, nil)
	if err != nil {
		t.Fatalf("finalizeRun: %v", err)
	}
	if res.State != StateDone || !res.OK {
		t.Fatalf("result = %+v, want done+ok", res)
	}
	return task, res.OutputArtifact
}

// TestFinalizeRunPacksBareTaskOutput is the regression for screenshots
// stranded on the executor: a bare actuator/native run that left a file in
// its private work dir publishes it as the task's output artifact, which is
// what lets the delegator pull it and `panda task artifact` reach it.
func TestFinalizeRunPacksBareTaskOutput(t *testing.T) {
	c, _, _ := projectCore(t)
	task, hash := runBareFinalize(t, c, map[string]string{"screen.png": "fakepng"})
	if hash == "" {
		t.Fatal("bare task that wrote a file produced no output artifact")
	}
	if _, held := c.artifacts.Has(hash); !held {
		t.Fatalf("output artifact %s not held in local pool", hash)
	}
	stored, err := c.store.Get(context.Background(), task.TaskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if stored.OutputArtifact != hash {
		t.Fatalf("task row output_artifact = %q, want %q", stored.OutputArtifact, hash)
	}
}

// TestFinalizeRunSkipsEmptyBareDir keeps pure-stdout work clean: a bare task
// whose work dir stayed empty must not mint an empty archive.
func TestFinalizeRunSkipsEmptyBareDir(t *testing.T) {
	c, _, _ := projectCore(t)
	_, hash := runBareFinalize(t, c, nil)
	if hash != "" {
		t.Fatalf("empty work dir produced artifact %s", hash)
	}
}

// TestFinalizeRunSkipsPinnedWorkDir guards the shared-ground rule: a task with
// a pinned WorkDir runs in the user's own tree, which is never bulk-packed —
// only an explicitly-attached/project tree is.
func TestFinalizeRunSkipsPinnedWorkDir(t *testing.T) {
	c, _, _ := projectCore(t)
	ctx := context.Background()
	pinned := t.TempDir()
	taskID := "bare-pinned"
	if _, err := c.store.CreateWithID(ctx, taskID, "", "", "pinned task", c.nodeID, nil, false); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := c.store.SetWorkDir(ctx, taskID, pinned); err != nil {
		t.Fatalf("set work dir: %v", err)
	}
	if err := c.store.Queue(ctx, taskID, c.nodeID); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.Dispatch(ctx, taskID, c.nodeID, c.nodeID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := c.store.Accept(ctx, taskID, c.nodeID); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pinned, "out.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	task, err := c.store.Get(ctx, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	res, err := c.finalizeRun(ctx, taskID, task,
		commander.Plan{Kind: "native", Tier: 1}, nil, task.AttemptID, pinned,
		commander.Result{ExitCode: 0}, nil, false, entry.SuperviseVerdict{}, false, "", "", nil, nil)
	if err != nil {
		t.Fatalf("finalizeRun: %v", err)
	}
	if res.OutputArtifact != "" {
		t.Fatalf("pinned work dir was bulk-packed into artifact %s", res.OutputArtifact)
	}
}

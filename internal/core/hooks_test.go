// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// waitForFile polls until path exists or the deadline passes.
func waitForFile(t *testing.T, path string) bool {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// TestHooksFireOnLifecycleEdges is the wiring test: a task walked through the
// state machine fires the hooks bound to started and done, with the task's own
// fields substituted in.
func TestHooksFireOnLifecycleEdges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell fixtures")
	}
	dir := t.TempDir()
	started := filepath.Join(dir, "started.log")
	done := filepath.Join(dir, "done.log")

	c := queueTestCore(t)
	c.SetHooks(config.HooksConfig{
		Enabled: true,
		On: []config.HookEntry{
			{Event: "task_started", Command: "echo {title} > " + started},
			{Event: "task_done", Command: "echo {task_id} >> " + done},
		},
	})
	t.Cleanup(c.detachHooks)

	ctx := context.Background()
	task, err := c.store.Create(ctx, "", "", "生成构建报告", "queue-node", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.store.Queue(ctx, task.TaskID, "queue-node"); err != nil {
		t.Fatal(err)
	}
	if err := c.store.Dispatch(ctx, task.TaskID, "queue-node", "queue-node"); err != nil {
		t.Fatal(err)
	}
	if err := c.store.Accept(ctx, task.TaskID, "queue-node"); err != nil {
		t.Fatal(err)
	}
	if !waitForFile(t, started) {
		t.Fatal("the started hook never fired")
	}
	if body, _ := os.ReadFile(started); !strings.Contains(string(body), "生成构建报告") {
		t.Errorf("the title was not substituted: %q", body)
	}

	if err := c.store.Complete(ctx, task.TaskID, "queue-node", map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}
	if !waitForFile(t, done) {
		t.Fatal("the done hook never fired")
	}
	if body, _ := os.ReadFile(done); !strings.Contains(string(body), task.TaskID) {
		t.Errorf("the task id was not substituted: %q", body)
	}
}

// TestHooksRefuseTier2ByDefault: a hook is unattended by definition, so an
// irreversible command must be refused unless the operator opted in.
func TestHooksRefuseTier2ByDefault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell fixtures")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "should-not-exist")

	c := queueTestCore(t)
	c.SetHooks(config.HooksConfig{
		Enabled: true,
		On: []config.HookEntry{
			{Event: "task_submitted", Command: "rm -rf " + marker + " && touch " + marker},
		},
	})
	t.Cleanup(c.detachHooks)

	if _, err := c.store.Create(context.Background(), "", "", "危险命令", "queue-node", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("a tier-2 hook ran without allow_tier2")
	}
}

// TestHooksAreNotDoubleWired: two cores on one database (the daemon and the ask
// engine) must not fire every hook twice.
func TestHooksAreNotDoubleWired(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell fixtures")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "count.log")

	db := openTestDB(t)
	card := ledger.Card{
		Device:        "queue-node",
		ResourceClass: "Standard",
		Capacity:      ledger.Capacity{CPUCores: 4, RAMGB: 8, MaxConcurrent: 2},
	}
	hooksCfg := config.HooksConfig{
		Enabled: true,
		On:      []config.HookEntry{{Event: "task_submitted", Command: "echo x >> " + marker}},
	}
	first := NewCore(db, "queue-node", card, 5, testLogger(), config.ModelConfig{})
	first.SetHooks(hooksCfg)
	t.Cleanup(first.detachHooks)
	second := NewCore(db, "queue-node", card, 5, testLogger(), config.ModelConfig{})
	second.SetHooks(hooksCfg)
	if second.hookRunner.Load() == nil {
		t.Fatal("the second core did not borrow the runner")
	}
	if second.hookUnsub != nil {
		t.Error("the second core registered a second observer")
	}

	if _, err := first.store.Create(context.Background(), "", "", "一次提交", "queue-node", nil); err != nil {
		t.Fatal(err)
	}
	if !waitForFile(t, marker) {
		t.Fatal("the hook never fired")
	}
	time.Sleep(300 * time.Millisecond)
	body, _ := os.ReadFile(marker)
	if got := strings.Count(string(body), "x"); got != 1 {
		t.Errorf("hook fired %d times, want exactly once", got)
	}
}

// countLines reports how many complete lines a hook log holds.
func countLines(path string) int {
	body, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(body), "\n")
}

func waitForLines(t *testing.T, path string, want int) bool {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if countLines(path) >= want {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// TestHooksFireOnFileBackedStore pins the observer's read against a pooled,
// file-backed database — the shape every real node runs. The observer fires
// inside the writer's transaction, so a naive read on another pooled
// connection sees the pre-commit snapshot: task_submitted finds no row and a
// result still sees the old running state. Both edges were dropped silently
// before the dispatch waited for the commit to become visible. :memory: tests
// cannot catch it (one connection blocks the read until commit).
func TestHooksFireOnFileBackedStore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell fixtures")
	}
	dir := t.TempDir()
	submitted := filepath.Join(dir, "submitted.log")
	done := filepath.Join(dir, "done.log")

	db, err := storage.Open(filepath.Join(dir, "hooks.db"))
	if err != nil {
		t.Fatalf("open file db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	card := ledger.Card{
		Device: "hooks-node",
		Native: []ledger.NativeAbility{
			{ID: "sys:info", Command: "echo", Args: []string{"hooks-ok"}},
		},
		Capacity: ledger.Capacity{CPUCores: 4, RAMGB: 8, MaxConcurrent: 2},
	}
	c := NewCore(db, "hooks-node", card, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())
	c.SetHooks(config.HooksConfig{
		Enabled: true,
		On: []config.HookEntry{
			{Event: "task_submitted", Command: "echo {task_id} >> " + submitted},
			{Event: "task_done", Command: "echo {task_id} >> " + done},
		},
	})
	t.Cleanup(c.detachHooks)

	ctx := context.Background()
	const n = 12
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		task, err := c.store.Create(ctx, "", "", fmt.Sprintf("文件库任务 %d", i), "hooks-node", nil)
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		ids = append(ids, task.TaskID)
		// One at a time: the runner intentionally drops past four concurrent
		// hook processes, and this test is about the store read, not the
		// concurrency ceiling.
		if !waitForLines(t, submitted, i+1) {
			t.Fatalf("task_submitted fired %d times, want %d", countLines(submitted), i+1)
		}
	}
	for i, id := range ids {
		if err := c.store.Queue(ctx, id, "hooks-node"); err != nil {
			t.Fatalf("queue: %v", err)
		}
		if err := c.store.Dispatch(ctx, id, "hooks-node", "hooks-node"); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if err := c.store.Accept(ctx, id, "hooks-node"); err != nil {
			t.Fatalf("accept: %v", err)
		}
		if err := c.store.Complete(ctx, id, "hooks-node", map[string]any{"ok": true}); err != nil {
			t.Fatalf("complete: %v", err)
		}
		if !waitForLines(t, done, i+1) {
			t.Fatalf("task_done fired %d times, want %d", countLines(done), i+1)
		}
	}
}

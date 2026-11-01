// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
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
	if second.hookRunner == nil {
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

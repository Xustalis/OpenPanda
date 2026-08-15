//go:build !windows

package executil

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestCancelKillsProcessGroup verifies P1-17: cancelling the context kills the
// whole process group, so a grandchild spawned by the direct child does not
// survive as an orphan.
func TestCancelKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")

	// sh spawns a sleep grandchild, records its pid, and waits. Killing only
	// sh would orphan the sleep; killing the group takes both.
	ctx, cancel := context.WithCancel(context.Background())
	cmd := CommandContext(ctx, "sh", "-c", "sleep 300 & echo $! > \"$1\"; wait", "sh", pidFile)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Wait for the grandchild pid to appear.
	var childPID int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				childPID = n
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatalf("grandchild pid never written")
	}

	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatalf("expected non-nil wait error after cancel")
	}

	// The grandchild must be gone too: signal 0 probes existence.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); err == syscall.ESRCH {
			return // grandchild reaped
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("grandchild pid %d survived group kill", childPID)
}

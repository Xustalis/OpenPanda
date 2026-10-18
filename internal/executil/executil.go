// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

// Package executil wraps os/exec so child processes can be hidden on Windows.
package executil

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// termGrace is the window a cancelled process group gets to wind down on
// SIGTERM before the escalation to SIGKILL. It only has to cover an
// interpreter's signal-handler trip — kill the child tree, flush, exit —
// so two seconds is generous; a group that ignores TERM earns the
// uncatchable kill right after.
const termGrace = 2 * time.Second

// CommandContext builds an exec.Cmd whose cancellation kills the whole
// process group, not just the direct child (P1-17). Without this, a cancelled
// python adapter would die while the claude CLI it spawned kept running as an
// orphan — burning tokens and holding file locks. The child is put in its own
// process group (Setpgid) so Cancel can signal the group by negative pid.
//
// Cancel escalates TERM → KILL instead of going straight to SIGKILL: the
// adapter harness deliberately spawns the agent CLI in ITS OWN session
// (start_new_session), unreachable from the adapter's process group. A bare
// SIGKILL removes the adapter before its watchdog can take the CLI tree down
// — SIGTERM gives the adapter's handler the one beat it needs to kill its
// children and exit. A group that ignores TERM still dies at termGrace.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		// Negative pid = the process group the child leads. The group is gone
		// if the child already exited and nothing inherited it; ESRCH is fine.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
			if err == syscall.ESRCH {
				return nil
			}
			return err
		}
		pid := cmd.Process.Pid
		time.AfterFunc(termGrace, func() {
			// Group already dead → ESRCH, harmless. A wedged adapter that
			// swallowed TERM gets the kill it cannot catch.
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		})
		return nil
	}
	return cmd
}

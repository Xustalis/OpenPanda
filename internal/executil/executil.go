//go:build !windows

// Package executil wraps os/exec so child processes can be hidden on Windows.
package executil

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

// CommandContext builds an exec.Cmd whose cancellation kills the whole
// process group, not just the direct child (P1-17). Without this, a cancelled
// python adapter would die while the claude CLI it spawned kept running as an
// orphan — burning tokens and holding file locks. The child is put in its own
// process group (Setpgid) so Cancel can signal the group by negative pid.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		// Negative pid = the process group the child leads. The group is gone
		// if the child already exited and nothing inherited it; ESRCH is fine.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			return err
		}
		return nil
	}
	return cmd
}

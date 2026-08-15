//go:build windows

package executil

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// CommandContext builds an exec.Cmd with a hidden console window
// (SysProcAttr.HideWindow), so child processes (python3, git, native commands)
// spawned by the headless kernel never pop a terminal on Windows.
//
// Cancellation kills the whole process tree via taskkill /T, not just the
// direct child (P1-17): a python adapter that spawned a CLI must not leave
// orphans burning tokens after the parent context is done. (A Job Object with
// KILL_ON_JOB_CLOSE would be the stricter form; taskkill covers the tree of
// processes that are still children at kill time.)
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
		if err != nil {
			// taskkill fails when the process already exited; fall back to a
			// plain kill so cancellation still guarantees the child is gone.
			return cmd.Process.Kill()
		}
		return nil
	}
	return cmd
}

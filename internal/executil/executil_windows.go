//go:build windows

package executil

import (
	"context"
	"os/exec"
	"syscall"
)

// CommandContext builds an exec.Cmd with a hidden console window
// (SysProcAttr.HideWindow), so child processes (python3, git, native commands)
// spawned by the headless kernel never pop a terminal on Windows.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

//go:build !windows

// Package executil wraps os/exec so child processes can be hidden on Windows.
package executil

import (
	"context"
	"os/exec"
)

// CommandContext builds an exec.Cmd. On Windows the child is created with its
// console window hidden so the headless kernel never pops a terminal; on other
// platforms it is a plain exec.CommandContext.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

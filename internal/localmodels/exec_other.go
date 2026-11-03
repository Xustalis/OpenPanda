// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package localmodels

import (
	"context"
	"os/exec"
	"time"
)

// shimCommand builds the invocation. A shell script with the executable bit
// set runs as-is, so the Unix side needs no shim handling.
func shimCommand(ctx context.Context, cli string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, cli, args...)
	// Same reason as the Windows shim: a CLI that forks helpers can keep our
	// pipe open after the parent is gone.
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

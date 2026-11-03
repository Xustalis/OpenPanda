// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package browser

import (
	"os/exec"
	"syscall"
)

// detach puts the host in its own session so a CLI invocation that ends, or a
// terminal that closes, does not take the browser with it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

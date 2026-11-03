// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// detachCommand starts the child without a console and in its own process
// group, so closing the terminal that ran `panda web --daemon` does not send
// CTRL_CLOSE_EVENT to the console process. `panda service install` is the
// boot-persistent path; this only fixes the one-shot background flag.
func detachCommand(cmd *exec.Cmd) {
	const (
		createNewProcessGroup = 0x00000200
		detachedProcess       = 0x00000008
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | detachedProcess,
	}
}

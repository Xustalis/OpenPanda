// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package browser

import (
	"os/exec"
	"syscall"
)

// DETACHED_PROCESS keeps the host out of the caller's console group.
const detachedProcess = 0x00000008

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess,
	}
}

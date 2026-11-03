// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package devserver

import (
	"os/exec"
	"strconv"
	"syscall"
)

// setProcessGroup gives the child its own console group so taskkill /T can
// reach the package manager's own children.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// /T walks the child tree; /F forces a server that ignores Ctrl-C.
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	_ = cmd.Process.Kill()
}

// shellCommand runs a project-declared command through cmd.exe.
func shellCommand(command string) (string, []string) {
	return "cmd", []string{"/C", command}
}

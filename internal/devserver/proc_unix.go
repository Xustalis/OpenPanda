// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package devserver

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the shell and everything it starts in one process
// group. A dev command is usually a package manager that forks the real
// server; killing only the shell would leave the port held.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	// The group id is the leader's pid; fall back to the process alone when
	// the group is already gone.
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}

// shellCommand runs a project-declared command the way its author expects:
// through the platform shell, so pipes, && and globs work.
func shellCommand(command string) (string, []string) {
	return "sh", []string{"-c", command}
}

// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detachCommand makes a background `panda web` survive the terminal that
// started it. Setsid gives the child its own session, so closing the shell
// (SIGHUP to the foreground group) no longer takes the console down. The
// proper long-lived path is still `panda service install`; this is what
// `--daemon` promises when someone has not registered a service.
func detachCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

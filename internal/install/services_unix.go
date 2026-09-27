//go:build !windows

package install

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// StopServices best-effort stops a user-level daemon if one is registered
// (LaunchAgent on macOS, user systemd unit on Linux — the units ship in
// scripts/ and deploy/). Failures are ignored: an unregistered service, a
// different init system, or no daemon running are all normal states during
// uninstall, and the unit files themselves belong to the OS packaging, not
// to this binary.
func StopServices() {
	_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/com.openpanda.node", os.Getuid())).Run()
	_ = exec.Command("systemctl", "--user", "disable", "--now", "openpanda").Run()
}

// RestartDaemon best-effort bounces a registered node daemon so it picks up a
// freshly swapped binary (self-update replaces the file on disk, but a running
// daemon keeps its old image until the service manager restarts it).
//
// It only ever touches services that are already running: launchd kickstart -k
// runs only when the agent is loaded and reports "state = running", and
// systemctl's try-restart is a no-op for inactive or absent units. A daemon
// started by hand in a terminal is intentionally left alone — there is no
// service to bounce — so the update surfaces the restart hint instead.
// Failures are ignored for the same reason as StopServices.
func RestartDaemon() {
	svc := fmt.Sprintf("gui/%d/com.openpanda.node", os.Getuid())
	if out, err := exec.Command("launchctl", "print", svc).Output(); err == nil &&
		strings.Contains(string(out), "state = running") {
		_ = exec.Command("launchctl", "kickstart", "-k", svc).Run()
	}
	if _, err := exec.LookPath("systemctl"); err == nil {
		_ = exec.Command("systemctl", "--user", "try-restart", "openpanda.service").Run()
	}
}

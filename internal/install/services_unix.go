//go:build !windows

package install

import (
	"fmt"
	"os"
	"os/exec"
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

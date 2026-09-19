//go:build windows

package install

import "os/exec"

// StopServices best-effort removes the logon task that scripts/install.ps1
// registers, so `panda uninstall` leaves no auto-start behind.
//
// It is a scheduled task, not a service. An earlier iteration registered a
// service through `sc create`, and this function still stopped that name long
// after the registration path moved to `schtasks` (progress.md records the
// move: a daemon started from an SSH session died with the session, and the
// logon task is what replaced it). Stopping `sc openpanda` therefore matched
// nothing that the installer ever created — the task survived uninstall and
// relaunched a daemon whose binary had just been deleted, at every logon,
// reporting the failure to a user who had already uninstalled the product.
//
// Failures stay ignored: no task registered is the normal case, and the task
// itself belongs to whoever created it.
func StopServices() {
	_ = exec.Command("schtasks.exe", "/Delete", "/TN", "OpenPandaNode", "/F").Run()
}

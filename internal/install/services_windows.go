//go:build windows

package install

import "os/exec"

// nodeTaskName is the scheduled task scripts/install.ps1 registers, and the
// name its own closing instructions tell the user to remove by hand.
//
// It is a cross-artifact contract, not an internal detail, and that is exactly
// how the bug described below happened: the name lived in three files, the
// registration moved to schtasks, and this one kept deleting something else.
// The value is a var rather than a const so the windows test can point
// StopServices at a scratch task instead of the real one — a test that deleted
// a developer's actual logon task would be worse than no test.
var nodeTaskName = "OpenPandaNode"

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
	_ = exec.Command("schtasks.exe", "/Delete", "/TN", nodeTaskName, "/F").Run()
}

// RestartDaemon best-effort restarts the registered logon task so it picks up
// a freshly swapped binary (self-update replaces the file on disk, but the
// running daemon keeps its old image until restarted). /End succeeds only
// when the task is currently running, so a stopped or absent task stays
// untouched — and the matching /Run brings the daemon back on the new image.
// Failures are ignored: no registered task is the normal case.
func RestartDaemon() {
	if err := exec.Command("schtasks.exe", "/End", "/TN", nodeTaskName).Run(); err != nil {
		return
	}
	_ = exec.Command("schtasks.exe", "/Run", "/TN", nodeTaskName).Run()
}

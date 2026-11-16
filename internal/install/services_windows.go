// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package install

import (
	"os"
	"os/exec"
	"path/filepath"
)

// nodeTaskName is the scheduled task scripts/install.ps1 registers, and the
// name its own closing instructions tell the user to remove by hand.
//
// It is a cross-artifact contract, not an internal detail, and that is exactly
// how the bug described below happened: the name lived in three files, the
// registration moved to schtasks, and this one kept deleting something else.
// It stays immutable: unit tests inject a complete service controller instead
// of replacing one global task name while leaving the Web task live.
const nodeTaskName = NodeTaskName

// windowsServiceControl keeps all scheduled-task and filesystem side effects
// together. Tests replace both operations on a local value; no production task
// names or process-wide hooks need to be mutated to exercise the full stop.
type windowsServiceControl struct {
	runCommand     func(name string, args ...string) error
	removeFile     func(path string) error
	nodeTask       string
	webTask        string
	webWrapperPath string
}

func installedWindowsServices() windowsServiceControl {
	return windowsServicesWith(func(name string, args ...string) error {
		return exec.Command(name, args...).Run()
	}, os.Remove, webServiceWrapperPath())
}

func windowsServicesWith(runCommand func(string, ...string) error, removeFile func(string) error, wrapperPath string) windowsServiceControl {
	return windowsServiceControl{
		runCommand:     runCommand,
		removeFile:     removeFile,
		nodeTask:       nodeTaskName,
		webTask:        WebServiceTaskName,
		webWrapperPath: wrapperPath,
	}
}

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
	installedWindowsServices().stopServices()
}

func (s windowsServiceControl) stopServices() {
	_ = s.runCommand("schtasks.exe", "/Delete", "/TN", s.nodeTask, "/F")
	s.stopWebService()
}

// RestartDaemon best-effort restarts the registered logon task so it picks up
// a freshly swapped binary (self-update replaces the file on disk, but the
// running daemon keeps its old image until restarted). /End succeeds only
// when the task is currently running, so a stopped or absent task stays
// untouched — and the matching /Run brings the daemon back on the new image.
// Failures are ignored: no registered task is the normal case.
func RestartDaemon() {
	s := installedWindowsServices()
	s.restartTask(s.nodeTask)
}

// RestartWebService restarts the console's logon task so the panel answers from
// the binary on disk rather than the image it started with. Same running-only
// contract as the daemon: /End succeeds only for a live task.
func RestartWebService() {
	s := installedWindowsServices()
	s.restartTask(s.webTask)
}

func (s windowsServiceControl) restartTask(name string) {
	if err := s.runCommand("schtasks.exe", "/End", "/TN", name); err != nil {
		return
	}
	_ = s.runCommand("schtasks.exe", "/Run", "/TN", name)
}

// webServiceWrapperPath mirrors the wrapper `panda service install` writes
// under the per-user data directory (the install prefix may be read-only).
func webServiceWrapperPath() string {
	if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
		return filepath.Join(dir, "openpanda-data", "panda-web.cmd")
	}
	return ""
}

func (s windowsServiceControl) stopWebService() {
	_ = s.runCommand("schtasks.exe", "/End", "/TN", s.webTask)
	_ = s.runCommand("schtasks.exe", "/Delete", "/TN", s.webTask, "/F")
	if s.webWrapperPath != "" {
		_ = s.removeFile(s.webWrapperPath)
	}
}

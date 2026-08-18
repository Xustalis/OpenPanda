//go:build windows

package install

import "os/exec"

// StopServices best-effort stops the openpanda service if one is registered
// via `sc create`. Failures are ignored — no service registered is the
// common case; the service definition itself belongs to the admin, not us.
func StopServices() {
	_ = exec.Command("sc.exe", "stop", "openpanda").Run()
}

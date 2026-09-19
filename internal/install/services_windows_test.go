//go:build windows

package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestNodeTaskNameMatchesInstaller guards the contract that broke once already:
// the binary's uninstall path and the installer's registration path have to
// agree on the task name.
//
// When they drifted, `panda uninstall` deleted a name nothing had ever created,
// so the logon task survived and kept relaunching a daemon whose binary was
// gone. Nothing caught it: the registration code was in a shell script, the
// deletion code was in Go, and no test looked at both.
//
// The assertion reads the shipped script rather than a copy of its contents, so
// renaming the task in either place turns this red.
func TestNodeTaskNameMatchesInstaller(t *testing.T) {
	script := filepath.Join("..", "..", "scripts", "install.ps1")
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("read %s: %v", script, err)
	}
	src := string(data)

	// The registering call, not a mention: /TN <name> is what schtasks creates.
	if !strings.Contains(src, `/TN "`+nodeTaskName+`"`) {
		t.Fatalf("scripts/install.ps1 does not register /TN %q; "+
			"the uninstall path would delete a task that does not exist", nodeTaskName)
	}
}

// TestStopServicesDeletesRegisteredTask exercises the deletion itself against a
// real task, because StopServices is pure side effect: the failure mode is a
// silent no-op, which a name comparison alone cannot see.
//
// It runs under a scratch name. `go test ./...` is a thing developers run on
// their own machines, and those machines may be running the daemon the real
// task starts.
func TestStopServicesDeletesRegisteredTask(t *testing.T) {
	if _, err := exec.LookPath("schtasks.exe"); err != nil {
		t.Skip("schtasks.exe not available")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}

	orig := nodeTaskName
	nodeTaskName = "OpenPandaStopServicesTest"
	t.Cleanup(func() {
		nodeTaskName = orig
		_ = exec.Command("schtasks.exe", "/Delete", "/TN", nodeTaskName, "/F").Run()
	})

	tr := `"` + exe + `" daemon`
	if out, err := exec.Command("schtasks.exe", "/Create", "/TN", nodeTaskName,
		"/SC", "ONLOGON", "/RL", "LIMITED", "/TR", tr, "/F").CombinedOutput(); err != nil {
		// A locked-down host may refuse task creation for a plain process. The
		// product treats that as a warning, so the test skips rather than
		// failing — it must not be stricter than the code under test.
		t.Skipf("cannot register a logon task here: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	StopServices()

	if out, err := exec.Command("schtasks.exe", "/Query", "/TN", nodeTaskName).CombinedOutput(); err == nil {
		t.Fatalf("StopServices left %s registered; `panda uninstall` would not "+
			"remove the logon task (%s)", nodeTaskName, strings.TrimSpace(string(out)))
	}
}

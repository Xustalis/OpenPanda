//go:build windows

package updater

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// replaceBinary on Windows: the running image is locked, so it is renamed to a
// .old sidecar first, the new binary is written into place, and the .old is
// swept after the new process starts (see SweepResidue).
func replaceBinary(src, dst string) error {
	old := dst + ".old"
	_ = os.Remove(old) // sweep a stale .old from a previous run
	if err := os.Rename(dst, old); err != nil {
		return fmt.Errorf("rename running binary: %w", err)
	}
	if err := copyFile(src, dst, 0o755); err != nil {
		_ = os.Rename(old, dst) // restore on failure
		return fmt.Errorf("write new binary: %w", err)
	}
	return nil
}

// restartSelf spawns the new binary and exits. The fresh process calls
// SweepResidue at startup to remove the .old sidecar once it is unlocked.
func restartSelf() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = os.Environ()
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start new process: %w", err)
	}
	_ = cmd.Process.Release()
	os.Exit(0)
	return nil
}

// SweepResidue removes a leftover `<exe>.old` from a prior Windows update. It
// is called from startup so the swap's sidecar never lingers.
func SweepResidue() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	_ = os.Remove(exe + ".old")
}

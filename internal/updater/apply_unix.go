//go:build !windows

package updater

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// replaceBinary atomically swaps the running binary. On Unix a running image
// is never locked, so a rename onto the live binary is atomic: the old inode
// stays valid for the running process until it exits.
func replaceBinary(src, dst string) error {
	tmp := dst + ".new"
	if err := copyFile(src, tmp, 0o755); err != nil {
		return fmt.Errorf("stage: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// restartSelf re-executes the current binary in place, preserving args and
// environment. Go marks its sockets close-on-exec, so the listener is released
// and the new process re-binds cleanly.
func restartSelf() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	return errors.New("unreachable")
}

// SweepResidue is a no-op on Unix: the atomic rename-over leaves no sidecar.
func SweepResidue() {}

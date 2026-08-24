// Package nodeidentity owns the process-wide identity lock for a running node.
// It deliberately keys locks by node kind and stable identity: one physical
// node per host, while a VM on that host may hold a separate VM lock.
package nodeidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	KindPhysical = "physical"
	KindVM       = "vm"
)

// ErrAlreadyRunning identifies a live process holding the same node identity.
// Callers should use errors.Is instead of parsing platform-specific errors.
var ErrAlreadyRunning = errors.New("node identity already running")

// Lock is held for the lifetime of a daemon process.
type Lock struct {
	file *os.File
	path string
}

// Acquire obtains the exclusive local lock for kind/identity. The OS-level
// advisory lock is released automatically if the process exits unexpectedly.
func Acquire(kind, identity string) (*Lock, error) {
	kind = strings.TrimSpace(kind)
	identity = strings.TrimSpace(identity)
	if kind == "" {
		kind = KindPhysical
	}
	if kind != KindPhysical && kind != KindVM {
		return nil, fmt.Errorf("unsupported node kind %q", kind)
	}
	if identity == "" {
		return nil, fmt.Errorf("node identity is empty")
	}
	path, err := lockPath(kind, identity)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create node lock directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open node lock %s: %w", path, err)
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("node %s identity %q: %w: %v", kind, identity, ErrAlreadyRunning, err)
	}
	return &Lock{file: f, path: path}, nil
}

// Held reports whether another process currently owns the identity lock.
func Held(kind, identity string) (bool, error) {
	lock, err := Acquire(kind, identity)
	if err == nil {
		_ = lock.Release()
		return false, nil
	}
	if errors.Is(err, ErrAlreadyRunning) {
		return true, nil
	}
	return false, err
}

// Release releases the OS lock. The lock file itself is intentionally kept:
// that makes startup races deterministic and avoids stale PID-file recovery.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unlockFile(l.file)
	closeErr := l.file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func lockPath(kind, identity string) (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve node lock directory: %w", err)
	}
	sum := sha256.Sum256([]byte(identity))
	return filepath.Join(root, "openpanda", "locks", kind+"-"+hex.EncodeToString(sum[:12])+".lock"), nil
}

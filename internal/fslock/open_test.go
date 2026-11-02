// SPDX-License-Identifier: AGPL-3.0-or-later

package fslock

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// withOpenSeams swaps the package seams for one test and restores them.
func withOpenSeams(t *testing.T, open func(string, int, os.FileMode) (*os.File, error), retry func(error) bool) {
	t.Helper()
	prevOpen, prevRetry := openLockFile, retryableOpen
	openLockFile, retryableOpen = open, retry
	t.Cleanup(func() { openLockFile, retryableOpen = prevOpen, prevRetry })
}

// An existing marker must be opened read-only: a write open is what Windows
// answers with ACCESS_DENIED while another process creates the same file.
func TestExistingLockOpenedReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var flags []int
	withOpenSeams(t, func(name string, flag int, perm os.FileMode) (*os.File, error) {
		flags = append(flags, flag)
		return os.OpenFile(name, flag, perm)
	}, func(error) bool { return false })

	release, err := Acquire(path, false)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	release()
	if len(flags) != 1 {
		t.Fatalf("open calls = %d, want 1", len(flags))
	}
	if flags[0]&(os.O_WRONLY|os.O_RDWR|os.O_CREATE) != 0 {
		t.Errorf("existing lock opened with flags %#x, want read-only", flags[0])
	}
}

// The loser of a create race sees ErrExist and must fall back to a read-only
// open of the file the winner created.
func TestCreateRaceFallsBackToReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	// The winner's file exists by the time the loser's fallback runs.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	var lastFlags int
	withOpenSeams(t, func(name string, flag int, perm os.FileMode) (*os.File, error) {
		calls++
		lastFlags = flag
		switch calls {
		case 1: // first read: not created yet
			return nil, fs.ErrNotExist
		case 2: // create: another writer won
			return nil, fs.ErrExist
		default:
			return os.OpenFile(name, flag, perm)
		}
	}, func(error) bool { return false })

	release, err := Acquire(path, false)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	release()
	if calls != 3 {
		t.Fatalf("open calls = %d, want 3 (read, create, read)", calls)
	}
	if lastFlags&(os.O_WRONLY|os.O_RDWR|os.O_CREATE) != 0 {
		t.Errorf("fallback opened with flags %#x, want read-only", lastFlags)
	}
}

// A transient open error the platform classifies as retryable must be retried
// until the marker becomes openable.
func TestRetriesTransientOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	transient := errors.New("transient sharing violation")
	calls := 0
	withOpenSeams(t, func(name string, flag int, perm os.FileMode) (*os.File, error) {
		calls++
		if calls <= 2 {
			return nil, transient
		}
		return os.OpenFile(name, flag, perm)
	}, func(err error) bool { return errors.Is(err, transient) })

	release, err := Acquire(path, false)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	release()
	if calls < 3 {
		t.Errorf("open calls = %d, want at least 3", calls)
	}
}

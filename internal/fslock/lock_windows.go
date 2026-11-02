// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package fslock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryLock takes the exclusive byte-range lock Windows offers in place of
// flock. LOCKFILE_FAIL_IMMEDIATELY is the non-blocking mode; a lock violation
// is contention, while a bad handle or a volume that cannot lock is an error.
// Range 0/1 is enough: the lock file is a dedicated marker, never data.
func tryLock(f *os.File, blocking bool) (held, unsupported bool, err error) {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if !blocking {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	err = windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &windows.Overlapped{})
	switch {
	case err == nil:
		return false, false, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return true, false, nil
	default:
		return false, false, err
	}
}

// unlock releases the same range. The OVERLAPPED of the lock is not needed
// here: UnlockFileEx identifies the range by offset and length, and the lock
// also dies with the handle when Acquire's release closes it.
func unlock(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{})
}

// platformRetryableOpen reports errors that are transient while another process
// creates or holds the marker file: a sharing violation (AV/indexer holding it)
// and an access denial in the narrow create window both clear on a retry.
// A persistent ACL denial still surfaces after openRetryWindow.
func platformRetryableOpen(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_ACCESS_DENIED)
}

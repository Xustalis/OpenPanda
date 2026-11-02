// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package fslock

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// tryLock takes an exclusive flock. The three outcomes are kept distinct
// because the caller's diagnosis depends on it: a would-block answer is
// contention ("someone else has it"), an ENOLCK/EOPNOTSUPP answer means the
// filesystem has no locks at all, and anything else — EACCES, EBADF, EIO — is
// a real failure that must not be reported as "held". Folding them together is
// how a probe ends up claiming a daemon owns a node row when the truth is that
// the lock directory is unreadable.
func tryLock(f *os.File, blocking bool) (held, unsupported bool, err error) {
	how := unix.LOCK_EX
	if !blocking {
		how |= unix.LOCK_NB
	}
	for {
		err = unix.Flock(int(f.Fd()), how)
		switch {
		case err == nil:
			return false, false, nil
		case errors.Is(err, syscall.EINTR):
			continue // a signal interrupted the wait; ask again
		case errors.Is(err, syscall.EWOULDBLOCK), errors.Is(err, syscall.EAGAIN):
			return true, false, nil
		case errors.Is(err, syscall.ENOLCK), errors.Is(err, syscall.EOPNOTSUPP):
			return false, true, nil
		default:
			return false, false, err
		}
	}
}

func unlock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}

// platformRetryableOpen is false on POSIX: open contention is a permission
// error, not a transient sharing state, and retrying it would only delay the
// caller's diagnosis.
func platformRetryableOpen(error) bool { return false }

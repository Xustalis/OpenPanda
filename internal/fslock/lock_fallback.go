// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly || windows)

package fslock

import "os"

// tryLock reports "no lock primitive on this platform" rather than pretending
// to have taken one: the caller's in-process mutex still serializes goroutines,
// and a second process writing the same store remains a known gap here.
func tryLock(*os.File, bool) (held, unsupported bool, err error) {
	return false, true, nil
}

func unlock(*os.File) error { return nil }

// platformRetryableOpen is false where there is no locking primitive to race.
func platformRetryableOpen(error) bool { return false }

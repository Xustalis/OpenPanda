// SPDX-License-Identifier: AGPL-3.0-or-later

// Package fslock takes an exclusive advisory lock on a file, so a
// read-modify-write cycle is atomic ACROSS PROCESSES and not only across
// goroutines.
//
// The pattern it exists for is the whole-file store: something loads a file
// into memory, changes it, and writes the whole thing back. Two writers that
// overlap lose one of the two changes, and the loss is silent — the file still
// parses, it is just missing an entry nobody can point at. The daemon, the CLI
// and the web console all open the same stores, and inside one daemon the
// subagents of a single round run in parallel, so "two writers" is the normal
// case rather than an edge one.
//
// The lock is advisory: it coordinates the callers that take it. A foreign
// program that writes the same file without looking is outside the contract.
package fslock

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrHeld reports that another holder has the lock and the caller asked not to
// wait. It is contention, never a failure: a caller that probes ("is this
// identity taken?") maps it to its own sentinel, while one that needs the lock
// uses blocking mode instead.
var ErrHeld = errors.New("fslock: lock held by another process")

// Acquire takes an exclusive advisory lock on path, creating the file and its
// directory as needed. blocking=false returns ErrHeld instead of waiting.
//
// The returned release unlocks and closes the file; it is safe to call exactly
// once. A platform without a lock primitive, and a filesystem that reports it
// has none, both degrade to a no-op release rather than failing the caller —
// the caller's own mutex still serializes goroutines there, and refusing to
// write a user's memory because their data directory sits on an NFS mount
// would trade a rare silent loss for a guaranteed loud breakage. Any other
// error (a permission, a bad handle) is returned: those are the caller's
// problem to surface.
func Acquire(path string, blocking bool) (release func(), err error) {
	if path == "" {
		return func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := openWithRetry(path)
	if err != nil {
		return nil, err
	}
	held, unsupported, err := tryLock(f, blocking)
	switch {
	case unsupported:
		// No lock primitive here: the store still works, one writer at a time
		// per process. Reported by the platform shim, not an error.
		_ = f.Close()
		return func() {}, nil
	case err != nil:
		_ = f.Close()
		return nil, err
	case held:
		_ = f.Close()
		return nil, ErrHeld
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = unlock(f)
			_ = f.Close()
		})
	}, nil
}

// The lock file is a marker, never data. Existing files are opened read-only:
// an exclusive open requests write access, and on Windows a concurrent creator
// (or a filter/AV holding the file) answers that with ERROR_ACCESS_DENIED long
// before LockFileEx is reached. A read handle is enough for both flock and
// LockFileEx. Creation uses O_EXCL, so the loser of a create race falls back
// to the read-only open instead of retrying a write open. A short, Windows-only
// retry covers the window while another process is creating the same file.
const (
	openRetryWindow = time.Second
	openRetryDelay  = 5 * time.Millisecond
	openRetryMax    = 100 * time.Millisecond
)

// Seams kept as variables so tests can exercise the create race and the
// transient-open retry without a Windows host.
var openLockFile = os.OpenFile
var retryableOpen = platformRetryableOpen

func openLock(path string) (*os.File, error) {
	f, err := openLockFile(path, os.O_RDONLY, 0)
	if err == nil {
		fi, statErr := f.Stat()
		if statErr != nil {
			_ = f.Close()
			return nil, statErr
		}
		// The old O_RDWR open failed on a directory; O_RDONLY would succeed,
		// which would silently "lock" something that is not a lock file.
		if fi.IsDir() {
			_ = f.Close()
			return nil, &os.PathError{Op: "open", Path: path, Err: fs.ErrInvalid}
		}
		return f, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	f, err = openLockFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err == nil {
		return f, nil
	}
	if errors.Is(err, fs.ErrExist) {
		return openLockFile(path, os.O_RDONLY, 0)
	}
	return nil, err
}

func openWithRetry(path string) (*os.File, error) {
	deadline := time.Now().Add(openRetryWindow)
	delay := openRetryDelay
	for {
		f, err := openLock(path)
		if err == nil || !retryableOpen(err) || !time.Now().Before(deadline) {
			return f, err
		}
		time.Sleep(delay)
		delay *= 2
		if delay > openRetryMax {
			delay = openRetryMax
		}
	}
}

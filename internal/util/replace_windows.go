// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package util

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// Ordinary Windows read handles do not share DELETE, so a concurrent reader
// can make replacement return access denied or a sharing violation. Keep the
// synced temporary file while that short conflict clears. Persistent failures
// still return the original error and leave the prior destination intact.
func replaceFile(source, target string) error {
	deadline := time.Now().Add(100 * time.Millisecond)
	for {
		err := os.Rename(source, target)
		if (!errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED)) || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

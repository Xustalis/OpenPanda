// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package sessions

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// A different Store (or process) may briefly deny opens while replacing the
// checkpoint file. Retry only that Windows sharing conflict; missing files,
// permissions and malformed JSON must retain their ordinary errors.
func readSessionFile(path string) ([]byte, error) {
	deadline := time.Now().Add(100 * time.Millisecond)
	for {
		data, err := os.ReadFile(path)
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) || !time.Now().Before(deadline) {
			return data, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

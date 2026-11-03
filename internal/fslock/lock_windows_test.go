// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package fslock

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestRetryableOpenClassifiesWindowsSharingErrors(t *testing.T) {
	if !platformRetryableOpen(windows.ERROR_ACCESS_DENIED) {
		t.Error("ERROR_ACCESS_DENIED should be retried inside the create window")
	}
	if !platformRetryableOpen(windows.ERROR_SHARING_VIOLATION) {
		t.Error("ERROR_SHARING_VIOLATION should be retried")
	}
	if platformRetryableOpen(windows.ERROR_FILE_NOT_FOUND) {
		t.Error("ERROR_FILE_NOT_FOUND is a real miss, not a transient open")
	}
}

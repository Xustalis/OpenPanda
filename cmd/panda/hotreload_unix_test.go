// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package main

import (
	"os"
	"os/exec"
	"testing"
)

// looksLikePandaProcess must name our own process (the test binary's comm is
// its own basename, which is exactly what a same-name CLI build produces) and
// must refuse a pid that does not exist — the stale-pidfile case.
func TestLooksLikePandaProcess(t *testing.T) {
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("ps not available")
	}
	if !looksLikePandaProcess(os.Getpid()) {
		t.Fatal("own pid not recognized — SIGHUP would never reach the daemon")
	}
	// PID 0 is rejected by the caller's parse check upstream, and a huge
	// pid simply does not exist: both must fail closed.
	if looksLikePandaProcess(1 << 30) {
		t.Fatal("nonexistent pid passed the identity check")
	}
}

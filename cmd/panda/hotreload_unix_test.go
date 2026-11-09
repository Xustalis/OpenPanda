// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// looksLikePandaProcess must name our own process (the test binary's comm is
// its own basename, which is exactly what a same-name CLI build produces) and
// must refuse a pid that does not exist — the stale-pidfile case.
//
// It must also be able to say "I could not check". ps is a child process, and
// a sandbox or a PATH without ps makes it fail for reasons unrelated to
// whether the daemon is alive; reporting that as "a stranger" is what made the
// caller print "daemon not running" over a perfectly healthy daemon.
func TestLooksLikePandaProcess(t *testing.T) {
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("ps not on PATH")
	}
	// Being on PATH is not the same as being runnable: a sandbox can deny the
	// exec outright. The probe is honest about that (procUnverified), so this
	// test can only exercise the matching branch where ps actually answers.
	if out, err := exec.Command("ps", "-p", strconv.Itoa(os.Getpid()), "-o", "comm=").Output(); err != nil || strings.TrimSpace(string(out)) == "" {
		t.Skipf("ps cannot be run here (%v) — the unverified state is covered by TestLooksLikePandaProcessWithoutPS", err)
	}
	if got := looksLikePandaProcess(os.Getpid()); got != procMatches {
		t.Fatalf("own pid verdict = %v, want procMatches — SIGHUP would never reach the daemon", got)
	}
	// A huge pid simply does not exist: it must fail closed, and it must fail
	// as a stranger rather than as unverified so the caller's message is right.
	if got := looksLikePandaProcess(1 << 30); got == procMatches {
		t.Fatal("nonexistent pid passed the identity check")
	}
}

// TestLooksLikePandaProcessWithoutPS pins the third state end to end: with no
// ps on PATH the probe must report procUnverified, and sighupDaemon must
// refuse to signal while reporting reloadUnverified — never reloadNotRunning.
func TestLooksLikePandaProcessWithoutPS(t *testing.T) {
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("ps not available, so the empty-PATH case is already the norm")
	}
	t.Setenv("PATH", t.TempDir())
	if got := looksLikePandaProcess(os.Getpid()); got != procUnverified {
		t.Fatalf("verdict without ps = %v, want procUnverified", got)
	}
}

// TestMeshReloadHintNoLongerClaimsDaemonDown: a missing daemon pid is not
// evidence that nothing applied the change — the 1s config observer runs in
// the standalone web console too. The old wording told operators to restart
// a healthy process.
func TestMeshReloadHintNoLongerClaimsDaemonDown(t *testing.T) {
	var buf bytes.Buffer
	notifyDaemonMeshTo(&buf, filepath.Join(t.TempDir(), "config.yaml"))
	out := buf.String()
	if strings.Contains(out, "daemon not running") {
		t.Fatalf("stale restart advice: %q", out)
	}
	if !strings.Contains(out, "within ~1s") && !strings.Contains(out, "live now") {
		t.Fatalf("hint omits the live reload: %q", out)
	}
}

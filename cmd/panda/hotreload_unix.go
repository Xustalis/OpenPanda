// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package main

// Daemon hot-reload signaling (unix). The daemon writes its PID next to the
// database at startup; a card write reads it, checks the process is alive
// with signal 0, and SIGHUPs it into reloading the card — the difference
// between "the edit is live now" and "the edit is live after someone
// remembers to restart".

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// daemonPIDFile returns where the daemon records its PID, next to the
// database the given config resolves to. Empty when the config cannot be
// resolved silently (the caller then just prints the restart hint).
//
// configPath must be the path the caller just wrote (or, for card edits that
// touch no config, the CLI's resolved config — pass cliConfigPath so the
// global --config flag is honored). Resolving the default chain here instead
// would signal a daemon governed by a different config: `nodes add --config
// node-b.yaml` must SIGHUP node-b's daemon, never the default node's —
// multi-node hosts run one daemon per config and only the edited one's peer
// list is stale.
func daemonPIDFile(configPath string) string {
	cfg, err := loadConfigQuietly(configPath)
	if err != nil || cfg.Storage.DBPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(cfg.Storage.DBPath), "daemon.pid")
}

// notifyDaemonReload reports the reload outcome on process stdout — the CLI
// spelling. REPL/TUI callers use notifyDaemonReloadTo with their scoped
// writer instead, so the lines land in the transcript rather than on the
// frame Bubble Tea is repainting.
func notifyDaemonReload(configPath string) {
	notifyDaemonReloadTo(os.Stdout, configPath)
}

// reloadOutcome is why a SIGHUP did or did not happen. It exists because
// "not signaled" is not one condition: a daemon that is genuinely gone, a pid
// whose owner could not be verified, and a signal that failed all leave the
// user with a different thing to do, and collapsing them into one bool made
// the caller print "daemon not running" when the daemon was running fine.
type reloadOutcome int

const (
	reloadSignaled     reloadOutcome = iota // SIGHUP delivered
	reloadNotRunning                        // no pidfile, unreadable, or the pid is gone
	reloadStranger                          // the pid belongs to another program
	reloadUnverified                        // ps could not be consulted
	reloadSignalFailed                      // the pid was ours but the signal failed
)

// sighupDaemon delivers SIGHUP to the daemon PID recorded next to the
// database configPath's config points at. Signal 0 probes liveness first: a
// stale PID file from a crashed daemon must not turn into a signal to an
// unrelated process that happened to reuse the number.
func sighupDaemon(configPath string) (int, reloadOutcome) {
	pidFile := daemonPIDFile(configPath)
	if pidFile == "" {
		return 0, reloadNotRunning
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, reloadNotRunning
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, reloadNotRunning
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return 0, reloadNotRunning
	}
	switch verdict := looksLikePandaProcess(pid); verdict {
	case procStranger:
		return 0, reloadStranger
	case procUnverified:
		// Fail closed — SIGHUP's default action terminates whatever it lands
		// on — but say so honestly instead of claiming the daemon is gone.
		return pid, reloadUnverified
	}
	if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
		fmt.Fprintf(os.Stderr, "could not signal daemon (pid %d): %v\n", pid, err)
		return pid, reloadSignalFailed
	}
	return pid, reloadSignaled
}

// procVerdict is what the pidfile identity probe could establish. The third
// state is the point: ps is a child process, and a sandbox, a stripped
// container, or a PATH without ps all make it fail for reasons that have
// nothing to do with whether the daemon is running.
type procVerdict int

const (
	procMatches    procVerdict = iota // comm names this binary
	procStranger                      // comm names some other program
	procUnverified                    // ps could not answer
)

// looksLikePandaProcess keeps a stale pidfile from becoming a footgun: a
// crashed daemon leaves the file, the number gets reused by an unrelated
// same-user process, and signal 0 alone cannot tell them apart — SIGHUP's
// default action terminates whatever it lands on. Comm names the program;
// anything that does not look like this binary degrades to the restart hint
// instead of signaling a stranger.
//
// When ps itself is unavailable this returns procUnverified rather than
// procStranger: the caller still refuses to signal (fail closed), but it must
// not tell the user the daemon is down when the truth is that we could not
// check.
func looksLikePandaProcess(pid int) procVerdict {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return procUnverified
	}
	comm := strings.ToLower(filepath.Base(strings.TrimSpace(string(out))))
	if comm == "" || comm == "." {
		// ps succeeded but printed nothing: the pid vanished between the
		// liveness probe and this call.
		return procStranger
	}
	self := strings.ToLower(filepath.Base(os.Args[0]))
	if comm == self || strings.Contains(comm, "panda") {
		return procMatches
	}
	return procStranger
}

// notifyDaemonReloadTo SIGHUPs a running daemon so it hot-reloads the card,
// and reports which of the two outcomes happened. A dead PID file (crashed
// daemon), a missing one (daemon never started), or a config that cannot be
// resolved all degrade to the restart hint — the card on disk is already the
// new one either way.
func notifyDaemonReloadTo(out io.Writer, configPath string) {
	pid, outcome := sighupDaemon(configPath)
	switch outcome {
	case reloadSignaled:
		fmt.Fprintf(out, "daemon (pid %d) told to reload the card — changes are live\n", pid)
	case reloadUnverified:
		fmt.Fprintf(out, "daemon (pid %d) could not be verified (ps unavailable); no signal sent — running instances reload the card automatically within ~1s\n", pid)
	case reloadStranger:
		fmt.Fprintln(out, "the recorded pid belongs to another program — running instances reload the card automatically within ~1s")
	case reloadSignalFailed:
		fmt.Fprintln(out, "the daemon could not be signaled — running instances reload the card automatically within ~1s")
	default:
		fmt.Fprintln(out, "no daemon pid to signal — running instances reload the card automatically within ~1s; otherwise it applies at next start")
	}
}

// notifyDaemonMeshTo is the peer-list counterpart of notifyDaemonReloadTo.
// SIGHUP is the fast path when a daemon pid is available. It is NOT the only
// path: both the daemon and the standalone web console run a 1-second
// config.yaml observer (runNetworkSync), so a missing or unverifiable pid
// does not mean nothing applied the change. The old "daemon not running —
// takes effect at its next start" wording told operators to restart a
// healthy process, which is both unnecessary and a source of identity-lock
// races.
func notifyDaemonMeshTo(out io.Writer, configPath string) {
	pid, outcome := sighupDaemon(configPath)
	switch outcome {
	case reloadSignaled:
		fmt.Fprintf(out, "daemon (pid %d) applied the new peer list — live now\n", pid)
	case reloadUnverified:
		fmt.Fprintf(out, "daemon (pid %d) could not be verified (ps unavailable); no signal sent — running instances reload the peer list automatically within ~1s\n", pid)
	case reloadStranger:
		fmt.Fprintln(out, "the recorded pid belongs to another program — running instances reload the peer list automatically within ~1s")
	case reloadSignalFailed:
		fmt.Fprintln(out, "the daemon could not be signaled — running instances reload the peer list automatically within ~1s")
	default:
		fmt.Fprintln(out, "no daemon pid to signal — running instances reload the peer list automatically within ~1s; otherwise it applies at next start")
	}
}

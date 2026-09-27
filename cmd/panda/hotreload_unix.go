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
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// daemonPIDFile returns where the daemon records its PID, next to the
// database the config resolves to. Empty when the config cannot be resolved
// silently (the caller then just prints the restart hint).
func daemonPIDFile() string {
	cfg, err := loadConfigQuietly("")
	if err != nil || cfg.Storage.DBPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(cfg.Storage.DBPath), "daemon.pid")
}

// notifyDaemonReload reports the reload outcome on process stdout — the CLI
// spelling. REPL/TUI callers use notifyDaemonReloadTo with their scoped
// writer instead, so the lines land in the transcript rather than on the
// frame Bubble Tea is repainting.
func notifyDaemonReload() {
	notifyDaemonReloadTo(os.Stdout)
}

// notifyDaemonReloadTo SIGHUPs a running daemon so it hot-reloads the card,
// and reports which of the two outcomes happened. A dead PID file (crashed
// daemon), a missing one (daemon never started), or a config that cannot be
// resolved all degrade to the restart hint — the card on disk is already the
// new one either way.
func notifyDaemonReloadTo(out io.Writer) {
	pidFile := daemonPIDFile()
	if pidFile == "" {
		fmt.Fprintln(out, "restart the daemon (or send it SIGHUP) for the new card to be advertised to peers")
		return
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		fmt.Fprintln(out, "daemon not running — the new card is picked up at its next start")
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		fmt.Fprintln(out, "daemon not running — the new card is picked up at its next start")
		return
	}
	// Signal 0 probes liveness without delivering anything: a stale PID file
	// from a crashed daemon must not turn into a signal to an unrelated
	// process that happened to reuse the number.
	if err := syscall.Kill(pid, 0); err != nil {
		fmt.Fprintln(out, "daemon not running — the new card is picked up at its next start")
		return
	}
	if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
		fmt.Fprintf(out, "could not signal daemon (pid %d): %v\n", pid, err)
		fmt.Fprintln(out, "restart the daemon (or send it SIGHUP) for the new card to be advertised to peers")
		return
	}
	fmt.Fprintf(out, "daemon (pid %d) told to reload the card — changes are live\n", pid)
}

//go:build windows

package main

// Daemon hot-reload signaling (Windows). Windows has no SIGHUP, so the CLI
// cannot nudge a running daemon into reloading its card; the honest outcome
// is the restart hint. The daemon-side SIGHUP handler simply never fires
// here (signal.Notify accepts the constant but the OS never delivers it),
// so no code is lost — the same binary still hot-reloads on unix hosts.

import "fmt"

// notifyDaemonReload reports that a card edit needs a daemon restart. Kept
// as a function (not a plain print at the call site) so the unix and windows
// call sites stay identical and the difference cannot leak elsewhere.
func notifyDaemonReload() {
	fmt.Println("restart the daemon for the new card to be advertised to peers")
}

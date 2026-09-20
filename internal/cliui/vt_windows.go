//go:build windows

package cliui

import (
	"os"
	"sync"

	"golang.org/x/sys/windows"
)

// ANSI VT processing on Windows consoles is opt-in per handle. Windows
// Terminal and other ConPTY hosts enable it themselves, but the classic
// console host (cmd.exe, Windows PowerShell 5.1, double-clicked binaries)
// does not: without ENABLE_VIRTUAL_TERMINAL_PROCESSING every SGR sequence the
// palette emits prints as raw `[31m` garbage. This gate enables the flag on
// first use and then answers the one question the callers need: may ANSI be
// emitted on stdout right now?
var (
	vtOnce sync.Once
	vtOK   bool
)

// WindowsVTReady reports whether ANSI VT sequences can be emitted on stdout.
// On first use it enables ENABLE_VIRTUAL_TERMINAL_PROCESSING on the stdout
// handle when the console does not already have it. A non-console stdout
// (pipes, redirection, the headless daemon's log file) and consoles that
// reject the mode change answer false — callers then degrade to plain text
// instead of spraying escape sequences.
func WindowsVTReady() bool {
	vtOnce.Do(func() {
		h := windows.Handle(os.Stdout.Fd())
		var mode uint32
		if err := windows.GetConsoleMode(h, &mode); err != nil {
			vtOK = false
			return
		}
		if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
			vtOK = true
			return
		}
		vtOK = windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
	})
	return vtOK
}

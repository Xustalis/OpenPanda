//go:build !windows

package cliui

// WindowsVTReady is the non-Windows half of the VT gate: every ANSI-capable
// terminal on unix already speaks VT (the palette's other checks — tty,
// TERM, NO_COLOR — carry the real decision), so the gate is always open and
// the Windows console-mode logic stays confined to vt_windows.go.
func WindowsVTReady() bool { return true }

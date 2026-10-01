//go:build !darwin && !linux

package security

import "os/exec"

// platformBackend reports "" — no OS confinement on this platform (Windows's
// Job Object / AppContainer surface is not wired). ModeOff and every other
// mode therefore behave identically, and callers should surface the empty
// backend rather than imply isolation exists.
func platformBackend() string { return "" }

func wrapSubprocess(cmd *exec.Cmd, p Policy) {}

// SPDX-License-Identifier: AGPL-3.0-or-later

package executil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// AugmentProcessPath makes the process PATH look like the user's, not like
// the launcher's. A daemon started by launchd, a systemd user unit, or the
// Windows task scheduler inherits a minimal PATH — /usr/bin:/bin:… on macOS,
// a service-style PATH on Windows — while the agent CLIs (claude, codex,
// opencode, …) live in per-user or package-manager directories that PATH
// never contains. The node then advertises zero agents in its capability
// summary and peers route no work to it: devices are "online" yet a coding
// task finds no executor. Called once at daemon startup, before any adapter
// or agent lookup, so every LookPath downstream (dispatchability filters,
// pyexec, adapter probes) sees the same PATH an interactive shell would.
//
// Entries are appended only when the directory exists and is not already on
// PATH, and the process environment is rewritten in place (children inherit
// it). Safe to call more than once.
func AugmentProcessPath() {
	current := filepath.SplitList(os.Getenv("PATH"))
	have := make(map[string]bool, len(current))
	for _, p := range current {
		have[filepath.Clean(p)] = true
	}
	var added []string
	for _, dir := range append(userBinDirs(), platformPathDirs()...) {
		if dir == "" {
			continue
		}
		clean := filepath.Clean(dir)
		if have[clean] {
			continue
		}
		if st, err := os.Stat(clean); err != nil || !st.IsDir() {
			continue
		}
		have[clean] = true
		added = append(added, clean)
	}
	if len(added) == 0 {
		return
	}
	_ = os.Setenv("PATH", strings.Join(append(current, added...), string(os.PathListSeparator)))
}

// userBinDirs lists the usual per-user binary directories — installers for
// the supported agent CLIs land in one of these on every platform.
func userBinDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	join := func(parts ...string) string { return filepath.Join(append([]string{home}, parts...)...) }
	if runtime.GOOS == "windows" {
		return []string{
			join(".local", "bin"),
			join(".grok", "bin"),
			join(".cargo", "bin"),
			join("go", "bin"),
			join("scoop", "shims"),
		}
	}
	return []string{
		join(".local", "bin"),
		join("bin"),
		join(".grok", "bin"),
		join(".cargo", "bin"),
		join(".npm-global", "bin"),
		join(".bun", "bin"),
		join("go", "bin"),
	}
}

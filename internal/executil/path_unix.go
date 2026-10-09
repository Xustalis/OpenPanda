// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package executil

// platformPathDirs lists package-manager prefixes that live outside the
// user's home: Homebrew (both Apple Silicon and Intel layouts) and the
// classic /usr/local bin, where npm -g and pip --user land on many systems.
func platformPathDirs() []string {
	return []string{
		"/opt/homebrew/bin",
		"/usr/local/bin",
	}
}

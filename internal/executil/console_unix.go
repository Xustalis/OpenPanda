// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package executil

// normalizeConsoleText passes captured bytes through unchanged on Unix: child
// output here is UTF-8 by convention, and a command that emits something else
// is that command's own business, not the kernel's to reinterpret.
func normalizeConsoleText(b []byte) string { return string(b) }

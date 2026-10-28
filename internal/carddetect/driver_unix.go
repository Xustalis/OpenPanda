// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package carddetect

import (
	"os"
	"path/filepath"
)

// driverFileNames lists the file names a driver may live under on unix, in
// preference order: the bare script the release ships (drivers/panda-*), which
// the kernel executes through its shebang.
func driverFileNames(name string) []string { return []string{name} }

// driverInvocation maps a resolved driver file to the argv a card must carry:
// on unix the driver runs directly, so the command is the file itself and
// there is no argv prefix. The bool reports whether this host can run it.
func driverInvocation(path string) (string, []string, bool) {
	return path, nil, true
}

// executableFile reports whether path names an existing non-directory file
// with any execute bit set — the kernel's own requirement for exec'ing it.
func executableFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

// binaryFileNames lists the file names an installed agent CLI may live under
// in a fallback directory: unix names the executable exactly as the registry
// spells it.
func binaryFileNames(name string) []string { return []string{name} }

// fallbackBinDirs lists the per-user and package-manager directories the
// agent-CLI installers land in on this platform, outside PATH: Homebrew
// (both Apple Silicon and Intel layouts), /usr/local, and the per-user bin
// dirs npm/pip/cargo use.
//
// The auto-start service ships the same directories as its PATH
// (scripts/install.sh's service_path, and deploy/macos/com.openpanda.node.plist)
// because launchd and systemd both hand a job a bare system PATH. The two lists
// answer two halves of one question — "is it installed" here, "can the daemon
// exec it" there — and a mismatch shows up as a card that admits a binary the
// daemon then cannot spawn. Keep them in step.
func fallbackBinDirs(home string) []string {
	return []string{
		filepath.Join(home, ".local", "bin"),
		"/opt/homebrew/bin",
		"/usr/local/bin",
		filepath.Join(home, ".grok", "bin"),
		filepath.Join(home, ".cargo", "bin"),
	}
}

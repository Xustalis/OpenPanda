// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package carddetect

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestServicePathCoversFallbackBinDirs keeps the two halves of "is this binary
// usable" agreeing with each other.
//
// fallbackBinDirs answers it for a process that already has a PATH: it is how
// InstalledBinary finds a CLI a package manager installed somewhere the shell
// never exported. The auto-start service answers the same question from the
// other side — launchd and systemd both hand a job a bare system PATH, so the
// unit file has to spell the directories out. When the two lists disagree, the
// card admits a binary the daemon then cannot spawn (or, as happened on a
// macOS node, the daemon prunes a runtime the host really has and the router
// silently drops down its agent chain).
//
// The service's PATH is written twice because scripts/install.sh ships as a
// single file (`curl … | sh`) and cannot read the deploy/ template at run time.
// This test is what holds the two copies to the same list.
func TestServicePathCoversFallbackBinDirs(t *testing.T) {
	want := fallbackBinDirs("/HOME")

	for _, tc := range []struct {
		name string
		path string
		file string
		re   *regexp.Regexp
	}{
		{
			name: "scripts/install.sh",
			file: filepath.Join("..", "..", "scripts", "install.sh"),
			// service_path() { printf '%s' "$HOME/.local/bin:…" } — anchored on
			// the literal $HOME so it cannot match the script's other
			// printf '%s' call sites (which pass positional arguments).
			re: regexp.MustCompile(`printf '%s' "(\$HOME/[^"]+)"`),
		},
		{
			name: "deploy/macos/com.openpanda.node.plist",
			file: filepath.Join("..", "..", "deploy", "macos", "com.openpanda.node.plist"),
			re:   regexp.MustCompile(`(?s)<key>PATH</key>\s*<string>([^<]+)</string>`),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := servicePathFrom(t, tc.file, tc.re)

			// The template cannot interpolate, so it ships a literal /Users/you
			// for the operator to replace; install.sh interpolates $HOME. Fold
			// both onto one placeholder before comparing against the dirs.
			dirs := map[string]bool{}
			for _, d := range strings.Split(got, ":") {
				d = strings.Replace(d, "$HOME", "/HOME", 1)
				d = strings.Replace(d, "/Users/you", "/HOME", 1)
				dirs[d] = true
			}

			for _, d := range want {
				if !dirs[d] {
					t.Errorf("service PATH is missing %q — a binary InstalledBinary "+
						"finds in that directory would be dropped from the card at "+
						"daemon start; got %q", d, got)
				}
			}

			// A missing system directory breaks the other half: the daemon then
			// loses sh, uname, ping and everything else the shipped cards declare.
			for _, sys := range []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"} {
				if !dirs[sys] {
					t.Errorf("service PATH is missing the system directory %q; got %q", sys, got)
				}
			}
		})
	}
}

// servicePathFrom returns the single PATH-shaped string the file declares. It
// fails rather than skips when the file or the pattern is gone: a rename here
// means the guard stopped guarding, which is the failure this test exists for.
func servicePathFrom(t *testing.T, file string, re *regexp.Regexp) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	m := re.FindStringSubmatch(string(b))
	if m == nil {
		t.Fatalf("%s declares no service PATH matching %s — if the file was "+
			"restructured, update this guard instead of deleting it", file, re)
	}
	return m[1]
}

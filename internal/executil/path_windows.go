// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package executil

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// platformPathDirs lists system-install locations plus the user's own
// registry PATH (HKCU\Environment). The registry read is the important one:
// Windows installers (winget, npm -g, vendor CLIs) append to the USER PATH,
// and a service-style launch environment — the task scheduler included —
// does not necessarily materialize it. Reading it directly makes the
// daemon's PATH match what the user sees in a fresh terminal.
func platformPathDirs() []string {
	var out []string
	if appdata := os.Getenv("APPDATA"); appdata != "" {
		out = append(out, filepath.Join(appdata, "npm"))
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		out = append(out, filepath.Join(local, "Microsoft", "WinGet", "Links"))
	}
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		out = append(out, filepath.Join(pf, "nodejs"))
	}
	out = append(out, registryUserPath()...)
	return out
}

// registryUserPath reads HKCU\Environment's PATH. Best-effort: any error
// (missing key, policy-locked hive) just means no extra directories.
func registryUserPath() []string {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	v, _, err := k.GetStringValue("PATH")
	if err != nil || v == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ";") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

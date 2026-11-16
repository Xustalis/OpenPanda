// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build darwin

package config

import (
	"context"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/executil"
)

// platformMachineID reads IOPlatformUUID — the Mac's hardware UUID. macOS has
// no machine-id file on a stock install, so without this the identity would
// fall back to the hostname and renaming the Mac would look like a new node.
func platformMachineID() string {
	out := probeIdentity("ioreg", "-rd1", "-c", "IOPlatformExpertDevice")
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "IOPlatformUUID") {
			continue
		}
		if _, v, ok := strings.Cut(line, "="); ok {
			return strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return ""
}

// probeIdentity runs a short identity probe. executil keeps the console window
// hidden on Windows — the daemon is headless there and must not flash a
// terminal at startup.
func probeIdentity(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := executil.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

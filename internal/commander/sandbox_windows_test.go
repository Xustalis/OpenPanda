// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package commander

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/security"
)

func TestWindowsSandboxGenericDefaultPolicy(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	before := security.BasePolicy()
	t.Cleanup(func() { security.SetBasePolicy(before) })
	for _, mode := range []string{"standard", "strict"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
			t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
			SetSandboxConfig(config.SandboxConfig{Mode: mode}, nil, nil)
			work := t.TempDir()
			marker := filepath.Join(work, "started")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			ctx = WithAgentCommand(ctx, fmt.Sprintf("%q -test.run=^TestSandboxUnavailableChild$ {stdin}", binary))
			result := runGenericNative(ctx, "sandbox-fixture", "private input", work, []string{sandboxTestMarker + "=" + marker})
			if !result.OK || result.ExitCode != 0 {
				t.Fatalf("default %s generic policy cannot start: %+v", mode, result)
			}
			if data, err := os.ReadFile(marker); err != nil || string(data) != "started" {
				t.Fatalf("generic command did not execute: %q %v", data, err)
			}
		})
	}
}

// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/security"
	"gopkg.in/yaml.v3"
)

// TestSandboxCommandRoundTrip pins /sandbox as a real write path: the file, the
// in-memory config and the process-wide policy move together, "off" is
// reachable again, and a typo is refused rather than folded to off.
func TestSandboxCommandRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Storage.WorkPath = dir
	cfgPath := filepath.Join(dir, "config.yaml")
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	// SetBasePolicy is process-wide; leave the suite running what it started with.
	before := security.BasePolicy()
	defer security.SetBasePolicy(before)

	r := &repl{loc: i18n.English, cfg: cfg, configPath: cfgPath}

	// Bare /sandbox reports: the mode, the platform's backend, and whether the
	// two meet. A report that omits the backend cannot answer "is this real?".
	out, err := r.sandboxCommand("")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	for _, want := range []string{"off", "backend:", "active:", dir} {
		if !strings.Contains(out, want) {
			t.Fatalf("report missing %q:\n%s", want, out)
		}
	}

	// A set writes the file, moves the live config, and echoes the result.
	out, err = r.sandboxCommand(" STRICT ")
	if err != nil {
		t.Fatalf("set strict: %v", err)
	}
	if cfg.Sandbox.NormalizedMode() != config.SandboxModeStrict {
		t.Fatalf("live config = %q, want strict", cfg.Sandbox.Mode)
	}
	if !strings.Contains(out, config.SandboxModeStrict) {
		t.Fatalf("output does not mention the new mode:\n%s", out)
	}
	onDisk, err := os.ReadFile(cfgPath)
	if err != nil || !strings.Contains(string(onDisk), config.SandboxModeStrict) {
		t.Fatalf("config file not updated: %q err %v", onDisk, err)
	}
	// The policy half: strict must be what subprocess launches now start from.
	if got := security.BasePolicy().Mode; got != security.ModeStrict {
		t.Fatalf("base policy mode = %q, want strict", got)
	}

	// A misspelling is an error, not a silent "off".
	if _, err := r.sandboxCommand("standrad"); err == nil {
		t.Fatal("typo accepted as a mode")
	}

	// off is a value, and it has to undo the policy too — otherwise the node
	// keeps confining subprocesses after the operator turned it off.
	if _, err := r.sandboxCommand("off"); err != nil {
		t.Fatalf("set off: %v", err)
	}
	if cfg.Sandbox.NormalizedMode() != config.SandboxModeOff {
		t.Fatalf("live config = %q, want off", cfg.Sandbox.Mode)
	}
	if got := security.BasePolicy().Mode; got != security.ModeOff {
		t.Fatalf("base policy mode = %q after off", got)
	}
}

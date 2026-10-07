// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/security"
)

// TestEngineAppliesConfiguredSandbox pins the fix for the embedded-engine
// gap: sandbox.mode used to reach only the daemon's core, so `panda repl` /
// `panda ask` / the panel sidecar ran the same tasks with ModeOff while the
// settings surface reported confinement. Scheduler init must install the
// configured mode plus the node's protected state paths as the base policy.
func TestEngineAppliesConfiguredSandbox(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{}
	cfg.Storage.DBPath = filepath.Join(root, "data", "panda.db")
	cfg.Storage.MemoryPath = filepath.Join(root, "memory")
	cfg.Storage.ProjectsPath = filepath.Join(root, "projects")
	cfg.Storage.WorkPath = root
	cfg.Node.Name = "sandboxed"
	cfg.Node.Kind = "vm"
	cfg.Sandbox.Mode = "strict"
	cardPath := filepath.Join(root, "capabilities.yaml")
	configPath := filepath.Join(root, "config.yaml")
	t.Cleanup(func() { security.SetBasePolicy(security.Policy{Mode: security.ModeOff, AllowNetwork: true}) })

	e, err := New(context.Background(), cfg, Options{CardPath: cardPath, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer e.Close()
	if e.sched.Load() == nil {
		t.Skip("no capability card available in this environment")
	}

	base := security.BasePolicy()
	if base.Mode != security.ModeStrict {
		t.Fatalf("base policy mode = %v, want strict — sandbox config did not reach the engine", base.Mode)
	}
	var dbDenied, cfgDenied, cardDenied bool
	for _, p := range base.DenyWritePaths {
		if p == cfg.Storage.DBPath {
			dbDenied = true
		}
		if p == configPath {
			cfgDenied = true
		}
		if p == cardPath {
			cardDenied = true
		}
	}
	if !dbDenied || !cfgDenied || !cardDenied {
		t.Fatalf("protected node state not write-denied (db=%v cfg=%v card=%v): %v",
			dbDenied, cfgDenied, cardDenied, base.DenyWritePaths)
	}
}

// TestEngineResetsSandboxOnReinit pins the other half of the wiring: init
// runs SetSandboxConfig unconditionally, so a config that turns the mode off
// resets the process-wide base policy instead of leaving the previous mode
// armed.
func TestEngineResetsSandboxOnReinit(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{}
	cfg.Storage.DBPath = filepath.Join(root, "data", "panda.db")
	cfg.Storage.MemoryPath = filepath.Join(root, "memory")
	cfg.Storage.ProjectsPath = filepath.Join(root, "projects")
	cfg.Storage.WorkPath = root
	cfg.Node.Name = "sandboxed"
	cfg.Node.Kind = "vm"
	cfg.Sandbox.Mode = "standard"
	cardPath := filepath.Join(root, "capabilities.yaml")
	t.Cleanup(func() { security.SetBasePolicy(security.Policy{Mode: security.ModeOff, AllowNetwork: true}) })

	e, err := New(context.Background(), cfg, Options{CardPath: cardPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer e.Close()
	if e.sched.Load() == nil {
		t.Skip("no capability card available in this environment")
	}
	if security.BasePolicy().Mode != security.ModeStandard {
		t.Fatalf("base policy mode = %v, want standard", security.BasePolicy().Mode)
	}

	e.schedMu.Lock()
	cfg.Sandbox.Mode = "off"
	if err := e.initSchedulerLocked(cardPath); err != nil {
		e.schedMu.Unlock()
		t.Fatalf("reinit with mode off: %v", err)
	}
	e.schedMu.Unlock()
	if security.BasePolicy().Mode != security.ModeOff {
		t.Fatalf("turning sandbox off left base policy at %v", security.BasePolicy().Mode)
	}
}

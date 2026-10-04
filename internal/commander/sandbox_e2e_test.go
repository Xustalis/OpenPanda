package commander

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/security"
)

// End-to-end: the native executor under the configured OS sandbox. Skipped on
// platforms without a backend so the suite stays green where the mode
// degrades to env filtering.
func TestNativeExecutorSandboxE2E(t *testing.T) {
	protected := t.TempDir()
	work := t.TempDir()
	backend := SetSandboxConfig(config.SandboxConfig{
		Mode:           "strict",
		DenyWritePaths: []string{protected},
	}, nil)
	t.Cleanup(func() { security.SetBasePolicy(security.Policy{Mode: security.ModeOff, AllowNetwork: true}) })
	if backend == "" {
		t.Skip("no sandbox backend on this platform")
	}

	e := NewExecutor().WithDir(work)
	home, _ := os.UserHomeDir()

	r := e.Run(context.Background(), "/bin/sh", "-c", "echo ok > \"$PWD/f.txt\" && echo wrote")
	if !strings.Contains(r.Stdout, "wrote") {
		t.Fatalf("workdir write failed under sandbox: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(work, "f.txt")); err != nil {
		t.Fatalf("workdir file missing: %v", err)
	}

	r = e.Run(context.Background(), "/bin/sh", "-c",
		"echo x > \""+home+"/panda-e2e-escape\" && echo ESCAPED || echo denied")
	if strings.Contains(r.Stdout, "ESCAPED") {
		os.Remove(filepath.Join(home, "panda-e2e-escape"))
		t.Fatalf("home write escaped the sandbox: %+v", r)
	}

	r = e.Run(context.Background(), "/bin/sh", "-c",
		"echo x > \""+protected+"/x\" && echo WROTE || echo denied")
	if strings.Contains(r.Stdout, "WROTE") {
		t.Fatalf("protected dir writable under sandbox: %+v", r)
	}

	// Strict denies reads of the platform's credential-shaped paths. Plant a
	// decoy under a secret-shaped name and confirm the sandbox refuses it.
	sshDir := filepath.Join(home, ".ssh")
	if _, err := os.Stat(sshDir); os.IsNotExist(err) {
		if err := os.MkdirAll(sshDir, 0o700); err == nil {
			defer os.RemoveAll(sshDir)
		}
	}
	if _, err := os.Stat(sshDir); err == nil {
		decoy := filepath.Join(sshDir, "panda-e2e-decoy")
		if err := os.WriteFile(decoy, []byte("x"), 0o600); err == nil {
			defer os.Remove(decoy)
			r = e.Run(context.Background(), "/bin/sh", "-c",
				"cat \""+decoy+"\" 2>/dev/null && echo READ || echo denied")
			if strings.Contains(r.Stdout, "READ") {
				t.Fatalf("strict mode did not deny a .ssh read: %+v", r)
			}
		}
	}
}

// adapterSandboxPolicy shape: the selected agent's credential dirs become
// writable, foreign agents' become read-denied under strict.
func TestAdapterSandboxPolicy(t *testing.T) {
	SetSandboxConfig(config.SandboxConfig{Mode: "strict"}, nil)
	t.Cleanup(func() { security.SetBasePolicy(security.Policy{Mode: security.ModeOff, AllowNetwork: true}) })

	p := adapterSandboxPolicy("codex.py", "/tmp/work")
	var found bool
	for _, w := range p.WritablePaths {
		if w == ".codex" {
			found = true
		}
	}
	if !found {
		t.Fatalf("codex's credential dir not writable: %v", p.WritablePaths)
	}
	for _, d := range p.DenyReadPaths {
		if d == ".codex" {
			t.Fatalf("own credential dir denied: %v", p.DenyReadPaths)
		}
	}
	// A different agent's credential dir must be denied for codex.
	var foreign bool
	for _, d := range p.DenyReadPaths {
		if d == ".claude" || d == ".grok" {
			foreign = true
		}
	}
	if !foreign {
		t.Fatalf("no foreign credential dir denied: %v", p.DenyReadPaths)
	}

	// Standard mode: nothing read-denied beyond the base policy.
	SetSandboxConfig(config.SandboxConfig{Mode: "standard"}, nil)
	p = adapterSandboxPolicy("codex.py", "/tmp/work")
	if len(p.DenyReadPaths) != 0 {
		t.Fatalf("standard mode must not deny reads: %v", p.DenyReadPaths)
	}
}

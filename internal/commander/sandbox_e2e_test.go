// SPDX-License-Identifier: AGPL-3.0-or-later

package commander

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/security"
)

// End-to-end: the native executor under the configured OS sandbox. Skipped on
// platforms without a backend, where separate launch-refusal tests verify
// that standard and strict do not start an unconfined task.
func TestNativeExecutorSandboxE2E(t *testing.T) {
	protected := t.TempDir()
	work := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	secret := filepath.Join(home, ".ssh", "sandbox-decoy")
	if err := os.MkdirAll(filepath.Dir(secret), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("decoy"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := SetSandboxConfig(config.SandboxConfig{
		Mode:           "strict",
		DenyWritePaths: []string{protected},
	}, nil, nil)
	t.Cleanup(func() { security.SetBasePolicy(security.Policy{Mode: security.ModeOff, AllowNetwork: true}) })
	if backend == "" {
		t.Skip("no sandbox backend on this platform")
	}

	e := NewExecutor().WithDir(work)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	run := func(action, path string) NativeResult {
		return e.Run(context.Background(), binary, "-test.run=^TestNativeSandboxPayload$", "--", "panda-native-sandbox", action, path)
	}
	// A backend can be present and still be unusable: a process that is
	// already inside a seatbelt cannot nest a stricter one, so sandbox-exec
	// refuses to apply the profile and every run below fails with
	// "sandbox_apply: Operation not permitted". That is the harness answering,
	// not a confinement regression — probe once and skip rather than report
	// three escapes that never happened.
	if probe := run("probe", ""); strings.Contains(probe.Stderr, "sandbox_apply") {
		t.Skipf("sandbox backend %q cannot apply a profile here: %s", backend, strings.TrimSpace(probe.Stderr))
	}

	r := run("write", filepath.Join(work, "f.txt"))
	if !strings.Contains(r.Stdout, "wrote") {
		t.Fatalf("workdir write failed under sandbox: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(work, "f.txt")); err != nil {
		t.Fatalf("workdir file missing: %v", err)
	}

	r = run("write", filepath.Join(home, "sandbox-escape"))
	if !r.OK || !strings.Contains(r.Stdout, "denied") {
		t.Fatalf("home write escaped the sandbox: %+v", r)
	}

	r = run("write", filepath.Join(protected, "x"))
	if !r.OK || !strings.Contains(r.Stdout, "denied") {
		t.Fatalf("protected dir writable under sandbox: %+v", r)
	}

	// Credential-shaped paths live under a private test home. Never plant a
	// decoy in the operator's actual .ssh directory to exercise confinement.
	r = run("read", secret)
	if !r.OK || !strings.Contains(r.Stdout, "denied") {
		t.Fatalf("strict mode did not deny a .ssh read: %+v", r)
	}
}

func TestNativeSandboxPayload(t *testing.T) {
	i := slices.Index(os.Args, "--")
	if i < 0 || len(os.Args) != i+4 || os.Args[i+1] != "panda-native-sandbox" {
		return
	}
	action, target := os.Args[i+2], os.Args[i+3]
	var err error
	switch action {
	case "probe":
		fmt.Println("ready")
	case "write":
		err = os.WriteFile(target, []byte("ok"), 0o600)
		if err == nil {
			fmt.Println("wrote")
		}
	case "read":
		_, err = os.ReadFile(target)
		if err == nil {
			fmt.Println("read")
		}
	default:
		t.Fatalf("unknown sandbox fixture action %q", action)
	}
	if err != nil {
		fmt.Println("denied")
	}
	os.Exit(0)
}

// adapterSandboxPolicy shape: the selected agent's credential dirs become
// writable, foreign agents' become read-denied under strict.
func TestAdapterSandboxPolicy(t *testing.T) {
	SetSandboxConfig(config.SandboxConfig{Mode: "strict"}, nil, nil)
	t.Cleanup(func() { security.SetBasePolicy(security.Policy{Mode: security.ModeOff, AllowNetwork: true}) })

	p := adapterSandboxPolicy("codex", "codex.py", "/tmp/work")
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
	SetSandboxConfig(config.SandboxConfig{Mode: "standard"}, nil, nil)
	p = adapterSandboxPolicy("codex", "codex.py", "/tmp/work")
	if len(p.DenyReadPaths) != 0 {
		t.Fatalf("standard mode must not deny reads: %v", p.DenyReadPaths)
	}
}

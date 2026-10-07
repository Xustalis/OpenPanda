// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build darwin

package security

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests drive the real sandbox-exec backend end to end — they are
// skipped when the binary is absent, so the suite stays green on a stripped
// or future macOS where Apple finally removes it.

func seatbeltOrSkip(t *testing.T) string {
	t.Helper()
	sb, err := exec.LookPath("sandbox-exec")
	if err != nil {
		t.Skip("sandbox-exec not installed")
	}
	return sb
}

// runSandboxed launches a shell -c under the rendered profile and returns its
// combined output. The policy is standard/strict confinement over a temp
// workdir, exactly what the commander computes for a task directory.
func runSandboxed(t *testing.T, p Policy, script string) (string, error) {
	t.Helper()
	seatbeltOrSkip(t)
	if p.WorkDir == "" {
		p.WorkDir = t.TempDir()
	}
	if p.Mode == "" {
		p.Mode = ModeStandard
	}
	cmd := exec.Command("/bin/sh", "-c", script)
	NewSandbox(p.WorkDir).ApplyPolicy(cmd, p)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestSeatbeltWriteConfinement(t *testing.T) {
	work := t.TempDir()
	out, err := runSandboxed(t, Policy{WorkDir: work}, `
echo inside > "$PWD/ok.txt" && echo wrote-workdir
echo escape > "$HOME/panda-escape-test" 2>/dev/null || echo home-write-denied
`)
	if err != nil {
		t.Fatalf("sandboxed run failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "wrote-workdir") {
		t.Fatalf("workdir write did not succeed: %s", out)
	}
	if !strings.Contains(out, "home-write-denied") {
		t.Fatalf("home write was not denied: %s", out)
	}
	if _, err := os.Stat(filepath.Join(work, "ok.txt")); err != nil {
		t.Fatalf("workdir file missing after sandboxed write: %v", err)
	}
}

func TestSeatbeltDenyRead(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	secret := filepath.Join(home, ".panda-test-secret")
	if err := os.WriteFile(secret, []byte("hunter2"), 0o600); err != nil {
		t.Skipf("cannot plant secret file: %v", err)
	}
	defer os.Remove(secret)

	out, _ := runSandboxed(t, Policy{
		Mode:          ModeStrict,
		DenyReadPaths: []string{secret},
	}, `cat "`+secret+`" 2>/dev/null || echo read-denied`)
	if !strings.Contains(out, "read-denied") {
		t.Fatalf("strict mode did not deny the read: %s", out)
	}

	// Standard mode reads everything — the same file must be readable there.
	out, err = runSandboxed(t, Policy{Mode: ModeStandard}, `cat "`+secret+`"`)
	if err != nil || !strings.Contains(out, "hunter2") {
		t.Fatalf("standard mode should still read: %v %s", err, out)
	}
}

func TestSeatbeltDenyWrite(t *testing.T) {
	protected := t.TempDir()
	out, _ := runSandboxed(t, Policy{
		Mode:           ModeStandard,
		DenyWritePaths: []string{protected},
	}, `echo x > "`+protected+`/x" 2>/dev/null || echo write-denied`)
	if !strings.Contains(out, "write-denied") {
		t.Fatalf("deny_write_paths entry still writable: %s", out)
	}
}

func TestSeatbeltWritableWhitelist(t *testing.T) {
	extra := t.TempDir()
	out, err := runSandboxed(t, Policy{WritablePaths: []string{extra}},
		`echo x > "`+extra+`/x" && echo wrote-extra`)
	if err != nil || !strings.Contains(out, "wrote-extra") {
		t.Fatalf("whitelisted path not writable: %v %s", err, out)
	}
}

func TestSeatbeltNetwork(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 needed to read the socket errno")
	}
	// connect() to a closed port reports "Connection refused" when the socket
	// syscall ran, and EPERM when the sandbox denied it — the errno text is
	// the signal, not the connection result.
	probe := `python3 -c 'import socket; s=socket.socket(); s.settimeout(2)
try: s.connect(("127.0.0.1", 9))
except OSError as e: print("errno", e.errno)'`
	out, err := runSandboxed(t, Policy{AllowNetwork: true}, probe)
	if err != nil || !strings.Contains(out, "errno") || strings.Contains(out, "errno 1") {
		t.Fatalf("allow_network=true should reach TCP (EPERM=1 means denied): %v %s", err, out)
	}
	out, _ = runSandboxed(t, Policy{AllowNetwork: false}, probe)
	if !strings.Contains(out, "errno 1") {
		t.Fatalf("allow_network=false did not deny the socket: %s", out)
	}
}

func TestSeatbeltProcessLifecycle(t *testing.T) {
	out, err := runSandboxed(t, Policy{}, `
sleep 60 & PID=$!
kill -9 $PID && echo kill-ok
python3 -c 'print("py-ok")' 2>/dev/null || true
`)
	if err != nil {
		t.Fatalf("sandboxed run failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "kill-ok") {
		t.Fatalf("could not kill a child inside the sandbox: %s", out)
	}
}

package security

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// The allow-list has to match the platform the adapter actually runs on. A
// Windows python started with only the POSIX names set cannot find its own
// install root (SYSTEMROOT) or user site-packages (APPDATA), which surfaces as
// "the compute node cannot launch agents" rather than as a missing variable.
func TestSandboxEnvCarriesThisPlatformsEssentials(t *testing.T) {
	want := []string{"PATH"}
	if runtime.GOOS == "windows" {
		want = append(want, "SYSTEMROOT", "PATHEXT", "TEMP", "APPDATA", "USERPROFILE")
	} else {
		want = append(want, "HOME")
	}
	for _, k := range want {
		t.Setenv(k, "x")
	}
	env := NewSandbox("").Env()
	for _, k := range want {
		found := false
		for _, kv := range env {
			if strings.EqualFold(kv, k+"=x") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s missing from the %s sandbox env: %v", k, runtime.GOOS, env)
		}
	}
}

// An unset variable must be omitted, not forwarded as empty: HOME="" reads as a
// configured-but-broken home to several CLIs, which then fail instead of
// falling back to the OS default.
func TestSandboxEnvOmitsUnsetVariables(t *testing.T) {
	for _, kv := range NewSandbox("").Env("INJECTED=v") {
		if strings.HasSuffix(kv, "=") {
			t.Errorf("empty variable forwarded to the child: %q", kv)
		}
	}
}

func TestSandboxEnvDoesNotLeakSecrets(t *testing.T) {
	// Simulate unrelated secrets in the parent process env; the sandbox env
	// allowlist must not forward them to the (potentially remote) subprocess.
	t.Setenv("OPENPANDA_TEST_SECRET", "hunter2")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "topsecret")

	env := NewSandbox("").Env("ANTHROPIC_API_KEY=k")
	for _, kv := range env {
		if strings.HasPrefix(kv, "OPENPANDA_TEST_SECRET=") || strings.HasPrefix(kv, "AWS_SECRET_ACCESS_KEY=") {
			t.Fatalf("sandbox env leaked a parent secret: %q", kv)
		}
	}
	// The explicitly injected credential is present.
	found := false
	for _, kv := range env {
		if kv == "ANTHROPIC_API_KEY=k" {
			found = true
		}
	}
	if !found {
		t.Fatalf("injected credential missing from sandbox env: %v", env)
	}
}

func TestSandboxApplySetsDirAndEnv(t *testing.T) {
	cmd := exec.Command("true")
	NewSandbox("/tmp/taskdir").Apply(cmd, "FOO=bar")

	if cmd.Dir != "/tmp/taskdir" {
		t.Fatalf("cmd.Dir = %q, want /tmp/taskdir", cmd.Dir)
	}
	hasFoo := false
	hasPath := false
	for _, kv := range cmd.Env {
		if kv == "FOO=bar" {
			hasFoo = true
		}
		if strings.HasPrefix(kv, "PATH=") {
			hasPath = true
		}
	}
	if !hasFoo {
		t.Fatalf("extra env not applied: %v", cmd.Env)
	}
	if !hasPath {
		t.Fatalf("PATH missing from sandbox env: %v", cmd.Env)
	}
}

package security

import (
	"os/exec"
	"strings"
	"testing"
)

func TestSandboxEnvDoesNotLeakSecrets(t *testing.T) {
	// Simulate unrelated secrets in the parent process env; the sandbox env
	// allowlist must not forward them to the (potentially remote) subprocess.
	t.Setenv("PANDA_TEST_SECRET", "hunter2")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "topsecret")

	env := NewSandbox("").Env("ANTHROPIC_API_KEY=k")
	for _, kv := range env {
		if strings.HasPrefix(kv, "PANDA_TEST_SECRET=") || strings.HasPrefix(kv, "AWS_SECRET_ACCESS_KEY=") {
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

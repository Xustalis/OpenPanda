package commander

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNativeExecutorCwdIsolation verifies a native command runs with its working
// directory set to the executor's dir (P1-1), by placing a sentinel file there
// and asserting the command can see it from its cwd.
func TestNativeExecutorCwdIsolation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sentinel"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := NewExecutor().WithDir(dir).Run(context.Background(), "sh", "-c", "test -f sentinel && echo INSIDE")
	if !res.OK {
		t.Fatalf("cwd check failed: %s", res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) != "INSIDE" {
		t.Fatalf("command did not run inside workDir; stdout=%q", res.Stdout)
	}
}

// TestNativeExecutorEnvExcludesSecrets verifies the native subprocess does not
// inherit host secrets (P1-1): the parent's full environment is not forwarded,
// so a leaked model key can never reach a native command.
func TestNativeExecutorEnvExcludesSecrets(t *testing.T) {
	t.Setenv("OPENPANDA_MODEL_API_KEY", "sk-panda-secret")
	t.Setenv("ANTHROPIC_API_KEY", "sk-anthropic-secret")

	res := NewExecutor().WithDir(t.TempDir()).Run(context.Background(), "env")
	if !res.OK {
		t.Fatalf("env failed: %s", res.Stderr)
	}
	for _, k := range []string{"OPENPANDA_MODEL_API_KEY", "ANTHROPIC_API_KEY"} {
		if strings.Contains(res.Stdout, k) {
			t.Fatalf("native env leaked %s", k)
		}
	}
}

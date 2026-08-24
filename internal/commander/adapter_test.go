package commander

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// fakeAdapter writes a known JSON result to stdout, so we can test the
// process bridge without invoking a real LLM CLI.
const fakeAdapter = `#!/usr/bin/env python3
import json, sys
req = json.loads(sys.stdin.read())
print(json.dumps({"ok": True, "result": "hello from " + req.get("prompt", ""), "exit_code": 0}))
`

func TestRunAdapterProcess(t *testing.T) {
	dir := t.TempDir()
	adapterPath := filepath.Join(dir, "fake.py")
	if err := os.WriteFile(adapterPath, []byte(fakeAdapter), 0o755); err != nil {
		t.Fatalf("write adapter: %v", err)
	}
	oldDir := adapterDir
	adapterDir = dir // redirect the constant for this test
	defer func() { adapterDir = oldDir }()

	res := runAdapterProcess(context.Background(), "fake.py", "PANDA", "", nil)
	if !res.OK {
		t.Fatalf("adapter failed: %+v", res)
	}
	if res.Result != "hello from PANDA" {
		t.Fatalf("unexpected result: %q", res.Result)
	}
}

func TestRunAdapterMissingBinary(t *testing.T) {
	oldDir := adapterDir
	adapterDir = t.TempDir() // no adapters here
	defer func() { adapterDir = oldDir }()

	res := runAdapterProcess(context.Background(), "nope.py", "x", "", nil)
	if res.OK {
		t.Fatalf("expected failure for missing adapter")
	}
}

func TestModelEnvInjectsProvider(t *testing.T) {
	env := modelEnv(config.ModelConfig{
		BaseURL: "https://api.deepseek.com/anthropic",
		APIKey:  "sk-test",
		Model:   "deepseek-chat",
	})
	want := map[string]string{
		"ANTHROPIC_BASE_URL": "https://api.deepseek.com/anthropic",
		"ANTHROPIC_API_KEY":  "sk-test",
		"ANTHROPIC_MODEL":    "deepseek-chat",
	}
	for _, kv := range env {
		for k, v := range want {
			if kv == k+"="+v {
				delete(want, k)
			}
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing env entries: %v", want)
	}
}

func TestModelEnvRejectsUnsupportedProviderMapping(t *testing.T) {
	env := modelEnvForAdapter(config.ModelConfig{
		APIType: config.APITypeOpenAI, BaseURL: "https://api.openai.com/v1", APIKey: "sk-test", Model: "gpt-4o",
	}, "claude_code.py")
	if len(env) != 0 {
		t.Fatalf("openai config must not be mapped to Claude env: %v", env)
	}
}

func TestAdapterCredentialEnvIsWhitelisted(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "anthropic-token")
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "unrelated")
	env := adapterCredentialEnv("codex.py")
	if len(env) != 1 || env[0] != "OPENAI_API_KEY=openai-secret" {
		t.Fatalf("codex credential env = %v, want only OpenAI key", env)
	}
}

func TestMergeAdapterEnvInjectedValueWinsWithoutDuplicates(t *testing.T) {
	got := mergeAdapterEnv(
		[]string{"PATH=/bin", "ANTHROPIC_API_KEY=native", "OPENAI_API_KEY=openai"},
		[]string{"ANTHROPIC_API_KEY=panda", "ANTHROPIC_MODEL=model"},
	)
	want := []string{"PATH=/bin", "ANTHROPIC_API_KEY=panda", "OPENAI_API_KEY=openai", "ANTHROPIC_MODEL=model"}
	if len(got) != len(want) {
		t.Fatalf("merged env = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("merged env[%d] = %q, want %q (all=%v)", i, got[i], want[i], got)
		}
	}
}

// TestAdapterPathRejectsTraversal verifies adapter names are flat filenames
// (P2-5): anything path-like (.., separators, absolute) is a traversal attempt
// and must be rejected before it reaches exec.CommandContext.
func TestAdapterPathRejectsTraversal(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../evil.py", "a/b.py", `a\b.py`, "/etc/passwd"} {
		if _, err := adapterPath(name); err == nil {
			t.Fatalf("adapterPath(%q): expected error", name)
		}
	}
	p, err := adapterPath("claude_code.py")
	if err != nil {
		t.Fatalf("valid adapter name rejected: %v", err)
	}
	if p == "" {
		t.Fatal("empty path for valid adapter name")
	}
}

func TestResolveAdapterDirEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(adapterDirEnv, dir)
	if got := resolveAdapterDir(); got != dir {
		t.Fatalf("resolveAdapterDir = %q, want env override %q", got, dir)
	}
}

// slowAdapter reads the request and then sleeps far past any test budget,
// simulating an adapter that ignores the advertised timeout_s.
const slowAdapter = `#!/usr/bin/env python3
import json, sys, time
req = json.loads(sys.stdin.read())
time.sleep(3600)
print(json.dumps({"ok": True, "result": "done", "exit_code": 0}))
`

// TestAdapterHardTimeout verifies P1-18: an adapter that ignores the timeout
// advertised in its request is killed by the Go-side hard deadline instead of
// running forever.
func TestAdapterHardTimeout(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "slow.py"), []byte(slowAdapter), 0o755); err != nil {
		t.Fatalf("write adapter: %v", err)
	}
	oldDir := adapterDir
	adapterDir = dir
	defer func() { adapterDir = oldDir }()
	oldTimeout := adapterHardTimeout
	adapterHardTimeout = 300 * time.Millisecond
	defer func() { adapterHardTimeout = oldTimeout }()

	start := time.Now()
	res := runAdapterProcess(context.Background(), "slow.py", "x", "", nil)
	elapsed := time.Since(start)

	if res.OK {
		t.Fatalf("sleeping adapter reported success")
	}
	if res.ExitCode != 124 {
		t.Fatalf("exit code = %d, want 124 (hard timeout)", res.ExitCode)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("hard timeout not enforced promptly: %v", elapsed)
	}
}

// TestAdapterCandidateDirsFollowsSymlink verifies a packaged install layout:
// the real binary lives at <prefix>/bin/panda with adapters beside it under
// <prefix>/adapters, and a symlink on PATH (~/.local/bin/panda) resolves back
// to it. The candidate list must include <prefix>/adapters when handed the
// symlink path, or the spawned adapter would die with a missing path.
func TestAdapterCandidateDirsFollowsSymlink(t *testing.T) {
	prefix := t.TempDir()
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(prefix, "bin", "panda")
	if err := os.WriteFile(real, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Normalize prefix the same way EvalSymlinks does — on macOS /var is a
	// symlink to /private/var, so the candidate under the real binary reports
	// /private/... while t.TempDir() handed us /var/... .
	if resolved, err := filepath.EvalSymlinks(prefix); err == nil {
		prefix = resolved
	}
	want := filepath.Join(prefix, "adapters")

	// Symlinks are unsupported on Windows without privileges; skip there.
	link := filepath.Join(t.TempDir(), "panda")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	found := false
	for _, cand := range adapterCandidateDirs(link) {
		if sameAbs(cand, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("adapterCandidateDirs(%q) missing %q (got %v)", link, want, adapterCandidateDirs(link))
	}
}

// sameAbs compares two paths after absolutizing + cleaning, so the test works
// whether the caller hands an absolute or relative link path.
func sameAbs(a, b string) bool {
	aa, err := filepath.Abs(a)
	if err != nil {
		return a == b
	}
	bb, err := filepath.Abs(b)
	if err != nil {
		return a == b
	}
	return filepath.Clean(aa) == filepath.Clean(bb)
}

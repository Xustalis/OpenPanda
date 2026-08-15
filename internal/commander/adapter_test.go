package commander

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xenith/panda/internal/config"
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

	res := runAdapterProcess(context.Background(), "fake.py", "PANDA", "", config.ModelConfig{})
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

	res := runAdapterProcess(context.Background(), "nope.py", "x", "", config.ModelConfig{})
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
	res := runAdapterProcess(context.Background(), "slow.py", "x", "", config.ModelConfig{})
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

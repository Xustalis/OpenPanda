package commander

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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

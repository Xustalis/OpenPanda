package commander

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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

	res := runAdapterProcess(context.Background(), "fake.py", "PANDA", "")
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

	res := runAdapterProcess(context.Background(), "nope.py", "x", "")
	if res.OK {
		t.Fatalf("expected failure for missing adapter")
	}
}

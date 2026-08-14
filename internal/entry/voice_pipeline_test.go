package entry

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeFake writes a fake voice sidecar script that prints a fixed JSON result,
// mirroring the real sidecars' stdout protocol. runSidecar invokes it via
// `python3`, so it needs no execute bit.
func writeFake(t *testing.T, body string) (dir string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fake.py"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunSidecarOK(t *testing.T) {
	dir := writeFake(t, `import json, sys
print(json.dumps({"ok": True, "result": "hello"}))
`)
	old := voiceDir
	voiceDir = dir
	defer func() { voiceDir = old }()

	res := runSidecar(context.Background(), "fake.py", map[string]any{"x": 1})
	if !res.ok || res.result != "hello" {
		t.Fatalf("runSidecar = %+v, want ok result=hello", res)
	}
}

func TestRunSidecarFailure(t *testing.T) {
	dir := writeFake(t, `import json, sys
print(json.dumps({"ok": False, "result": "pvporcupine not installed"}))
`)
	old := voiceDir
	voiceDir = dir
	defer func() { voiceDir = old }()

	res := runSidecar(context.Background(), "fake.py", nil)
	if res.ok || res.result != "pvporcupine not installed" {
		t.Fatalf("runSidecar = %+v, want ok=false with reason", res)
	}
}

func TestRunSidecarNotJSON(t *testing.T) {
	dir := writeFake(t, `print("garbage output")`)
	old := voiceDir
	voiceDir = dir
	defer func() { voiceDir = old }()

	res := runSidecar(context.Background(), "fake.py", nil)
	if res.ok || res.err == "" {
		t.Fatalf("runSidecar = %+v, want ok=false with err", res)
	}
}

package entry

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// TestListenReportsBrokenSidecar separates the two reasons Listen fails. A
// sidecar that ran and reported "No module named 'numpy'" puts that message in
// `result`, not `err`; reading only `err` reported a missing driver as an empty
// error, which a looping caller cannot distinguish from "nobody spoke" — so it
// respawns python forever while the prompt says nothing is wrong.
func TestListenReportsBrokenSidecar(t *testing.T) {
	dir := writeFake(t, "")
	if err := os.WriteFile(filepath.Join(dir, "wake.py"), []byte(`import json
print(json.dumps({"ok": False, "result": "wake failed: No module named 'numpy'"}))
`), 0o644); err != nil {
		t.Fatal(err)
	}
	old := voiceDir
	voiceDir = dir
	defer func() { voiceDir = old }()

	got := Listen(context.Background(), 1)
	if got.OK {
		t.Fatal("a broken wake sidecar reported a successful capture")
	}
	if got.Timeout {
		t.Error("a broken sidecar was reported as a timeout; the caller would loop on it")
	}
	if !strings.Contains(got.Err, "numpy") {
		t.Errorf("Err = %q, want the sidecar's own reason", got.Err)
	}
}

// TestListenTimeout is the other half: the wake word simply never fired. It must
// be marked as a timeout so the caller listens again rather than exiting.
func TestListenTimeout(t *testing.T) {
	dir := writeFake(t, "")
	if err := os.WriteFile(filepath.Join(dir, "wake.py"), []byte(`import json
print(json.dumps({"ok": True, "result": "timeout"}))
`), 0o644); err != nil {
		t.Fatal(err)
	}
	old := voiceDir
	voiceDir = dir
	defer func() { voiceDir = old }()

	got := Listen(context.Background(), 1)
	if got.OK || !got.Timeout {
		t.Fatalf("Listen = %+v, want a timeout", got)
	}
}

// SPDX-License-Identifier: AGPL-3.0-or-later

package commander

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/pyexec"
	"github.com/Xustalis/OpenPanda/internal/security"
)

const sandboxTestMarker = "PANDA_SANDBOX_TEST_MARKER"

// The child test writes evidence that the task command actually started.
func TestSandboxUnavailableChild(t *testing.T) {
	if marker := os.Getenv(sandboxTestMarker); marker != "" {
		if err := os.WriteFile(marker, []byte("started"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSandboxUnavailableBlocksNativeAndGeneric(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	before := security.BasePolicy()
	t.Cleanup(func() { security.SetBasePolicy(before) })
	// Hide optional POSIX backends too, so this regression runs on every OS.
	// The task executable is absolute and does not depend on PATH.
	t.Setenv("PATH", t.TempDir())
	if security.Backend() != "" {
		t.Skip("host has a native sandbox backend that does not depend on PATH; backendMissing is covered in security")
	}
	for _, kind := range []string{"native", "generic"} {
		for _, mode := range []security.Mode{security.ModeOff, security.ModeStandard, security.ModeStrict} {
			t.Run(kind+"/"+string(mode), func(t *testing.T) {
				work := t.TempDir()
				marker := filepath.Join(work, "started")
				env := []string{sandboxTestMarker + "=" + marker}
				security.SetBasePolicy(security.Policy{Mode: mode, AllowNetwork: true})
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				var ok bool
				var code int
				var detail string
				if kind == "native" {
					executor := NewExecutor().WithDir(work)
					executor.env = env
					result := executor.Run(ctx, binary, "-test.run=^TestSandboxUnavailableChild$")
					ok, code, detail = result.OK, result.ExitCode, result.Stderr
				} else {
					ctx = WithAgentCommand(ctx, fmt.Sprintf("%q -test.run=^TestSandboxUnavailableChild$", binary))
					result := runGenericNative(ctx, "test", "", work, env)
					ok, code, detail = result.OK, result.ExitCode, result.Result
				}
				assertSandboxLaunch(t, mode, marker, ok, code, detail)
			})
		}
	}
}

func TestSandboxUnavailableBlocksPythonAdapter(t *testing.T) {
	if security.Backend() != "" {
		t.Skip("requires a host without a sandbox backend")
	}
	if !pyexec.Available() {
		t.Skip("no Python interpreter")
	}
	before := security.BasePolicy()
	t.Cleanup(func() { security.SetBasePolicy(before) })
	previousDir := adapterDir
	adapterDir = t.TempDir()
	t.Cleanup(func() { adapterDir = previousDir })
	const script = `import json, os, sys
json.loads(sys.stdin.read())
with open(os.environ["PANDA_SANDBOX_TEST_MARKER"], "w") as f:
    f.write("started")
print(json.dumps({"ok": True, "result": "started", "exit_code": 0}))
`
	if err := os.WriteFile(filepath.Join(adapterDir, "marker.py"), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []security.Mode{security.ModeOff, security.ModeStandard, security.ModeStrict} {
		t.Run(string(mode), func(t *testing.T) {
			work := t.TempDir()
			marker := filepath.Join(work, "started")
			security.SetBasePolicy(security.Policy{Mode: mode, AllowNetwork: true})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result := runAdapterProcess(ctx, "marker.py", "", work, []string{sandboxTestMarker + "=" + marker})
			assertSandboxLaunch(t, mode, marker, result.OK, result.ExitCode, result.Result)
		})
	}
}

func assertSandboxLaunch(t *testing.T, mode security.Mode, marker string, ok bool, code int, detail string) {
	t.Helper()
	_, err := os.Stat(marker)
	if mode == security.ModeOff {
		if !ok || code != 0 || err != nil {
			t.Fatalf("off must run the test command: ok=%v code=%d marker=%v detail=%s", ok, code, err, detail)
		}
		return
	}
	if ok || code != 127 || !strings.Contains(detail, security.ErrSandboxUnavailable.Error()) {
		t.Fatalf("missing backend must refuse launch: ok=%v code=%d detail=%s", ok, code, detail)
	}
	if !os.IsNotExist(err) {
		t.Fatalf("sandbox refusal started the task command: marker err=%v", err)
	}
}

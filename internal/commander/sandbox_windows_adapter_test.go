// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package commander

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/pyexec"
	"github.com/Xustalis/OpenPanda/internal/security"
)

// Opt in with a disposable, user-owned embedded Python runtime, not a real
// installation: the sandbox temporarily grants read ACLs on the runtime.
// Run this compatibility check in isolation because pyexec caches its interpreter for the
// test process. An unset opt-in keeps normal tests independent of Python.
// Off must run the protocol; isolated modes must run it or report the precise
// DLL-initialization incompatibility without executing adapter code.
func TestWindowsSandboxPythonAdapterCompatibility(t *testing.T) {
	runtimeDir := os.Getenv("OPENPANDA_WINDOWS_PYTHON_SMOKE")
	if runtimeDir == "" {
		t.Skip("set OPENPANDA_WINDOWS_PYTHON_SMOKE to a disposable embedded Python runtime")
	}
	runtimeDir, err := filepath.Abs(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(runtimeDir, "python*._pth"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("smoke requires one isolated python*._pth in the disposable runtime: paths=%v err=%v", paths, err)
	}
	python := filepath.Join(runtimeDir, "python.exe")
	if _, err := os.Stat(python); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", runtimeDir+string(os.PathListSeparator)+filepath.Join(os.Getenv("SystemRoot"), "System32"))
	t.Setenv(pyexec.EnvOverride, python)
	interpreter := pyexec.Interpreter()
	if len(interpreter) != 1 {
		t.Fatalf("expected the isolated Python interpreter, got %v", interpreter)
	}
	resolved, err := exec.LookPath(interpreter[0])
	if err != nil || !strings.EqualFold(filepath.Clean(resolved), python) {
		t.Fatalf("Python resolver selected %q instead of disposable %q: %v; run this smoke separately", resolved, python, err)
	}
	before := security.BasePolicy()
	t.Cleanup(func() { security.SetBasePolicy(before) })
	for _, mode := range []string{"off", "standard", "strict"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
			t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
			adapters := t.TempDir()
			t.Setenv(adapterDirEnv, adapters)
			if err := os.WriteFile(filepath.Join(adapters, "marker.py"), []byte(windowsPythonAdapterSmoke), 0o600); err != nil {
				t.Fatal(err)
			}
			backend := SetSandboxConfig(config.SandboxConfig{Mode: mode}, nil, nil)
			if mode != "off" && backend != "appcontainer" {
				t.Fatalf("real Python smoke requires AppContainer, got %q", backend)
			}
			work := t.TempDir()
			prompt := "python-adapter-" + mode + "-\u4e2d\u6587"
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			result := runAdapterProcess(ctx, "marker.py", prompt, work, nil)
			if mode != "off" && !result.OK && result.ExitCode == 0xc0000142 {
				if !strings.Contains(result.Result, security.ErrSandboxUnavailable.Error()) || !strings.Contains(result.Result, "Isolation remains enabled") {
					t.Fatalf("Python runtime refusal lost its compatibility diagnosis: %+v", result)
				}
				if _, err := os.Stat(filepath.Join(work, "python-marker.txt")); !os.IsNotExist(err) {
					t.Fatalf("incompatible adapter ran despite refusal: %v", err)
				}
				if security.BasePolicy().Mode != security.Mode(mode) {
					t.Fatal("runtime failure changed the requested sandbox policy")
				}
				t.Logf("mode=%s Python DLL initialization is incompatible; original status and explicit diagnosis preserved, adapter marker absent", mode)
				return
			}
			if !result.OK || result.ExitCode != 0 {
				t.Fatalf("real Python adapter failed under %s: %+v", mode, result)
			}
			var got struct {
				Prompt   string            `json:"prompt"`
				CWD      string            `json:"cwd"`
				Request  string            `json:"request_cwd"`
				Python   string            `json:"python"`
				Temp     string            `json:"temp"`
				TempData string            `json:"temp_data"`
				TempEnv  map[string]string `json:"temp_env"`
				Isolated bool              `json:"isolated"`
			}
			if err := json.Unmarshal([]byte(result.Result), &got); err != nil {
				t.Fatalf("Python result is not the expected JSON report: %v: %s", err, result.Result)
			}
			if got.Prompt != prompt || got.TempData != prompt || !got.Isolated {
				t.Fatalf("stdin/stdout/tempfile protocol mismatch: %+v", got)
			}
			if !strings.EqualFold(filepath.Clean(got.CWD), work) || !strings.EqualFold(filepath.Clean(got.Request), work) || !strings.EqualFold(filepath.Clean(got.Python), python) {
				t.Fatalf("adapter did not use its requested cwd/interpreter: %+v", got)
			}
			if data, err := os.ReadFile(filepath.Join(work, "python-marker.txt")); err != nil || string(data) != prompt {
				t.Fatalf("Python cwd marker missing or changed: %q %v", data, err)
			}
			if mode != "off" {
				tempDir := got.TempEnv["TEMP"]
				if tempDir == "" || !strings.EqualFold(tempDir, got.TempEnv["TMP"]) || !strings.EqualFold(tempDir, got.TempEnv["TMPDIR"]) || !strings.EqualFold(filepath.Dir(got.Temp), tempDir) {
					t.Fatalf("Python did not use one redirected private Temp: %+v", got)
				}
				if !strings.Contains(strings.ToLower(tempDir), "openpanda.sandbox.") || strings.EqualFold(filepath.Clean(tempDir), filepath.Clean(os.TempDir())) {
					t.Fatalf("Python temporary file used the host Temp instead of its AppContainer profile: %+v", got)
				}
			}
			t.Logf("mode=%s python=%s cwd=%s private_temp=%s UTF-8 stdin/stdout and tempfile write verified", mode, got.Python, got.CWD, got.Temp)
		})
	}
}

const windowsPythonAdapterSmoke = `import json
import os
from pathlib import Path
import sys
import tempfile

request = json.loads(sys.stdin.buffer.read().decode("utf-8"))
prompt = request["prompt"]
with tempfile.NamedTemporaryFile(mode="w+b", prefix="adapter-smoke-", delete=False) as temporary:
    temporary.write(prompt.encode("utf-8"))
    temporary.flush()
    temp_path = temporary.name
temp_data = Path(temp_path).read_text(encoding="utf-8")
Path(temp_path).unlink()
Path("python-marker.txt").write_text(prompt, encoding="utf-8")
report = dict(prompt=prompt, cwd=os.getcwd(), request_cwd=request["cwd"],
              python=sys.executable, temp=temp_path, temp_data=temp_data,
              temp_env={key: os.environ.get(key, "") for key in ("TEMP", "TMP", "TMPDIR")},
              isolated=bool(sys.flags.isolated and sys.flags.no_site))
print(json.dumps(dict(ok=True, result=json.dumps(report), exit_code=0)))
`

// SPDX-License-Identifier: AGPL-3.0-or-later

// Package security implements the execution-side hardening of design doc §16
// and plan P3-29..P3-32: a reduced subprocess environment, network egress
// validation, secret scrubbing, a high-risk audit trail, and — when
// sandbox.mode is configured — OS-level confinement for every spawned
// subprocess (seatbelt on macOS, bubblewrap on Linux; see policy.go and the
// platform sandbox_* files). Everything here is deterministic — the
// "adversarial model review" layers (design §14.2 Layer 2/3) are a later,
// model-dependent phase.
package security

import (
	"os"
	"os/exec"
	"runtime"
)

// Sandbox sets an adapter subprocess's working directory and replaces its
// inherited environment with a fixed allow-list (plan P3-29).
//
// Apply stays the historical shape: environment filtering and cwd only, no OS
// boundary — the child can still read and write any path its uid can, starting
// with $HOME (deliberately forwarded because agent CLIs keep credentials and
// config there). ApplyPolicy layers the real confinement on top: a deny-default
// sandbox-exec profile on macOS or a bubblewrap bind-mount jail on Linux, driven
// by Policy (see policy.go). What both share: the child starts in the task
// directory instead of wherever the daemon happens to run, and it does not
// inherit the parent's arbitrary environment, so an unrelated secret in the
// daemon's env does not silently reach a third-party agent CLI.
type Sandbox struct {
	dir string // task working directory; "" = inherit the parent's
}

// NewSandbox builds a sandbox rooted at dir.
func NewSandbox(dir string) *Sandbox { return &Sandbox{dir: dir} }

// posixEnvKeys are the variables a POSIX adapter needs to function at all.
// HOME is here on purpose — agent CLIs read their own config and auth from it.
// Proxy variables are included so CLIs can reach model providers behind proxies.
var posixEnvKeys = []string{
	"PATH", "HOME", "USER", "SHELL", "LANG", "LC_ALL", "TMPDIR",
	"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "all_proxy", "no_proxy",
}

// windowsEnvKeys are the Windows equivalents. This list is not cosmetic: a
// process started with only PATH set on Windows is broken in ways that look like
// application bugs. Python resolves its install root through SYSTEMROOT and its
// user site-packages through APPDATA, the runtime finds DLLs under SystemRoot,
// PATHEXT is what makes `python` resolve to `python.exe` at all, and TEMP/TMP
// are where every toolchain writes scratch files. Sending a POSIX-only env to a
// Windows adapter is why the compute node could not launch one.
//
// Case matters to no one here (Windows env lookup is case-insensitive) but the
// spellings below are the conventional ones.
var windowsEnvKeys = []string{
	"PATH", "PATHEXT", "SYSTEMROOT", "windir", "SystemDrive", "COMSPEC",
	"TEMP", "TMP", "USERPROFILE", "HOMEDRIVE", "HOMEPATH",
	"APPDATA", "LOCALAPPDATA", "ProgramData", "ProgramFiles", "ProgramFiles(x86)",
	"USERNAME", "USERDOMAIN", "NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE",
	"LANG",
	"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "all_proxy", "no_proxy",
}

// Env returns the subprocess environment: a fixed allow-list of the variables an
// adapter needs to function at all, plus any explicitly injected entries (e.g.
// model credentials). Everything else in the parent's environment is dropped.
// HOME is forwarded on purpose — agent CLIs read their own config and auth from
// it — so this is an environment filter, not a home-directory boundary.
//
// Unset variables are omitted rather than forwarded empty: exporting HOME="" to
// a child is not the same as leaving it unset, and several CLIs treat the empty
// value as a configured-but-broken home and fail instead of falling back.
func (s *Sandbox) Env(extra ...string) []string {
	keys := posixEnvKeys
	if runtime.GOOS == "windows" {
		keys = windowsEnvKeys
	}
	env := make([]string, 0, len(keys)+len(extra))
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			env = append(env, k+"="+v)
		}
	}
	return append(env, extra...)
}

// Apply configures cmd with the sandboxed working directory and environment.
func (s *Sandbox) Apply(cmd *exec.Cmd, extra ...string) {
	s.ApplyPolicy(cmd, DefaultPolicy(s.dir), extra...)
}

// ApplyPolicy is Apply plus the OS confinement layer: it installs the
// environment and cwd first, then wraps the command under the platform's
// sandbox mechanism when the policy's mode asks for one. With ModeOff — or on
// a platform with no backend installed — it degenerates to the plain
// Apply behavior, so callers need no fallback path.
func (s *Sandbox) ApplyPolicy(cmd *exec.Cmd, p Policy, extra ...string) {
	cmd.Dir = p.resolveWorkDir()
	cmd.Env = s.Env(extra...)
	wrapSubprocess(cmd, p)
}

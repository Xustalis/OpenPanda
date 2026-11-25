// SPDX-License-Identifier: AGPL-3.0-or-later

// Package security implements the execution-side hardening of docs/design/execution-and-defense.md
// and plan P3-29..P3-32: a reduced subprocess environment, network egress
// validation, secret scrubbing, a high-risk audit trail, and — when
// sandbox.mode is configured — OS-level confinement for every spawned
// subprocess (seatbelt on macOS, bubblewrap on Linux, AppContainer plus a
// write-restricted token on Windows; see policy.go and the
// platform sandbox_* files). Everything here is deterministic — the
// "adversarial model review" layers (3; see docs/design/execution-and-defense.md) are a later,
// model-dependent phase.
package security

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// Sandbox sets an adapter subprocess's working directory and replaces its
// inherited environment with a fixed allow-list (plan P3-29).
//
// Apply uses the process-wide policy; ApplyPolicy accepts an explicit one.
// ModeOff only filters the environment and sets cwd — the child can still read
// and write any path its uid can, including $HOME (deliberately forwarded
// because agent CLIs keep credentials and config there). Standard and strict
// add a deny-default sandbox-exec profile on macOS or a bubblewrap bind-mount
// jail on Linux, or AppContainer/token restrictions on Windows, driven by
// Policy (see policy.go). The child starts in the task
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

// Apply configures cmd with the working directory, filtered environment and
// process-wide OS confinement policy.
func (s *Sandbox) Apply(cmd *exec.Cmd, extra ...string) error {
	return s.ApplyPolicy(cmd, DefaultPolicy(s.dir), extra...)
}

// ApplyPolicy is Apply plus the OS confinement layer: it installs the
// environment and cwd first, then wraps the command under the platform's
// sandbox mechanism when the policy's mode asks for one. With ModeOff it
// degenerates to the plain Apply behavior.
//
// Both standard and strict return an error on a host with no backend: the
// requested OS boundary must exist before the child may start. ModeOff is
// the explicit choice to run with environment filtering and cwd only.
func (s *Sandbox) ApplyPolicy(cmd *exec.Cmd, p Policy, extra ...string) error {
	cmd.Dir = p.resolveWorkDir()
	cmd.Env = s.Env(extra...)
	return wrapSubprocess(cmd, p)
}

// ErrSandboxUnavailable reports a policy that asks for OS confinement the host
// cannot provide.
var ErrSandboxUnavailable = errors.New("security: sandbox backend unavailable")

// backendMissing refuses every configured confinement mode when its backend
// is missing. Neither standard nor strict may degrade to an unconfined run.
func backendMissing(mode Mode, why string) error {
	if mode == ModeStandard || mode == ModeStrict {
		return fmt.Errorf("%w: sandbox.mode is %q but %s; refusing to start subprocess", ErrSandboxUnavailable, mode, why)
	}
	return nil
}

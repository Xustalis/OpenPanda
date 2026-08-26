// Package security implements the execution-side hardening of design doc §16
// and plan P3-29..P3-32: a reduced subprocess environment, network egress
// validation, secret scrubbing, and a high-risk audit trail. Everything here is
// deterministic — the "adversarial model review" layers (design §14.2 Layer
// 2/3) are a later, model-dependent phase. See Sandbox for what the execution
// hardening does and, importantly, does not confine.
package security

import (
	"os"
	"os/exec"
)

// Sandbox sets an adapter subprocess's working directory and replaces its
// inherited environment with a fixed allow-list (plan P3-29).
//
// It is NOT an isolation boundary, and callers must not treat it as one. Naming
// what it does *not* do is the whole point of this comment: there is no
// filesystem confinement (the child can read and write any path its uid can,
// starting with $HOME, which is deliberately forwarded because agent CLIs keep
// their credentials and config there), no seccomp or namespaces, no rlimits, and
// no network restriction — egress policy lives in netguard, enforced by the
// caller, not here.
//
// What it actually buys: the child starts in the task directory instead of
// wherever the daemon happens to run, and it does not inherit the parent's
// arbitrary environment, so an unrelated secret in the daemon's env does not
// silently reach a third-party agent CLI. Real OS-level isolation
// (seccomp/namespaces on Linux, sandbox-exec on macOS, or a container) is a
// deploy concern layered on top; until it exists, the permission tiers in
// internal/defense are the only thing standing between a plan and a destructive
// command.
type Sandbox struct {
	dir string // task working directory; "" = inherit the parent's
}

// NewSandbox builds a sandbox rooted at dir.
func NewSandbox(dir string) *Sandbox { return &Sandbox{dir: dir} }

// Env returns the subprocess environment: a fixed allow-list of the variables an
// adapter needs to function at all, plus any explicitly injected entries (e.g.
// model credentials). Everything else in the parent's environment is dropped.
// HOME is forwarded on purpose — agent CLIs read their own config and auth from
// it — so this is an environment filter, not a home-directory boundary.
func (s *Sandbox) Env(extra ...string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"USER=" + os.Getenv("USER"),
		"SHELL=" + os.Getenv("SHELL"),
		"LANG=" + os.Getenv("LANG"),
		"TMPDIR=" + os.Getenv("TMPDIR"),
	}
	return append(env, extra...)
}

// Apply configures cmd with the sandboxed working directory and environment.
func (s *Sandbox) Apply(cmd *exec.Cmd, extra ...string) {
	cmd.Dir = s.dir
	cmd.Env = s.Env(extra...)
}

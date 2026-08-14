// Package security implements the execution-side hardening of design doc §16
// and plan P3-29..P3-32: a sandboxed subprocess environment, network egress
// validation, secret scrubbing, and a high-risk audit trail. Everything here is
// deterministic — the "adversarial model review" layers (design §14.2 Layer
// 2/3) are a later, model-dependent phase.
package security

import (
	"os"
	"os/exec"
)

// Sandbox confines an adapter subprocess to a working directory and a minimal
// environment (plan P3-29). It is the deterministic MVP of "the agent only
// reads/writes the task directory": it removes the two most common leak
// vectors — an inherited home directory and a full environment full of
// unrelated secrets. Hard Linux-level isolation (seccomp/namespaces) is a
// deploy concern layered on top of this.
type Sandbox struct {
	dir string // task working directory; "" = inherit
}

// NewSandbox builds a sandbox rooted at dir.
func NewSandbox(dir string) *Sandbox { return &Sandbox{dir: dir} }

// Env returns the minimal environment for the subprocess: the path/home/locale
// an adapter needs, plus any explicitly injected entries (e.g. model
// credentials). The parent's full environment is deliberately not forwarded.
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

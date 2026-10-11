// SPDX-License-Identifier: AGPL-3.0-or-later

// Package commander implements the three-tier capability execution:
// native (deterministic commands, no model), agent (AI CLI via adapter), and
// manual (human-notified). Design doc §6.
package commander

import (
	"context"
	"os/exec"
	"time"

	"github.com/Xustalis/OpenPanda/internal/executil"
	"github.com/Xustalis/OpenPanda/internal/security"
)

// NativeResult is the outcome of a deterministic command.
type NativeResult struct {
	OK       bool
	ExitCode int
	Stdout   string
	Stderr   string
}

// Executor runs native commands with a timeout and captures output. It never
// invokes an LLM — native commands are deterministic by contract.
type Executor struct {
	timeout time.Duration
	dir     string   // working directory; "" = inherit
	env     []string // extra env, appended to os.Environ()
}

// DefaultNativeTimeout is the built-in per-command budget NewExecutor ships
// with. The operator's timeouts.native_s knob replaces THIS value only — an
// explicit WithTimeout is a deliberate bound the global default must not
// stretch (a 30s probe command would otherwise inherit a 30m ceiling).
const DefaultNativeTimeout = 5 * time.Minute

// NewExecutor creates a native executor. Default timeout 5m, no working dir.
func NewExecutor() *Executor {
	return &Executor{timeout: DefaultNativeTimeout}
}

// WithTimeout sets the command timeout.
func (e *Executor) WithTimeout(d time.Duration) *Executor { e.timeout = d; return e }

// nativeTimeout overrides every executor's own timeout when positive. It is
// the operator's timeouts.native_s knob, process-global like the agent budget
// (SetAgentTimeout): routers and executors are built before the config is
// applied, so the ceiling has to be readable at Run time, not frozen at
// construction. Zero means "unset" — each executor keeps its own default.
var nativeTimeout time.Duration

// SetNativeTimeout applies the configured native-command budget. Zero or
// negative leaves executors on their built-in default (5m), so an
// unconfigured fleet behaves exactly as before.
func SetNativeTimeout(d time.Duration) {
	if d <= 0 {
		nativeTimeout = 0
		return
	}
	nativeTimeout = d
}

// NativeTimeout reports the effective process-global native budget; zero when
// unset (callers fall back to the executor's own timeout).
func NativeTimeout() time.Duration { return nativeTimeout }

// WithDir sets the working directory for commands.
func (e *Executor) WithDir(dir string) *Executor { e.dir = dir; return e }

// Run executes a command and returns its result. ctx may carry a shorter
// deadline than the effective timeout; the operator's native_s knob (when
// set) replaces the executor's built-in default, never an explicit
// WithTimeout bound.
func (e *Executor) Run(ctx context.Context, command string, args ...string) NativeResult {
	timeout := e.timeout
	if timeout == DefaultNativeTimeout && nativeTimeout > 0 {
		timeout = nativeTimeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := executil.CommandContext(ctx, command, args...)
	// Run under the same minimal, secret-free environment as adapter
	// subprocesses (security.Sandbox), never the parent's full os.Environ(),
	// so a native command cannot exfiltrate the model API key or other host
	// secrets (P1-1). ApplyPolicy also wraps the process under the OS sandbox
	// (seatbelt/bwrap) when the configured mode asks for it — the policy
	// confines a bare command tighter than an adapter, which owns its own
	// credential dirs.
	security.NewSandbox(e.dir).ApplyPolicy(cmd, nativeSandboxPolicy(e.dir), e.env...)

	var stdout, stderr executil.Capture
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Custom writers spawn copy goroutines Wait blocks on; a detached
	// grandchild inheriting the pipes would otherwise hold them open and
	// wedge Wait forever after the command itself exits (or is killed).
	cmd.WaitDelay = 5 * time.Second

	err := cmd.Run()
	res := NativeResult{
		ExitCode: 0,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}
	switch {
	case err == nil:
		res.OK = true
	case ctx.Err() == context.DeadlineExceeded:
		res.OK = false
		res.ExitCode = 124 // convention: timeout (matches coreutils timeout)
		if res.Stderr == "" {
			res.Stderr = "command timed out"
		}
	case ctx.Err() == context.Canceled:
		res.OK = false
		res.ExitCode = 130 // convention: SIGINT
		if res.Stderr == "" {
			res.Stderr = "command canceled"
		}
	default:
		if ee, ok := err.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
			res.OK = res.ExitCode == 0
			if res.ExitCode < 0 && res.Stderr == "" {
				// Killed by a signal (ExitCode -1): give a diagnostic rather than
				// an empty stderr.
				res.Stderr = err.Error()
			}
		} else {
			// Command could not start (not found, permission, …): a non-zero
			// code so a failed start is never reported as a clean exit (D11).
			res.OK = false
			res.ExitCode = 127
			if res.Stderr == "" {
				res.Stderr = err.Error()
			}
		}
	}
	return res
}

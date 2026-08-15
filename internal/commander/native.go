// Package commander implements the three-tier capability execution:
// native (deterministic commands, no model), agent (AI CLI via adapter), and
// manual (human-notified). Design doc §6.
package commander

import (
	"context"
	"os/exec"
	"time"

	"github.com/xenith/panda/internal/security"
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

// NewExecutor creates a native executor. Default timeout 5m, no working dir.
func NewExecutor() *Executor {
	return &Executor{timeout: 5 * time.Minute}
}

// WithTimeout sets the command timeout.
func (e *Executor) WithTimeout(d time.Duration) *Executor { e.timeout = d; return e }

// WithDir sets the working directory for commands.
func (e *Executor) WithDir(dir string) *Executor { e.dir = dir; return e }

// Run executes a command and returns its result. ctx may carry a shorter
// deadline than e.timeout.
func (e *Executor) Run(ctx context.Context, command string, args ...string) NativeResult {
	if e.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = e.dir
	// Run under the same minimal, secret-free environment as adapter
	// subprocesses (security.Sandbox), never the parent's full os.Environ(),
	// so a native command cannot exfiltrate the model API key or other host
	// secrets (P1-1).
	cmd.Env = security.NewSandbox(e.dir).Env(e.env...)

	var stdout, stderr []byte
	cmd.Stdout = &outBuf{&stdout}
	cmd.Stderr = &outBuf{&stderr}

	err := cmd.Run()
	res := NativeResult{
		ExitCode: 0,
		Stdout:   string(stdout),
		Stderr:   string(stderr),
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
			res.OK = ee.ExitCode() == 0
		} else {
			// Command could not start (not found, timeout, ctx cancelled).
			res.OK = false
			if res.Stderr == "" {
				res.Stderr = err.Error()
			}
		}
	} else {
		res.OK = true
	}
	return res
}

// outBuf accumulates writes into a byte slice (exec.Cmd requires io.Writer).
type outBuf struct{ p *[]byte }

func (b *outBuf) Write(p []byte) (int, error) {
	*b.p = append(*b.p, p...)
	return len(p), nil
}

// LookPath reports whether an executable exists on PATH.
func LookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// SPDX-License-Identifier: AGPL-3.0-or-later

package contract

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Status is the outcome of one check.
type Status string

const (
	StatusPass    Status = "pass"
	StatusFail    Status = "fail"
	StatusError   Status = "error"   // the check could not run (launch failure)
	StatusSkipped Status = "skipped" // budget exhausted before it ran
	StatusFlaky   Status = "flaky"   // failed, then passed on a single retry
)

// Verdict statuses. verified means every selected check ran and passed.
// unverified is the honest label for an agent run that had neither a
// supervisor nor a delivery contract: the work may well be fine, but nothing
// checked it, so the row must not read like a verified one.
const (
	VerdictVerified   = "verified"
	VerdictFailed     = "failed"
	VerdictPartial    = "partial"
	VerdictSkipped    = "skipped"
	VerdictUnverified = "unverified"
)

// ExecResult is what a process launcher returns. Err is set when the process
// could not be started at all; a nonzero ExitCode with Err nil is a normal
// failed run.
type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
	Err      error
}

// ExecFunc runs argv in dir. The core supplies one backed by its sandboxed
// native executor; tests supply fakes.
type ExecFunc func(ctx context.Context, dir string, argv []string) ExecResult

// Result is one check's outcome, ready to persist and render.
type Result struct {
	Phase      string        `json:"phase"`
	CheckID    string        `json:"check_id"`
	Kind       string        `json:"kind"`
	Command    string        `json:"command,omitempty"`
	Status     Status        `json:"status"`
	ExitCode   int           `json:"exit_code"`
	DurationMS int64         `json:"duration_ms"`
	OutputHead string        `json:"output_head,omitempty"`
	OutputTail string        `json:"output_tail,omitempty"`
	Reason     string        `json:"reason,omitempty"`
	Duration   time.Duration `json:"-"`
}

// DefaultCheckTimeout and DefaultBudget bound one check and the whole phase.
// They are the operators' floor, not a model's choice: an acceptance suite
// that wants an hour needs an explicit configuration.
const (
	DefaultCheckTimeout = 10 * time.Minute
	DefaultBudget       = 30 * time.Minute
)

// Runner executes checks in one directory. The zero value is usable for
// artifact checks; command checks need Exec.
type Runner struct {
	Dir        string
	Timeout    time.Duration // per check; 0 = DefaultCheckTimeout
	Budget     time.Duration // whole phase; 0 = DefaultBudget, negative = unbounded
	Exec       ExecFunc
	RetryFlaky bool
	Now        func() time.Time
	// Guard may refuse a command before it runs. The core uses it to keep
	// irreversible commands out of acceptance checks: a contract is evidence,
	// not a second, ungated way to execute. A refused check is recorded as
	// skipped with the reason, never as a failure of the work.
	Guard func(argv []string) error
}

// Run executes checks and returns one Result per check, in order.
func (r *Runner) Run(ctx context.Context, phase string, checks []Check) []Result {
	now := r.Now
	if now == nil {
		now = time.Now
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}
	budget := r.Budget
	if budget == 0 {
		budget = DefaultBudget
	}
	started := now()
	results := make([]Result, 0, len(checks))
	for _, c := range checks {
		if budget > 0 && now().Sub(started) >= budget {
			results = append(results, Result{
				Phase: phase, CheckID: c.ID, Kind: c.EffectiveKind(), Command: c.Run,
				Status: StatusSkipped, Reason: "acceptance budget exhausted",
			})
			continue
		}
		res := r.runOne(ctx, phase, c, timeout, budget, started, now)
		results = append(results, res)
	}
	return results
}

func (r *Runner) runOne(ctx context.Context, phase string, c Check, timeout, budget time.Duration, started time.Time, now func() time.Time) Result {
	res := Result{Phase: phase, CheckID: c.ID, Kind: c.EffectiveKind(), Command: c.Run}
	begin := now()
	if c.EffectiveKind() == KindArtifact {
		var missing []string
		for _, p := range c.Exists {
			if _, err := os.Stat(filepath.Join(r.Dir, filepath.FromSlash(p))); err != nil {
				missing = append(missing, p)
			}
		}
		switch {
		case len(missing) == 0:
			res.Status = StatusPass
		default:
			res.Status = StatusFail
			res.Reason = "missing: " + strings.Join(missing, ", ")
		}
		res.Duration = now().Sub(begin)
		res.DurationMS = res.Duration.Milliseconds()
		return res
	}

	argv, err := SplitCommand(c.Run)
	if err != nil {
		res.Status = StatusError
		res.Reason = err.Error()
		return res
	}
	if r.Guard != nil {
		if gerr := r.Guard(argv); gerr != nil {
			res.Status = StatusSkipped
			res.Reason = gerr.Error()
			res.Duration = now().Sub(begin)
			res.DurationMS = res.Duration.Milliseconds()
			return res
		}
	}
	if r.Exec == nil {
		res.Status = StatusError
		res.Reason = "no executor configured"
		return res
	}
	if budget > 0 {
		if remaining := budget - now().Sub(started); remaining < timeout {
			timeout = remaining
		}
	}
	out, runErr := runWithTimeout(ctx, timeout, r.Dir, argv, r.Exec)

	res.Duration += out.Duration
	if res.Duration == 0 {
		res.Duration = now().Sub(begin)
	}
	res.ExitCode = normalizeExit(ctx.Err(), runErr, out)
	res.Status, res.Reason = judge(c, out)
	res.DurationMS = res.Duration.Milliseconds()
	res.OutputHead, res.OutputTail = clipOutput(out.Stdout, out.Stderr)

	// A single retry distinguishes a flaky suite from a regression. It only
	// runs on the final phase: retrying every baseline check doubles the cost
	// of a suite that is expected to fail before the work starts.
	if res.Status != StatusPass && r.RetryFlaky && ctx.Err() == nil {
		retryBegin := now()
		retryOut, retryErr := runWithTimeout(ctx, timeout, r.Dir, argv, r.Exec)
		retryOut.ExitCode = normalizeExit(ctx.Err(), retryErr, retryOut)
		retryStatus, retryReason := judge(c, retryOut)
		res.Duration += now().Sub(retryBegin)
		res.DurationMS = res.Duration.Milliseconds()
		res.ExitCode = retryOut.ExitCode
		res.OutputHead, res.OutputTail = clipOutput(retryOut.Stdout, retryOut.Stderr)
		if retryStatus == StatusPass {
			res.Status = StatusFlaky
			res.Reason = "failed, then passed on retry"
		} else {
			res.Reason = retryReason
		}
	}
	return res
}

// runWithTimeout runs one argv under an optional deadline and reports the
// context's own error state before the cancel runs.
func runWithTimeout(ctx context.Context, timeout time.Duration, dir string, argv []string, exec ExecFunc) (ExecResult, error) {
	if timeout <= 0 {
		out := exec(ctx, dir, argv)
		return out, ctx.Err()
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	out := exec(runCtx, dir, argv)
	err := runCtx.Err()
	cancel()
	return out, err
}

// normalizeExit maps the context's own expiry onto the timeout convention.
func normalizeExit(parentErr, runErr error, out ExecResult) int {
	if runErr == context.DeadlineExceeded || parentErr == context.DeadlineExceeded {
		return 124
	}
	if runErr == context.Canceled || parentErr == context.Canceled {
		return 130
	}
	return out.ExitCode
}

// judge applies the check's expectation to one execution.
func judge(c Check, out ExecResult) (Status, string) {
	if out.Err != nil {
		return StatusError, out.Err.Error()
	}
	if out.ExitCode != c.ExpectedExit() {
		if out.ExitCode == 124 {
			// 124 is the timeout convention, not the program's own verdict: a
			// suite that never finished proved nothing about the work.
			return StatusError, "timed out (exit 124)"
		}
		reason := fmt.Sprintf("exit %d, want %d", out.ExitCode, c.ExpectedExit())
		return StatusFail, reason
	}
	if c.Expect.StdoutContains != "" && !strings.Contains(out.Stdout, c.Expect.StdoutContains) {
		return StatusFail, fmt.Sprintf("stdout does not contain %q", c.Expect.StdoutContains)
	}
	return StatusPass, ""
}

// clipOutput keeps the head and tail of the combined output: the head says
// what was attempted, the tail says how it ended. Both halves are bounded so
// a noisy suite cannot bloat the task row.
func clipOutput(stdout, stderr string) (string, string) {
	combined := stdout
	if stderr != "" {
		if combined != "" {
			combined += "\n"
		}
		combined += stderr
	}
	combined = strings.TrimSpace(combined)
	const limit = 8192
	const half = limit / 2
	if len(combined) <= limit {
		return combined, ""
	}
	head := strings.ToValidUTF8(combined[:half], "")
	tail := strings.ToValidUTF8(combined[len(combined)-half:], "")
	return head, tail
}

// Verdict is the combined judgment of a contract. It is computed from the
// baseline and final results, and it names the checks by id so every renderer
// can phrase them in its own language.
type Verdict struct {
	Status      string   `json:"status"`
	Passed      []string `json:"passed,omitempty"`
	Failed      []string `json:"failed,omitempty"`
	Regressions []string `json:"regressions,omitempty"`
	PreExisting []string `json:"pre_existing,omitempty"`
	Skipped     []string `json:"skipped,omitempty"`
	Flaky       []string `json:"flaky,omitempty"`
	Reasons     []string `json:"reasons,omitempty"`
}

// Evaluate compares the final results against the baseline. A check that
// fails after passing at baseline is named a regression; one that was already
// failing is named as pre-existing. Either way the contract is not verified:
// the baseline explains the failure, it does not excuse it.
func Evaluate(checks []Check, baseline, final []Result) Verdict {
	v := Verdict{}
	if len(checks) == 0 {
		v.Status = VerdictSkipped
		return v
	}
	base := make(map[string]Status, len(baseline))
	for _, r := range baseline {
		base[r.CheckID] = r.Status
	}
	fin := make(map[string]Result, len(final))
	for _, r := range final {
		fin[r.CheckID] = r
	}
	for _, c := range checks {
		r, ok := fin[c.ID]
		if !ok || r.Status == StatusSkipped {
			v.Skipped = append(v.Skipped, c.ID)
			continue
		}
		switch r.Status {
		case StatusPass:
			v.Passed = append(v.Passed, c.ID)
		case StatusFlaky:
			v.Passed = append(v.Passed, c.ID)
			v.Flaky = append(v.Flaky, c.ID)
		default:
			v.Failed = append(v.Failed, c.ID)
			was := base[c.ID]
			switch was {
			case StatusPass, StatusFlaky:
				v.Regressions = append(v.Regressions, c.ID)
				v.Reasons = append(v.Reasons, fmt.Sprintf("%s regressed: it passed before the run and fails now", c.ID))
			case StatusFail, StatusError:
				v.PreExisting = append(v.PreExisting, c.ID)
				v.Reasons = append(v.Reasons, fmt.Sprintf("%s was already failing at baseline and still fails", c.ID))
			default:
				v.Reasons = append(v.Reasons, fmt.Sprintf("%s failed", c.ID))
			}
		}
	}
	switch {
	case len(v.Failed) > 0:
		v.Status = VerdictFailed
	case len(v.Passed) == 0:
		// Nothing ran or everything was skipped: no evidence either way.
		v.Status = VerdictPartial
	case len(v.Skipped) > 0:
		v.Status = VerdictPartial
	default:
		v.Status = VerdictVerified
	}
	return v
}

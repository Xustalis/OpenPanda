// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/contract"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// Acceptance phases. The baseline runs before the agent touches the tree; the
// final runs after it claims done. The pair is what distinguishes a regression
// from a suite that was already red.
const (
	acceptancePhaseBaseline = "baseline"
	acceptancePhaseFinal    = "final"
)

// taskAcceptance parses the contract out of a task's persisted spec. A spec
// this build cannot parse, or a contract that fails validation, yields nil: an
// invalid contract must not gate work it cannot judge.
func taskAcceptance(specJSON string) []contract.Check {
	if specJSON == "" {
		return nil
	}
	var spec struct {
		Acceptance []contract.Check `json:"acceptance"`
	}
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		return nil
	}
	checks, err := contract.Validate(spec.Acceptance)
	if err != nil {
		return nil
	}
	return checks
}

// Acceptance commands execute outside the agent's restricted tool face. A
// read-only/plan task must not run them automatically, even if a check was
// selected from the project shortlist or supplied by the user.
func executableAcceptance(specJSON string) []contract.Check {
	if taskPermissionMode(specJSON) == defense.ModePlan {
		return nil
	}
	return taskAcceptance(specJSON)
}

// acceptanceConfig reads the configured budgets under the same lock the
// runtime setters use.
func (c *Core) acceptanceConfig() config.AcceptanceConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.acceptance
}

// runAcceptance executes checks in workDir, records each result as evidence,
// and returns them. It never fails the task by itself: the verdict belongs to
// finalizeRun, which decides between done, failed and review.
//
// The checks run through the same sandboxed native executor as any other
// command, with an explicit per-check timeout so the global native budget
// cannot silently shorten a suite the operator configured.
func (c *Core) runAcceptance(ctx context.Context, taskID, phase, workDir string, checks []contract.Check) []contract.Result {
	if len(checks) == 0 {
		return nil
	}
	cfg := c.acceptanceConfig()
	timeout := cfg.CheckTimeout()
	if timeout <= 0 {
		timeout = contract.DefaultCheckTimeout
	}
	runner := &contract.Runner{
		Dir:     workDir,
		Timeout: timeout,
		Budget:  cfg.Budget(),
		Exec: func(ctx context.Context, dir string, argv []string) contract.ExecResult {
			start := time.Now()
			ex := commander.NewExecutor().WithDir(dir).WithTimeout(timeout)
			res := ex.Run(ctx, argv[0], argv[1:]...)
			return contract.ExecResult{
				ExitCode: res.ExitCode,
				Stdout:   res.Stdout,
				Stderr:   res.Stderr,
				Duration: time.Since(start),
			}
		},
		RetryFlaky: phase == acceptancePhaseFinal && cfg.FlakyRetry(),
		// The executor below is the sandboxed native one, but the tier gate
		// lives in the router, not in the executor: without this guard a check
		// would be a second way to run an irreversible command with no
		// authorization, and the contract is evidence, not a privilege.
		Guard: func(argv []string) error {
			if defense.TierFromCommand(argv[0], argv[1:]...) == defense.TierIrreversible {
				return fmt.Errorf("irreversible command %q is not run as an acceptance check", argv[0])
			}
			return nil
		},
	}
	results := runner.Run(ctx, phase, checks)
	for _, r := range results {
		if err := c.store.RecordCheck(context.WithoutCancel(ctx), taskID, r); err != nil {
			c.logger.Warn("record acceptance check", "task", taskID, "check", r.CheckID, "err", err)
		}
		c.EvTrace(context.WithoutCancel(ctx), taskID, EvCheckResult, map[string]any{
			"phase":       r.Phase,
			"check":       r.CheckID,
			"kind":        r.Kind,
			"status":      string(r.Status),
			"exit_code":   r.ExitCode,
			"duration_ms": r.DurationMS,
			"reason":      r.Reason,
		})
	}
	return results
}

// stripPeerAcceptance removes an acceptance contract from a spec that arrived
// from another node. The origin's checks stay on the origin's row and are that
// user's evidence; the executor must not run commands authored elsewhere, so
// the field is dropped at the wire boundary. Returns the spec unchanged and
// false when it carried nothing to strip.
func stripPeerAcceptance(specJSON string) (string, bool) {
	if specJSON == "" || !strings.Contains(specJSON, `"acceptance"`) {
		return specJSON, false
	}
	var spec map[string]json.RawMessage
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		return specJSON, false
	}
	if _, ok := spec["acceptance"]; !ok {
		return specJSON, false
	}
	delete(spec, "acceptance")
	out, err := json.Marshal(spec)
	if err != nil {
		return specJSON, false
	}
	return string(out), true
}

// acceptanceFailureReason is the one-line failure a task carries when its
// contract is not met. It names the checks, not the model's prose.
func acceptanceFailureReason(v contract.Verdict) string {
	var parts []string
	parts = append(parts, "acceptance failed: "+strings.Join(v.Failed, ", "))
	if len(v.Regressions) > 0 {
		parts = append(parts, "regressed since baseline: "+strings.Join(v.Regressions, ", "))
	}
	if len(v.PreExisting) > 0 {
		parts = append(parts, "already failing at baseline: "+strings.Join(v.PreExisting, ", "))
	}
	return strings.Join(parts, "; ")
}

// acceptanceSummary is the report fragment appended to the agent's stdout when
// a contract was evaluated, so the delegator sees the evidence even when the
// detail table is not available on the origin node.
func acceptanceSummary(v contract.Verdict, loc i18n.Locale) string {
	if v.Status == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(i18n.Tf(loc, "contract.summary.head", "status", v.Status))
	if len(v.Passed) > 0 {
		b.WriteString(i18n.Tf(loc, "contract.summary.passed", "checks", strings.Join(v.Passed, ", ")))
	}
	if len(v.Failed) > 0 {
		b.WriteString(i18n.Tf(loc, "contract.summary.failed", "checks", strings.Join(v.Failed, ", ")))
	}
	if len(v.Skipped) > 0 {
		b.WriteString(i18n.Tf(loc, "contract.summary.skipped", "checks", strings.Join(v.Skipped, ", ")))
	}
	return b.String()
}

// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

// Child outputs belong to the workspace that submitted their snapshot, which
// may be a stage or an isolated parent rather than the configured checkout.
// Delivery happens while the parent yields its locks, before await returns.
func (c *Core) childOutputDelivery(ctx context.Context, child Task) bool {
	if child.ParentID == "" || len(child.Inputs) == 0 {
		return false
	}
	_, err := c.store.Get(ctx, child.ParentID)
	return err == nil
}

func (c *Core) deliverChildOutput(ctx context.Context, child Task, result bus.TaskResultPayload) error {
	if !c.childOutputDelivery(ctx, child) || result.State != StateDone {
		return nil
	}
	if result.OutputArtifact == "" || c.artifacts == nil {
		return fmt.Errorf("child completed without a deliverable workspace artifact")
	}
	if err := c.store.SetOutputArtifact(ctx, child.TaskID, result.OutputArtifact); err != nil {
		return err
	}
	child.OutputArtifact = result.OutputArtifact
	ctx, run, delivered, stop, err := c.holdMergeLease(ctx, child)
	if err != nil {
		return fmt.Errorf("child delivery lease: %w", err)
	}
	defer stop()
	if delivered {
		return nil
	}
	dst, err := c.childWorkspaceSource(ctx, child.ParentID)
	if err != nil {
		return fmt.Errorf("parent workspace unavailable for child delivery: %w", err)
	}
	if dst == "" || !notHomeOrRoot(dst) {
		return fmt.Errorf("parent workspace unavailable for child delivery")
	}
	if st, err := os.Stat(dst); err != nil || !st.IsDir() {
		return fmt.Errorf("parent workspace no longer exists")
	}
	from, err := c.store.DispatchTarget(ctx, child.TaskID)
	if err != nil {
		return fmt.Errorf("child executor unavailable for delivery: %w", err)
	}
	if from == "" {
		return fmt.Errorf("child executor unavailable for delivery")
	}
	if _, held := c.artifacts.Has(result.OutputArtifact); !held {
		if _, err := c.FetchArtifact(ctx, from, child.TaskID, result.OutputArtifact); err != nil {
			return fmt.Errorf("fetch child output: %w", err)
		}
	}
	beforeDir, err := os.MkdirTemp("", "panda-child-before-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(beforeDir)
	afterDir, err := os.MkdirTemp("", "panda-child-after-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(afterDir)
	skip := c.extractSkipSet(dst)
	// Extract both sides under the SAME policy, so a withheld file cannot
	// become a deletion in the three-way comparison.
	if _, err := c.artifacts.ExtractExcept(child.Inputs[0].Hash, beforeDir, skip); err != nil {
		return fmt.Errorf("read child fork snapshot: %w", err)
	}
	m, err := c.artifacts.ExtractExcept(result.OutputArtifact, afterDir, skip)
	if err != nil {
		return fmt.Errorf("read child output: %w", err)
	}
	before, err := c.hashWorkTree(beforeDir)
	if err != nil {
		return err
	}
	after, err := c.hashWorkTree(afterDir)
	if err != nil {
		return err
	}
	mergeTask := child
	mergeTask.WorkDir = dst
	release, err := c.acquireMergeLocks(ctx, mergeTask, run)
	if err != nil {
		return err
	}
	defer release()
	if err := c.store.renewMergeLease(ctx, child, run, false); err != nil {
		return err
	}
	merged, conflicts, err := c.applyIsolatedChangesContext(ctx, afterDir, dst, before, after, nil)
	c.EvTrace(context.WithoutCancel(ctx), child.TaskID, EvProjectSync, map[string]any{
		"stage": "child_delivery", "dir": dst, "hash": result.OutputArtifact, "from": from,
		"merged": merged, "conflicts": conflicts, "withheld": m.SkippedPaths, "ok": err == nil && len(conflicts) == 0,
	})
	if err != nil {
		return err
	}
	if len(conflicts) > 0 {
		return fmt.Errorf("child workspace delivery has conflicts: %s; output artifact retained as %s", strings.Join(conflicts[:min(len(conflicts), maxIsolatedConflicts)], ", "), result.OutputArtifact)
	}
	return c.store.renewMergeLease(ctx, child, run, true)
}

func childStoredOutput(t Task) bus.TaskResultPayload {
	var result bus.TaskResultPayload
	_ = json.Unmarshal([]byte(t.ResultJSON), &result)
	result.TaskID, result.AttemptID, result.State = t.TaskID, t.AttemptID, t.State
	if result.OutputArtifact == "" {
		result.OutputArtifact = t.OutputArtifact
	}
	return result
}

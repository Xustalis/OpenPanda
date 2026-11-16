// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

type delegateSnapshotKey struct{}
type delegateSnapshot struct {
	source   string
	dir      string
	baseline map[string]string
}

// One immutable view per sibling batch: children share fork bytes and hashes,
// while each receives a private writable copy. Explicit directories bypass it.
func (c *Core) snapshotDelegateBatch(ctx context.Context, parent Task, requests []delegateRequest) (context.Context, func(), error) {
	count := 0
	for _, r := range requests {
		if r.Isolate && r.WorkDir == "" {
			count++
		}
	}
	if parent.Project == "" || count < 2 {
		return ctx, func() {}, nil
	}
	source, err := c.childWorkspaceSource(ctx, parent.TaskID)
	if err != nil {
		return ctx, nil, err
	}
	finishSource, cleanupSource, err := c.workspaceSourceLocks(ctx, parent, source)
	if err != nil {
		// The source-lock acquisition yielded the parent's workspace keys.
		// A failed acquisition cannot let its caller continue running unlocked.
		c.cancelRunning(parent.TaskID)
		return ctx, nil, err
	}
	defer cleanupSource()
	dir, err := os.MkdirTemp("", "panda-delegate-source-*")
	if err != nil {
		resumeErr := finishSource()
		if resumeErr != nil {
			c.cancelRunning(parent.TaskID)
		}
		return ctx, nil, errors.Join(err, resumeErr)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	var baseline map[string]string
	snapshotErr := c.copyWorkTree(source, dir)
	if snapshotErr == nil {
		baseline, snapshotErr = c.hashWorkTree(dir)
	}
	resumeErr := finishSource()
	if resumeErr != nil {
		c.cancelRunning(parent.TaskID)
	}
	if err := errors.Join(snapshotErr, resumeErr); err != nil {
		cleanup()
		return ctx, nil, fmt.Errorf("snapshot sibling source: %w", err)
	}
	return context.WithValue(ctx, delegateSnapshotKey{}, &delegateSnapshot{source: source, dir: dir, baseline: baseline}), cleanup, nil
}

// childWorkspaceSource resolves the actual parent attempt workspace. Project
// configuration may point at a different checkout from an isolated/stage run.
func (c *Core) childWorkspaceSource(ctx context.Context, parentID string) (string, error) {
	p, err := c.store.Get(ctx, parentID)
	if err != nil {
		return "", err
	}
	if p.ExecWorkDir != "" {
		return p.ExecWorkDir, nil
	}
	if p.WorkDir != "" {
		return p.WorkDir, nil
	}
	if p.Project != "" {
		return c.projectWorkDir(p.Project)
	}
	return "", nil
}

func (c *Core) attachChildWorkspace(ctx context.Context, p *bus.TaskDelegatePayload, child Task) error {
	c.attachProjectMemory(ctx, p, child.Project)
	source, err := c.childWorkspaceSource(ctx, child.ParentID)
	if err != nil {
		return err
	}
	if snapshot, _ := ctx.Value(delegateSnapshotKey{}).(*delegateSnapshot); snapshot != nil {
		source = snapshot.dir
	}
	if source == "" {
		return nil
	}
	if !notHomeOrRoot(source) {
		return fmt.Errorf("parent workspace is too broad")
	}
	var spec struct {
		Grant ChildGrant `json:"child_grant"`
	}
	_ = json.Unmarshal([]byte(child.SpecJSON), &spec)
	if spec.Grant.InputVersion != "" {
		current, err := c.childWorkspaceVersion(ctx, child.ParentID)
		if err != nil {
			return err
		}
		if current != spec.Grant.InputVersion {
			return fmt.Errorf("child input version changed before dispatch")
		}
	}
	ref, err := c.packProjectTree(ctx, child.TaskID, source)
	if err != nil {
		return err
	}
	if ref.Hash == "" {
		return fmt.Errorf("parent workspace artifact unavailable")
	}
	p.Inputs = []bus.ArtifactRef{ref}
	if child.Project == "" {
		p.ContextType = "file"
	}
	return c.store.SetStageInputs(ctx, child.TaskID, p.Inputs)
}

func (c *Core) seedDelegateSnapshot(ctx context.Context, child Task, in TaskInput) error {
	snapshot, _ := ctx.Value(delegateSnapshotKey{}).(*delegateSnapshot)
	if snapshot == nil || !in.Isolate || in.WorkDir != "" {
		return nil
	}
	source, err := c.childWorkspaceSource(ctx, child.ParentID)
	if err != nil {
		return err
	}
	if source != snapshot.source {
		return fmt.Errorf("sibling source changed during dispatch")
	}
	dir, err := c.attachedWorkDir(child.TaskID)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, isolatedBaselineFile)); err == nil {
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if err := c.copyWorkTree(snapshot.dir, dir); err != nil {
		return err
	}
	return writeIsolatedBaseline(dir, snapshot.baseline)
}

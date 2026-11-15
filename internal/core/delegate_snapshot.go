// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	source, err := c.projectWorkDir(parent.Project)
	if err != nil {
		return ctx, nil, err
	}
	dir, err := os.MkdirTemp("", "panda-delegate-source-*")
	if err != nil {
		return ctx, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err := c.copyWorkTree(source, dir); err != nil {
		cleanup()
		return ctx, nil, fmt.Errorf("snapshot sibling source: %w", err)
	}
	baseline, err := c.hashWorkTree(dir)
	if err != nil {
		cleanup()
		return ctx, nil, err
	}
	return context.WithValue(ctx, delegateSnapshotKey{}, &delegateSnapshot{source: source, dir: dir, baseline: baseline}), cleanup, nil
}

func (c *Core) seedDelegateSnapshot(ctx context.Context, child Task, in TaskInput) error {
	snapshot, _ := ctx.Value(delegateSnapshotKey{}).(*delegateSnapshot)
	if snapshot == nil || !in.Isolate || in.WorkDir != "" {
		return nil
	}
	source, err := c.projectWorkDir(in.Project)
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

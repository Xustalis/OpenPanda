// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"

	"github.com/Xustalis/OpenPanda/internal/core"
)

// RejudgeReviewed asks the reviewing model again about work already stored on a
// review-parked task. The panel runs this under a detached, cancellable
// operation; a verdict that accepts the work goes on to the ordinary accept
// path, so the approval receipt and peer mirroring stay in one place.
func (e *Engine) RejudgeReviewed(ctx context.Context, taskID string) *Result {
	if e == nil || e.db == nil || e.sched.Load() == nil {
		return &Result{Kind: "task", TaskID: taskID, TaskState: core.StateReview, Stderr: "task execution engine is unavailable", ExitCode: 1}
	}
	sched := e.sched.Load()
	task, result, err := sched.RejudgeReviewed(ctx, taskID)
	if err != nil {
		if current, getErr := sched.TaskStore().Get(context.WithoutCancel(ctx), taskID); getErr == nil {
			task = current
		}
		return &Result{Kind: "task", TaskID: taskID, TaskTitle: task.Title, TaskState: task.State, Stderr: err.Error(), ExitCode: 1}
	}
	return resultFromTask(task, result, routeFallbackPeer(ctx, sched.TaskStore(), taskID))
}

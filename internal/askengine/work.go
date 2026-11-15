// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"fmt"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/plan"
)

// EnqueueWork preserves the normal operation-approval boundary while publishing
// a durable business request. Assignment is never operation consent.
func (e *Engine) EnqueueWork(ctx context.Context, req core.ExecutionRequest, in core.TaskInput, q core.QueueSpec) (core.EnqueueResult, string, error) {
	sched := e.sched.Load()
	if sched == nil {
		return core.EnqueueResult{}, "", fmt.Errorf("task creation requires a capability card")
	}
	v := e.approvalFor(in.SessionID, in.Project)
	authorized, denied := v.gate(in.Authorized)
	if denied {
		return core.EnqueueResult{}, "", fmt.Errorf("%w (scope: %s)", ErrApprovalDenied, v.decisionScope)
	}
	in.Authorized = authorized
	r, err := sched.EnqueueRequest(ctx, req, in, q)
	if err != nil {
		return r, "", err
	}
	if !e.queueConsumerAlive(ctx) {
		return r, i18n.T(e.Locale(), "cli.queue.noConsumer"), nil
	}
	return r, "", nil
}

// StartWorkPlan applies the same operation authorization as a single task.
// The business approval mode concerns final actions and never grants this gate.
func (e *Engine) StartWorkPlan(ctx context.Context, req core.ExecutionRequest, p plan.Plan, q core.QueueSpec) (core.PlanRequestResult, string, error) {
	sched := e.sched.Load()
	if sched == nil {
		return core.PlanRequestResult{}, "", fmt.Errorf("task creation requires a capability card")
	}
	v := e.approvalFor(q.SessionID, q.Project)
	authorized, denied := v.gate(q.Authorized)
	if denied {
		return core.PlanRequestResult{}, "", fmt.Errorf("%w (scope: %s)", ErrApprovalDenied, v.decisionScope)
	}
	q.Authorized = authorized
	r, err := sched.StartPlanRequest(ctx, req, p, q)
	if err != nil {
		return r, "", err
	}
	if !e.queueConsumerAlive(ctx) {
		return r, i18n.T(e.Locale(), "cli.queue.noConsumer"), nil
	}
	return r, "", nil
}

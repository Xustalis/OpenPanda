// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"github.com/Xustalis/OpenPanda/internal/plan"
)

type PlanRequestResult struct {
	PlanID         string `json:"planId"`
	RequestID      string `json:"requestId"`
	AlreadyStarted bool   `json:"alreadyStarted"`
}

type planRequestPayload struct {
	Plan  plan.Plan
	Queue QueueSpec
}

// StartPlanRequest binds every stage and the durable business request in the
// publication transaction. Source IO and Plan validation happen outside it.
func (c *Core) StartPlanRequest(ctx context.Context, req ExecutionRequest, p plan.Plan, q QueueSpec) (PlanRequestResult, error) {
	if req.ConnectionID != "" {
		// Business preparation is currently a single bounded operation. A Plan
		// may carry arbitrary stage contracts, pins and mesh routes; reject it
		// until those can enforce the same scoped operation authorization.
		return PlanRequestResult{}, errors.New("remote business Plans require a constrained Plan executor")
	}
	identityQueue := q
	identityQueue.SessionID = ""
	r, err := c.store.reserveExecution(ctx, req, planRequestPayload{p, identityQueue}, "plan", planRequestPayload{p, q})
	if err != nil {
		return PlanRequestResult{}, err
	}
	if r.State == "published" {
		if r.PlanID == "" {
			return PlanRequestResult{}, ErrConflict
		}
		stages, err := c.store.PlanStages(ctx, r.PlanID)
		if err == nil && len(stages) == 0 {
			err = ErrExecutionUnavailable
		}
		return PlanRequestResult{PlanID: r.PlanID, RequestID: r.RequestID, AlreadyStarted: true}, err
	}
	if r.State != "preparing" {
		return PlanRequestResult{}, ErrExecutionUnavailable
	}
	id, existing, err := c.startPlan(ctx, p, q, &r.ExecutionRequest)
	if errors.Is(err, ErrConflict) {
		if _, recordErr := c.db.ExecContext(ctx, `UPDATE execution_requests SET recovery_allowed=0,preparation_error=? WHERE request_id=? AND state='preparing'`, err.Error(), r.RequestID); recordErr != nil {
			err = errors.Join(err, recordErr)
		}
	}
	return PlanRequestResult{PlanID: id, RequestID: r.RequestID, AlreadyStarted: existing}, err
}

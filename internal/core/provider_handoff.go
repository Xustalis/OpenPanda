// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
)

type peerFallbackKey struct{}

type providerHandoff struct {
	task     Task
	decision scheduler.Decision
	reason   string
}

func (h *providerHandoff) Error() string { return h.reason }

// Only the origin can move untouched work. Already-retried tasks may have
// side effects from an earlier attempt even if this attempt never started.
func (c *Core) providerFallbackPeer(ctx context.Context, t Task, reason string) *providerHandoff {
	if t.Remote || len(t.Chain) > 1 || targetNodeOf(t) != "" || len(t.Requires) == 0 || (t.WorkDir != "" && t.ContextType != "file") {
		return nil
	}
	if n, err := c.store.RetryCount(ctx, t.TaskID); err != nil || n != 0 {
		return nil
	}
	declined, err := c.store.DeclinedBy(ctx, t.TaskID)
	if err != nil {
		return nil
	}
	evicted, err := c.store.EvictedExecutors(ctx, t.TaskID)
	if err != nil {
		return nil
	}
	peers := c.eligibleForSend(ctx, needsForTask(t), c.onlineEmployees(ctx))
	roster := routingTaskSpec(t.SpecJSON).AllowAgents
	peers = slices.DeleteFunc(peers, func(n ledger.Node) bool {
		if scheduler.IsSelfRow(n.ID, c.nodeID) || !n.Matches(t.Requires) || !c.sendableTo(n.ID) {
			return true
		}
		if len(roster) == 0 {
			return false
		}
		for name := range n.Agents {
			if rosterIncludes(roster, name) {
				return false
			}
		}
		return true
	})
	decision := scheduler.RouteP(c.nodeID, append(declined, evicted...), peers, func([]string) bool { return false }, t.Requires, resourceRequirement(t.ResourceJSON), preferredNodeOf(t), t.Project)
	if decision.Action != scheduler.ActionForward {
		return nil
	}
	return &providerHandoff{task: t, decision: decision, reason: reason}
}

// The old worker has already released its leases and cancellation handler.
// Fence its attempt before delegating, without exposing a terminal failure to
// the plan scheduler between the local refusal and the remote acceptance.
func (c *Core) handoffUnstartedTask(ctx context.Context, h *providerHandoff) (bus.TaskResultPayload, error) {
	t := h.task
	aid, err := newUUID()
	if err != nil {
		return bus.TaskResultPayload{}, err
	}
	err = c.store.withTx(ctx, func(tx *sql.Tx) error {
		if err := checkConversationTaskTx(ctx, tx, t.TaskID); err != nil {
			return err
		}
		update, err := tx.ExecContext(ctx, "UPDATE tasks SET state=?,attempt_id=?,state_version=state_version+1,updated_at=? WHERE task_id=? AND attempt_id=? AND owner_node=? AND state=?", StateDispatched, aid, c.store.now(), t.TaskID, t.AttemptID, c.nodeID, StateRunning)
		if err != nil {
			return err
		}
		if n, err := update.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return ErrCancelled
		}
		return c.store.recordEventTx(ctx, tx, t.TaskID, EvRetry, map[string]any{"reason": h.reason, "target": h.decision.Target, "attempt_id": aid})
	})
	if err != nil {
		return bus.TaskResultPayload{}, err
	}
	t.AttemptID, t.State = aid, StateDispatched
	w := c.waiters.add(t.TaskID)
	defer c.waiters.remove(t.TaskID, w)
	if !c.forwardClaimTo(ctx, t, t.Chain, h.decision, false) {
		err = fmt.Errorf("local provider unavailable (%s); could not transfer work to %s", h.reason, h.decision.Target)
		c.failLocal(ctx, t.TaskID, err, aid)
		return bus.TaskResultPayload{}, err
	}
	if t.Scheduled {
		return bus.TaskResultPayload{TaskID: t.TaskID, AttemptID: aid, State: StateDispatched}, nil
	}
	_, result, err := c.waitRemoteResult(ctx, t, w.ch, "provider handoff")
	return result, err
}

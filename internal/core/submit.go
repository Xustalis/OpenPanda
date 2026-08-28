package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
)

// TaskInput is the local-entry form of a task: the structured task the entry
// model emitted, translated into the fields the core persists and executes. It
// mirrors the wire TaskDelegatePayload but originates locally rather than over
// the bus. The caller (cmd/panda) owns the translation from entry.TaskSpec.
type TaskInput struct {
	Title         string
	Project       string
	ContextType   string
	ContextHash   string // pre-packed snapshot hash; empty means "pack if applicable"
	ContextLevel  string // pointer|summary|full; empty is derived by packContext
	RepoPath      string // file-type repo root for auto-packing (MVP: CLI not yet wired)
	Intent        string
	SpecJSON      string
	Requires      []string
	PreferredNode string // user-named node (task spec scope); routing prefers it when it can match
	Complexity    float64
	Risk          string
	ResourceJSON  string
	Authorized    bool // user consented to executing tier-2 (irreversible) commands
	// ClassifyKind carries the entry model's classification ("task") for trace
	// emission. Empty means the input did not come from a classified ask (a
	// direct CLI submission, a delegated peer task): no classify_result event
	// fires for those, matching the pre-trace behavior.
	ClassifyKind string
}

// detail folds the input into the persisted detail columns.
func (in TaskInput) detail() TaskDetail {
	return TaskDetail{
		ContextType:  in.ContextType,
		Intent:       in.Intent,
		SpecJSON:     in.SpecJSON,
		Complexity:   in.Complexity,
		Risk:         in.Risk,
		ResourceJSON: in.ResourceJSON,
		Requires:     in.Requires,
	}
}

// SubmitLocal creates and executes a task on this node — no delegation. It is
// the Phase 1 local closed loop: the entry model's task output lands here, is
// persisted with full detail, then runs through the same execute pipeline used
// for inbound delegated tasks. It returns the final task row and the execution
// result payload.
//
// On an execution error the task is failed (not left queued) so a local task
// always ends in a terminal state. ErrCancelled is not an error to the caller:
// the task was cancelled mid-run and the result payload reflects that outcome.
func (c *Core) SubmitLocal(ctx context.Context, in TaskInput) (Task, bus.TaskResultPayload, error) {
	t, _, _, err := c.createTask(ctx, in)
	if err != nil {
		return Task{}, bus.TaskResultPayload{}, fmt.Errorf("create task: %w", err)
	}
	return c.runLocal(ctx, t, in)
}

// Submit creates a task from the entry model and routes it to the best node:
// locally if this node matches, otherwise forwarded to a capable peer (P2P
// per-edge delegation). It is the root-scheduler entry point used by the ask
// CLI. A forwarded task blocks until the peer's result arrives (or ctx is
// done); a local task returns immediately with the execution result.
func (c *Core) Submit(ctx context.Context, in TaskInput) (Task, bus.TaskResultPayload, error) {
	t, hash, level, err := c.createTask(ctx, in)
	if err != nil {
		return Task{}, bus.TaskResultPayload{}, fmt.Errorf("create task: %w", err)
	}

	chain := []string{c.nodeID}
	employees := c.onlineEmployees(ctx)
	localMatch := c.localMatch()
	decision := scheduler.Route(c.nodeID, chain, employees, localMatch, in.Requires,
		resourceRequirement(in.ResourceJSON), in.PreferredNode)

	// — Trace: route decision with per-candidate score breakdown (orbit Step-2).
	// Build the same candidate set RouteAt uses (online + hardware fit +
	// capability match). Best-effort; any error is logged and the execution
	// path is unaffected.
	{
		req := resourceRequirement(in.ResourceJSON)
		seen := make(map[string]bool, len(chain))
		for _, n := range chain {
			seen[n] = true
		}
		now := time.Now().Unix()
		var capable []ledger.Node
		for _, n := range employees {
			if scheduler.IsSelfRow(n.ID, c.nodeID) {
				continue
			}
			if n.Status != "online" || seen[n.ID] {
				continue
			}
			if !n.Fits(req) {
				continue
			}
			if n.Matches(in.Requires) || len(in.Requires) == 0 {
				capable = append(capable, n)
			}
		}
		// Also route the self-node in so ScoreAllCandidates can apply localBias.
		for i := range employees {
			if scheduler.IsSelfRow(employees[i].ID, c.nodeID) {
				capable = append(capable, employees[i])
				break
			}
		}
		allCandidates := scheduler.ScoreAllCandidates(capable, c.nodeID, in.PreferredNode, now)
		// — Pick the breakdown that actually drove the decision so the orbit
		//    can explain "why this node". For ActionLocal the winner is the
		//    local self node (top of allCandidates due to localBias); for
		//    ActionForward it's decision.Target; decline leaves it nil (no
		//    winner, no score to explain).
		var chosenBreakdown any
		switch decision.Action {
		case scheduler.ActionLocal:
			for i := range allCandidates {
				if scheduler.IsSelfRow(allCandidates[i].NodeID, c.nodeID) {
					chosenBreakdown = allCandidates[i].Breakdown
					break
				}
			}
		case scheduler.ActionForward:
			for i := range allCandidates {
				if allCandidates[i].NodeID == decision.Target {
					chosenBreakdown = allCandidates[i].Breakdown
					break
				}
			}
		}
		c.EvTrace(ctx, t.TaskID, EvRouteDecision, map[string]any{
			"action":          decision.Action,
			"target_node":     decision.Target,
			"reason":          decision.Reason,
			"score_breakdown": chosenBreakdown,
			"self_id":         c.nodeID,
			"chain":           chain,
			"candidates":      allCandidates,
		})
	}

	switch decision.Action {
	case scheduler.ActionLocal:
		return c.runLocal(ctx, t, in)
	case scheduler.ActionForward:
		payload := bus.TaskDelegatePayload{
			TaskID:        t.TaskID,
			Project:       in.Project,
			Title:         in.Title,
			ContextType:   in.ContextType,
			ContextHash:   hash,
			ContextLevel:  level,
			Intent:        in.Intent,
			SpecJSON:      in.SpecJSON,
			Requires:      in.Requires,
			Chain:         chain,
			PreferredNode: in.PreferredNode,
			Complexity:    in.Complexity,
			Risk:          in.Risk,
			ResourceJSON:  in.ResourceJSON,
			AttemptID:     t.AttemptID,
			Authorized:    in.Authorized,
		}
		// Register a waiter so the inbound task_result unblocks this call.
		ch := make(chan bus.TaskResultPayload, 1)
		c.waiters.Store(t.TaskID, ch)
		defer c.waiters.Delete(t.TaskID)

		if err := c.forwardDelegated(ctx, t.TaskID, decision.Target, payload, chain); err != nil {
			return t, bus.TaskResultPayload{}, fmt.Errorf("forward: %w", err)
		}
		select {
		case res := <-ch:
			final, err := c.store.Get(ctx, t.TaskID)
			if err != nil {
				return t, res, err
			}
			return final, res, nil
		case <-time.After(defaultDelegateTimeout):
			// A dead target must not leave Submit blocked forever (D3): the same
			// default deadline forwardDelegated stamps on the lease bounds the
			// wait, after which the task is failed and an error returned.
			c.failLocal(ctx, t.TaskID, errors.New("delegation timeout"))
			return t, bus.TaskResultPayload{}, fmt.Errorf("delegation timeout waiting for %s", decision.Target)
		case <-ctx.Done():
			return t, bus.TaskResultPayload{}, ctx.Err()
		}
	default:
		return t, bus.TaskResultPayload{}, fmt.Errorf("no capability: %s", decision.Reason)
	}
}

// createTask persists a new local-origin task with full entry-model detail and
// packs its context snapshot. It returns the task plus the wire context fields
// (hash + level) so Submit can carry them on a forwarded delegate.
func (c *Core) createTask(ctx context.Context, in TaskInput) (Task, string, string, error) {
	t, err := c.store.Create(ctx, "", in.Project, in.Title, c.nodeID, []string{c.nodeID})
	if err != nil {
		return Task{}, "", "", err
	}
	// Persist the user's tier-2 consent as server-side state (design §16 / P0-1).
	// execute/run read it from the DB, so the wire payload never needs to carry it.
	if err := c.store.SetAuthorized(ctx, t.TaskID, in.Authorized); err != nil {
		return Task{}, "", "", fmt.Errorf("set authorized: %w", err)
	}
	hash, level, err := c.packContext(ctx, in)
	if err != nil {
		return Task{}, "", "", fmt.Errorf("pack context: %w", err)
	}
	d := in.detail()
	d.ContextHash = hash
	if err := c.store.SetDetail(ctx, t.TaskID, d); err != nil {
		return Task{}, "", "", fmt.Errorf("set detail: %w", err)
	}
	// — Trace: the entry model's classification, fired at task creation —
	// before Submit's routing decision, before the queue scheduler claims an
	// enqueued task, before anything runs. The orbit's Step-1 (意图分类) must
	// lead the timeline, not trail the result it was supposed to explain
	// (design doc §3.1.1: kind/note/stages_count; a single task is one stage).
	if in.ClassifyKind != "" {
		c.EvTrace(ctx, t.TaskID, EvClassifyResult, map[string]any{
			"kind":         in.ClassifyKind,
			"note":         in.Title,
			"stages_count": 1,
		})
	}
	return t, hash, level, nil
}

// runLocal executes a task on this node and returns the final row + result.
// It is the shared local branch for both SubmitLocal and Submit's local route.
func (c *Core) runLocal(ctx context.Context, t Task, in TaskInput) (Task, bus.TaskResultPayload, error) {
	result, err := c.execute(ctx, t.TaskID, in.Intent, in.Requires)
	return c.retryLoop(ctx, t.TaskID, in.Intent, in.Requires, result, err)
}

// ResumeApproved grants tier-2 consent to a task parked in review by an
// authorization refusal and re-runs it synchronously on this node, returning
// the final row and result. It is the inline approval closure: when the user
// approves a tier-2 agent at the ask/repl prompt, the task that just refused
// runs to completion in the same round-trip — no background scheduler required
// (the ask/repl process runs none). This is the cost-efficient path: the tier-2
// refusal is raised before the agent subprocess ever spawns, so the first
// Submit spent no agent tokens and the resume is the sole execution.
//
// The task must be in review; its intent and requirements are read from the
// persisted row (the entry model's distilled intent, plus the appended user
// prompt, both stored at submit). A concurrent scheduler claiming the
// just-approved task is tolerated: Dispatch loses the CAS and we report the
// current row rather than double-running.
func (c *Core) ResumeApproved(ctx context.Context, taskID string) (Task, bus.TaskResultPayload, error) {
	cur, err := c.store.Get(ctx, taskID)
	if err != nil {
		return Task{}, bus.TaskResultPayload{}, err
	}
	if cur.State != StateReview {
		return cur, bus.TaskResultPayload{}, fmt.Errorf("resume approved: task %s state=%s, want %s", taskID, cur.State, StateReview)
	}
	// Approve grants consent (authorized=1) and moves review -> queued.
	if err := c.store.Approve(ctx, taskID); err != nil {
		return cur, bus.TaskResultPayload{}, fmt.Errorf("approve: %w", err)
	}
	// The parking already reset the retry budget; keep it fresh for this run.
	c.reviewReset(taskID)
	if err := c.store.Dispatch(ctx, taskID, c.nodeID, c.nodeID); err != nil {
		// A queue scheduler sharing this store may have claimed the queued row
		// first; leave the run to it and report the current state.
		final, gerr := c.store.Get(ctx, taskID)
		if gerr != nil {
			return cur, bus.TaskResultPayload{}, gerr
		}
		return final, bus.TaskResultPayload{}, nil
	}
	result, err := c.run(ctx, taskID, cur.Intent, cur.Requires)
	return c.retryLoop(ctx, taskID, cur.Intent, cur.Requires, result, err)
}

// retryLoop drives a task from its first execution outcome to a stable state:
// a failed task is retried (with a fresh attempt) up to the loop detector's
// budget; past that it is paused into review for human analysis rather than
// left in failed or retried forever (design §14.2 signal C, plan P2-18). It is
// shared by the synchronous Submit paths and the queue scheduler's runner.
func (c *Core) retryLoop(ctx context.Context, taskID, intent string, required []string, result bus.TaskResultPayload, err error) (Task, bus.TaskResultPayload, error) {
	if err != nil && !errors.Is(err, ErrCancelled) {
		c.failLocal(ctx, taskID, err)
		final, gerr := c.store.Get(ctx, taskID)
		if gerr != nil {
			return Task{}, result, gerr
		}
		return final, result, err
	}

	retries := 0
	for {
		final, err := c.store.Get(ctx, taskID)
		if err != nil {
			return Task{}, result, err
		}
		if final.State != StateFailed {
			// The task settled without needing (another) retry. Drop its failure
			// count: keeping it would both leak one map entry per task for the
			// daemon's lifetime and charge a future execution of this id for
			// attempts that have already been resolved.
			c.loop.Reset(taskID)
			return final, result, nil
		}
		// A tier-2 authorization refusal is deterministic policy, not a
		// failed attempt: retrying cannot produce consent, so the task goes
		// straight to review with the actionable reason instead of burning
		// the retry budget (and re-spawning nothing) first.
		if commander.IsAuthorizationRefusal(result.Stderr) {
			if rerr := c.store.Review(ctx, taskID, c.nodeID, result.Stderr); rerr != nil {
				c.logger.Warn("review task", "task", taskID, "err", rerr)
			}
			c.reviewReset(taskID)
			final, err = c.store.Get(ctx, taskID)
			if err != nil {
				return Task{}, result, err
			}
			return final, result, nil
		}
		if !c.loop.Allow(taskID) {
			if rerr := c.store.Review(ctx, taskID, c.nodeID, result.Stderr); rerr != nil {
				c.logger.Warn("review task", "task", taskID, "err", rerr)
			}
			c.reviewReset(taskID)
			// Re-fetch so the returned row reflects the review transition.
			final, err = c.store.Get(ctx, taskID)
			if err != nil {
				return Task{}, result, err
			}
			return final, result, nil
		}
		// Exponential backoff between retries so a deterministically-failing
		// task does not hammer the agent/LLM in a tight loop. The loop detector
		// still bounds the total retry count.
		if c.sleep != nil {
			c.sleep(c.retryBackoff << uint(retries))
		}
		if rerr := c.retryOnce(ctx, taskID); rerr != nil {
			c.logger.Warn("retry task", "task", taskID, "err", rerr)
			return final, result, rerr
		}
		retries++
		c.logger.Info("retrying task", "task", taskID)
		result, err = c.run(ctx, taskID, intent, required)
		if err != nil && !errors.Is(err, ErrCancelled) {
			c.failLocal(ctx, taskID, err)
			final, gerr := c.store.Get(ctx, taskID)
			if gerr != nil {
				return Task{}, result, gerr
			}
			return final, result, err
		}
	}
}

// reviewReset clears a parked task's failure count. A task in review is waiting
// on a person, and the only ways out of that state are deliberate human
// transitions (approve, reject, cancel, or send it back to the queue). Whichever
// they pick, the next execution of this id must start with its full retry budget:
// without this, a task the human sends back is already out of retries and parks
// itself again on its first failure — the review round-trip accomplishes nothing.
// The in-loop counter is what bounds an automatic retry storm, and it keeps
// accumulating for as long as the loop actually runs.
func (c *Core) reviewReset(taskID string) { c.loop.Reset(taskID) }

// retryOnce rotates the attempt and returns a failed task to the queue,
// dispatched back to this node, so run() can accept and re-execute it.
func (c *Core) retryOnce(ctx context.Context, taskID string) error {
	if _, err := c.store.RotateAttempt(ctx, taskID, c.nodeID); err != nil {
		return fmt.Errorf("rotate attempt: %w", err)
	}
	if err := c.store.Requeue(ctx, taskID, c.nodeID); err != nil {
		return fmt.Errorf("requeue: %w", err)
	}
	return c.store.Dispatch(ctx, taskID, c.nodeID, c.nodeID)
}

// failLocal force-fails a task left in a non-terminal state by a routing or
// dispatch error, so the queue reflects reality.
func (c *Core) failLocal(ctx context.Context, taskID string, err error) {
	if ferr := c.store.ForceFail(ctx, taskID, err.Error()); ferr != nil {
		c.logger.Warn("fail local task", "task", taskID, "err", ferr)
	}
}

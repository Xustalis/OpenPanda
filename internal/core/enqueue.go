package core

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
	"github.com/Xustalis/OpenPanda/internal/scheduler/queue"
)

// QueueSpec carries the queue-scheduling metadata for an Enqueued task.
// Priority must be one of PriorityHigh/Normal/Low; use DefaultQueueSpec for
// the normal-priority defaults so a zero struct is never submitted by
// accident (PriorityHigh is 0).
type QueueSpec struct {
	Priority     int
	SessionID    string
	WorkDir      string
	ResourceKeys []string
}

// DefaultQueueSpec returns a spec with normal priority and no session/
// resource bindings.
func DefaultQueueSpec() QueueSpec {
	return QueueSpec{Priority: PriorityNormal}
}

// Enqueue creates a task and hands it to the local queue scheduler instead of
// executing it inline: the task lands in queued state (marked scheduled) and
// returns immediately; the scheduler starts it when its resources are free
// and an execution slot is available. It is the panel's task-submission path
// (queue redesign), contrasting Submit/SubmitLocal which block until done.
func (c *Core) Enqueue(ctx context.Context, in TaskInput, q QueueSpec) (Task, error) {
	if q.Priority < PriorityHigh || q.Priority > PriorityLow {
		return Task{}, fmt.Errorf("queue priority %d out of range", q.Priority)
	}
	t, _, _, err := c.createTask(ctx, in)
	if err != nil {
		return Task{}, fmt.Errorf("create task: %w", err)
	}
	if err := c.store.SetQueueMeta(ctx, t.TaskID, q.Priority, q.SessionID, q.WorkDir, q.ResourceKeys); err != nil {
		return Task{}, fmt.Errorf("set queue meta: %w", err)
	}
	if err := c.store.Queue(ctx, t.TaskID, c.nodeID); err != nil {
		return Task{}, fmt.Errorf("queue task: %w", err)
	}
	t.Priority = q.Priority
	t.SessionID = q.SessionID
	t.WorkDir = q.WorkDir
	t.ResourceKeys = q.ResourceKeys
	t.Scheduled = true
	t.State = StateQueued
	c.queueWake()
	c.logger.Info("task enqueued", "task", t.TaskID, "priority", q.Priority)
	return t, nil
}

// queueWake nudges the queue scheduler if one is running.
func (c *Core) queueWake() {
	c.mu.RLock()
	s := c.queueSched
	c.mu.RUnlock()
	if s != nil {
		s.Wake()
	}
}

// StartQueueScheduler starts the node-local queue scheduler loop: it adopts
// queued-and-scheduled tasks in policy order (drag seq → priority → FIFO),
// enforcing resource conflicts and the card's MaxConcurrent budget. Idempotent
// within one Core; runs until ctx ends.
func (c *Core) StartQueueScheduler(ctx context.Context) *queue.Scheduler {
	max := c.card.Capacity.MaxConcurrent
	if max < 1 {
		max = 1
	}
	s := queue.New(queueStoreAdapter{c: c}, queueRunnerAdapter{c: c}, max, c.logger)
	c.mu.Lock()
	c.queueSched = s
	c.mu.Unlock()
	go s.Run(ctx)
	// A previous process instance may have left claimed/queued tasks behind;
	// nudge once so adoption happens without waiting for the next event.
	s.Wake()
	return s
}

// QueueScheduler exposes the running scheduler (nil before
// StartQueueScheduler). Used by tests and diagnostics.
func (c *Core) QueueScheduler() *queue.Scheduler {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.queueSched
}

// queueStoreAdapter implements queue.Store on the core's task store.
type queueStoreAdapter struct{ c *Core }

func (a queueStoreAdapter) ListReady(ctx context.Context) ([]queue.ReadyTask, error) {
	tasks, err := a.c.store.ListReady(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]queue.ReadyTask, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, queue.ReadyTask{
			ID:           t.TaskID,
			Project:      t.Project,
			Priority:     t.Priority,
			Seq:          t.Seq,
			CreatedAt:    t.CreatedAt,
			ResourceKeys: t.ResourceKeys,
		})
	}
	return out, nil
}

func (a queueStoreAdapter) CountActive(ctx context.Context) (int, error) {
	return a.c.store.CountScheduledActive(ctx)
}

func (a queueStoreAdapter) Claim(ctx context.Context, taskID string) error {
	return a.c.store.ClaimLocal(ctx, taskID, a.c.nodeID)
}

// queueRunnerAdapter implements queue.Runner: run one claimed task to a
// terminal state.
type queueRunnerAdapter struct{ c *Core }

func (a queueRunnerAdapter) Run(ctx context.Context, taskID string) {
	a.c.runScheduled(ctx, taskID)
}

// runScheduled executes a task the queue scheduler claimed (queued ->
// dispatched already done by ClaimLocal): accept it into running and drive
// the shared retry/review loop. Intent/requires come from the persisted
// detail so the scheduler needs no out-of-band state.
func (c *Core) runScheduled(ctx context.Context, taskID string) {
	t, err := c.store.Get(ctx, taskID)
	if err != nil {
		c.logger.Warn("queue: load claimed task", "task", taskID, "err", err)
		return
	}
	// Cross-device routing: when this node cannot serve the task's required
	// abilities, hand it to a capable peer — the same root-scheduler policy
	// Submit applies (design §2.4). The claim is re-targeted to the peer and
	// its task_result completes the local row via handleResult, so there is
	// nothing left to run here. Queued tasks are first-class network
	// citizens, not local-only work.
	if c.forwardScheduled(ctx, t) {
		return
	}
	result, err := c.run(ctx, taskID, t.Intent, t.Requires)
	final, _, rerr := c.retryLoop(ctx, taskID, t.Intent, t.Requires, result, err)
	if rerr != nil && !errors.Is(rerr, ErrCancelled) {
		c.logger.Warn("queue: task run error", "task", taskID, "err", rerr)
		return
	}
	c.logger.Info("queue: task finished", "task", taskID, "state", final.State)
	// A finished stage may have released its successors. The plan node is the one
	// that decides, and for a locally-executed stage that is this node.
	c.advanceStagePlan(ctx, final)
	// Unblock any synchronous waiter (delegation-style waits on enqueued
	// tasks); a no-op when nobody is waiting.
	c.signalResult(taskID, result)
}

// forwardScheduled routes a claimed queue task to the node that should run it.
// It returns true when the task was handed off (the result arrives
// asynchronously via handleResult); false means the caller should proceed with
// local execution — either this node won the routing decision, or nobody did
// (the local run then fails with the standard capability error).
//
// The decision is scheduler.Route's, not this function's. It used to be "if I
// can do it, I do it", which is the short-circuit RouteAt's doc comment says was
// removed in v0.0.6 because it makes load balancing impossible by construction —
// but it was removed from Route only, and this is the path every plan stage and
// every panel-submitted task takes. Asking here meant a stage needing a GPU ran
// on the Orange Pi whenever the Pi happened to hold the ability, and a burst of
// queued tasks all stayed on the node that accepted them while idle peers
// watched. Both are the exact cases the hardware filter and the score exist for.
func (c *Core) forwardScheduled(ctx context.Context, t Task) bool {
	// A task pinned to a directory on this machine is local work by definition:
	// the delegate payload carries no work dir (each executor derives its own),
	// so a forwarded copy would run against a different tree.
	if t.WorkDir != "" {
		return false
	}
	chain := t.Chain
	if len(chain) == 0 {
		chain = []string{c.nodeID}
	}
	// Loop safety, same as rerouteDeclined (P1-5): exclude every node that
	// already declined this task so a re-queued task is not handed straight
	// back to its last decliner.
	excluded, err := c.store.DeclinedBy(ctx, t.TaskID)
	if err != nil {
		c.logger.Warn("queue forward: declined-by", "task", t.TaskID, "err", err)
	}
	seenChain := append(slices.Clone(chain), excluded...)
	decision := scheduler.Route(c.nodeID, seenChain, c.onlineEmployees(ctx), c.localMatch(), t.Requires,
		resourceRequirement(t.ResourceJSON), "")
	if decision.Action != scheduler.ActionForward {
		c.logger.Info("queue: no peer for task", "task", t.TaskID,
			"action", string(decision.Action), "reason", decision.Reason)
		return false
	}
	p := bus.TaskDelegatePayload{
		TaskID:       t.TaskID,
		ParentID:     t.ParentID,
		Project:      t.Project,
		Title:        t.Title,
		ContextType:  t.ContextType,
		ContextHash:  t.ContextHash,
		Intent:       t.Intent,
		SpecJSON:     t.SpecJSON,
		Requires:     t.Requires,
		Chain:        chain,
		Complexity:   t.Complexity,
		Risk:         t.Risk,
		ResourceJSON: t.ResourceJSON,
		AttemptID:    t.AttemptID,
		Authorized:   t.Authorized,
		// A stage travels with its plan identity and its inputs, so the executor
		// can pull the trees its predecessors produced. Empty on a plain task.
		PlanID:  t.PlanID,
		StageID: t.StageID,
		Inputs:  t.Inputs,
	}
	if err := c.sendClaimedDelegate(ctx, t.TaskID, decision.Target, p); err != nil {
		c.logger.Warn("queue: forward to peer failed", "task", t.TaskID,
			"target", decision.Target, "err", err)
		return false
	}
	c.logger.Info("queue: task forwarded to peer", "task", t.TaskID, "target", decision.Target)
	return true
}

// sendClaimedDelegate re-targets an already-claimed (dispatched-to-self) task
// to target and sends the delegate envelope. Unlike dispatchDelegated it does
// not transition state — the queue claim already moved the task to dispatched —
// it records the new delegation target on the audit trail (so isCurrentExecutor
// authenticates the peer's result/decline) and stamps a lease so a dead
// executor is detected (D3).
func (c *Core) sendClaimedDelegate(ctx context.Context, taskID, target string, p bus.TaskDelegatePayload) error {
	if err := c.store.RetargetDelegation(ctx, taskID, target); err != nil {
		return fmt.Errorf("retarget: %w", err)
	}
	timeoutMS := p.TimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = c.lease().Milliseconds()
		p.TimeoutMS = timeoutMS
	}
	if err := c.store.SetLease(ctx, taskID, timeoutMS); err != nil {
		return fmt.Errorf("set lease: %w", err)
	}
	msgID, err := newUUID()
	if err != nil {
		return err
	}
	env, err := bus.NewEnvelope(bus.MsgTaskDelegate, c.nodeID, msgID, p)
	if err != nil {
		return err
	}
	env.To = target
	if err := c.sendTo(target, env); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	return nil
}

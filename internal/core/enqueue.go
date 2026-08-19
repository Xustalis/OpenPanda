package core

import (
	"context"
	"errors"
	"fmt"

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
	result, err := c.run(ctx, taskID, t.Intent, t.Requires)
	final, _, rerr := c.retryLoop(ctx, taskID, t.Intent, t.Requires, result, err)
	if rerr != nil && !errors.Is(rerr, ErrCancelled) {
		c.logger.Warn("queue: task run error", "task", taskID, "err", rerr)
		return
	}
	c.logger.Info("queue: task finished", "task", taskID, "state", final.State)
	// Unblock any synchronous waiter (delegation-style waits on enqueued
	// tasks); a no-op when nobody is waiting.
	c.signalResult(taskID, result)
}

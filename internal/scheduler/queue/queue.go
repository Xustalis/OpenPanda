// Package queue implements the node-local task queue scheduler: it decides
// which queued task runs next on this node. It is the temporal counterpart of
// the parent scheduler package (which decides WHICH node a task runs on) and
// is deliberately decoupled from internal/core: it only sees the Store and
// Runner interfaces below, so a future multi-node deployment can swap in a
// distributed lock table and a cluster-wide store without touching the
// ordering policy.
//
// Scheduling semantics (deterministic rules, no learning — personal-scale
// system decision):
//   - ordering: manual drag seq (seq>0, ascending) first, then priority
//     (ascending), then FIFO by creation time;
//   - concurrency: a task starts only when none of its resource keys is held
//     by a running task AND the node's execution budget has a free slot, so
//     resource-disjoint tasks run in parallel while same-resource tasks queue.
package queue

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// ReadyTask is the scheduler's view of one queued task — just the fields the
// ordering policy and the resource registry need.
type ReadyTask struct {
	ID           string
	Project      string
	Priority     int
	Seq          int64
	CreatedAt    int64
	ResourceKeys []string
}

// Store is the persistence surface the scheduler needs. Implemented by the
// core's task store (via a thin adapter), kept here as an interface so the
// package never imports core.
type Store interface {
	// ListReady returns every task waiting for the local scheduler.
	ListReady(ctx context.Context) ([]ReadyTask, error)
	// CountActive returns how many execution slots are currently occupied.
	CountActive(ctx context.Context) (int, error)
	// Claim atomically moves a task from queued to claimed-by-caller. It must
	// fail (not block) when another scheduler instance won the race.
	Claim(ctx context.Context, taskID string) error
}

// Runner executes one claimed task to a terminal state. It is called on its
// own goroutine; returning releases the task's resource locks.
type Runner interface {
	Run(ctx context.Context, taskID string)
}

// DefaultResourceKey is the conservative lock taken by a task that declares
// neither explicit resource keys nor a project: two anonymous agent tasks may
// otherwise trample each other's working directory, so they serialize.
const DefaultResourceKey = "agent:*"

// DefaultKeys fills in the resource keys a task occupies when it declared
// none: project tasks lock their project (same project shares a worktree and
// must serialize), everything else falls back to the shared agent lock.
func DefaultKeys(keys []string, project string) []string {
	if len(keys) > 0 {
		return keys
	}
	if project != "" {
		return []string{"project:" + project}
	}
	return []string{DefaultResourceKey}
}

// sortReady orders the ready set by the scheduling policy: explicit drag
// order first (seq>0, ascending), then unsequenced tasks by priority and FIFO.
func sortReady(ts []ReadyTask) {
	sort.SliceStable(ts, func(i, j int) bool {
		a, b := ts[i], ts[j]
		if (a.Seq > 0) != (b.Seq > 0) {
			return a.Seq > 0 // dragged tasks jump ahead of pure FIFO ones
		}
		if a.Seq > 0 && b.Seq > 0 && a.Seq != b.Seq {
			return a.Seq < b.Seq
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		return a.CreatedAt < b.CreatedAt
	})
}

// ResourceRegistry is the resource lock table. A task may hold several keys
// atomically (all-or-nothing) so a task needing two resources never deadlocks
// half-acquired against another task wanting them in reverse order.
type ResourceRegistry struct {
	mu     sync.Mutex
	holder map[string]string   // resource key -> holding task id
	byTask map[string][]string // task id -> held keys (for Release)
}

// NewResourceRegistry returns an empty lock table.
func NewResourceRegistry() *ResourceRegistry {
	return &ResourceRegistry{
		holder: make(map[string]string),
		byTask: make(map[string][]string),
	}
}

// TryAcquire locks every key for taskID if none of them is held by another
// task (a key held by taskID itself is not a conflict). It returns false and
// locks nothing on any conflict.
func (r *ResourceRegistry) TryAcquire(keys []string, taskID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range keys {
		if h, ok := r.holder[k]; ok && h != taskID {
			return false
		}
	}
	for _, k := range keys {
		r.holder[k] = taskID
	}
	// Merge with any keys the task already holds so a later Release drops the
	// union — a re-acquire must never leak the earlier locks.
	held := make(map[string]bool, len(keys))
	for _, k := range r.byTask[taskID] {
		held[k] = true
	}
	for _, k := range keys {
		held[k] = true
	}
	merged := make([]string, 0, len(held))
	for k := range held {
		merged = append(merged, k)
	}
	r.byTask[taskID] = merged
	return true
}

// Release drops every lock held by taskID. Releasing an unknown task is a
// no-op, so double-release (defensive cleanup paths) is safe.
func (r *ResourceRegistry) Release(taskID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range r.byTask[taskID] {
		if r.holder[k] == taskID {
			delete(r.holder, k)
		}
	}
	delete(r.byTask, taskID)
}

// HeldBy reports the task currently holding key ("" = free). Exposed for
// tests and diagnostics.
func (r *ResourceRegistry) HeldBy(key string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.holder[key]
}

// Scheduler pulls ready tasks in policy order and runs the ones whose
// resources are free, bounded by MaxConcurrent.
type Scheduler struct {
	store    Store
	runner   Runner
	registry *ResourceRegistry
	max      int
	logger   *slog.Logger

	// pollInterval bounds how long a wake-less event (e.g. a missed signal)
	// can stall the queue; wake provides the fast path.
	pollInterval time.Duration
	wake         chan struct{}
}

// New builds a scheduler. maxConcurrent < 1 is clamped to 1.
func New(store Store, runner Runner, maxConcurrent int, logger *slog.Logger) *Scheduler {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		store:        store,
		runner:       runner,
		registry:     NewResourceRegistry(),
		max:          maxConcurrent,
		logger:       logger,
		pollInterval: 2 * time.Second,
		wake:         make(chan struct{}, 1),
	}
}

// Registry exposes the lock table (tests and diagnostics).
func (s *Scheduler) Registry() *ResourceRegistry { return s.registry }

// Wake nudges the scheduler to re-evaluate the queue immediately (a task was
// enqueued, finished, cancelled, or reordered). Coalesced: a pending wake is
// enough.
func (s *Scheduler) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run loops until ctx ends: every wake (or poll fallback) it tries to start
// whatever the policy and the locks allow.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
		s.tick(ctx)
	}
}

// tick starts every ready task that fits the budget and whose resources are
// free. It is one pass, not a drain loop: a started task may free resources
// only after it finishes, and its completion wakes the next pass.
func (s *Scheduler) tick(ctx context.Context) {
	ready, err := s.store.ListReady(ctx)
	if err != nil {
		s.logger.Warn("queue: list ready", "err", err)
		return
	}
	if len(ready) == 0 {
		return
	}
	sortReady(ready)

	active, err := s.store.CountActive(ctx)
	if err != nil {
		s.logger.Warn("queue: count active", "err", err)
		return
	}

	for _, t := range ready {
		if active >= s.max {
			return // budget exhausted; a finishing task wakes the next pass
		}
		keys := DefaultKeys(t.ResourceKeys, t.Project)
		if !s.registry.TryAcquire(keys, t.ID) {
			continue // a running task holds a needed resource: stay queued
		}
		if err := s.store.Claim(ctx, t.ID); err != nil {
			// Another instance claimed it first (or it vanished): drop the
			// locks and let the winner run it.
			s.registry.Release(t.ID)
			continue
		}
		active++
		s.logger.Info("queue: starting task", "task", t.ID, "resources", keys)
		go func(id string) {
			defer s.registry.Release(id)
			defer s.Wake()
			s.runner.Run(ctx, id)
		}(t.ID)
	}
}

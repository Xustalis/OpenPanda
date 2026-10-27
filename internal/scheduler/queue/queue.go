// SPDX-License-Identifier: AGPL-3.0-or-later

// Package queue implements the node-local task queue scheduler: it decides
// which queued task runs next on this node. It is the temporal counterpart of
// the parent scheduler package (which decides WHICH node a task runs on) and
// is deliberately decoupled from internal/core: it only sees the Store and
// Runner interfaces below, so a future multi-node deployment can swap in a
// distributed lock table and a cluster-wide store without touching the
// ordering policy. (The execution-time half of that lock table already
// exists in core's exec_locks — see execlock.go — so paths this registry
// never sees, delegates, inline submits and other core processes on the same
// database, share the same key exclusion.)
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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/guard"
)

// ReadyTask is the scheduler's view of one queued task — just the fields the
// ordering policy and the resource registry need.
type ReadyTask struct {
	ID      string
	Project string
	WorkDir string // submitter-pinned work directory ("" = derive per task)
	// Pin carries spec.node verbatim — the hard destination a pinned task
	// names. The scheduler reads it only to decide whether this node's
	// resource table applies: a task pinned ELSEWHERE never runs locally
	// (the pin forbids local fallback — the forward parks or fails), so
	// holding a local project/scope key against it serializes remote work
	// behind unrelated local runs for nothing.
	Pin          string
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

// DefaultKeys fills in the resource keys a task occupies when it declared
// none: project tasks lock their project (same project shares a worktree and
// must serialize), a task with a submitter-pinned work directory locks that
// directory (two tasks writing one pinned tree serialize), and everything
// else locks its own id — which is to say, nothing at all: an anonymous task
// runs in a private per-task work dir (core.prepareRunDir), so a shared lock
// would only make independent tasks queue behind each other. Concurrency for
// those stays bounded by the node's MaxConcurrent, not by exclusion.
// ProjectKey returns the lock identity a whole-project task serializes on.
// The name folds case through lockFold: spec.project comes back in whatever
// casing the classifier inferred from the directory, --project arrives
// verbatim, and the same tree under two spellings must still exclude itself.
func ProjectKey(project string) string {
	return "project:" + lockFold(project)
}

func DefaultKeys(keys []string, project, workDir, taskID string) []string {
	if len(keys) > 0 {
		return keys
	}
	if project != "" {
		// The scope root rides next to the legacy project key: scoped
		// children hold "scope:<p>/<path>" leaves and must still exclude a
		// whole-project task (and vice versa), while tasks created before
		// scope keys existed keep serializing on "project:<p>". The project
		// key folds case like every other path-ish lock — the classifier's
		// spec.project arrives in whatever casing the model inferred from
		// the directory name ("Needlen"), while a typed --project flag is
		// verbatim ("needlen"), and two spellings of one tree must not get
		// parallel writers.
		return []string{ProjectKey(project), ScopeRootKey(project)}
	}
	if wd := workdirKey(workDir); wd != "" {
		return []string{"dir:" + wd}
	}
	return []string{"task:" + taskID}
}

// workdirKey normalizes a submitter-pinned work directory into its lock
// identity: spelling variants of one directory ("~/x", "./x", "/a/../x")
// must collide on the same key or the serialization the key exists for
// leaks. Symlinks resolve on a best-effort basis — an unresolvable path
// still gets a cleaned absolute spelling, which is exactly what the pinned
// dir's second task would compute too — and case folds like every other
// path-typed key (see lockFold).
func workdirKey(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	if strings.HasPrefix(dir, "~/") || dir == "~" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return lockFold(filepath.Clean(dir))
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return lockFold(resolved)
	}
	return lockFold(abs)
}

// WorkDirKey is the exported form of workdirKey for lock composition done
// outside this package: an isolated child's merge must hold the pinned
// directory's identity the same way a task pinned to it would.
func WorkDirKey(dir string) string { return workdirKey(dir) }

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
//
// It orders STARTS in this process. The same keys are taken again at execution
// time in the shared exec_locks table (core/execlock.go), which is what makes
// the exclusion hold for paths this registry never sees — inbound delegates,
// inline submits, and other core processes on the same database.
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
// task (a key held by taskID itself is not a conflict). Scope keys contend by
// prefix-overlap rather than exact match — a held "scope:p" blocks a new
// "scope:p/a" and vice versa, while "scope:p/a" and "scope:p/b" coexist.
// It returns false and locks nothing on any conflict.
func (r *ResourceRegistry) TryAcquire(keys []string, taskID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range keys {
		if IsScopeKey(k) {
			for hk, h := range r.holder {
				if h != taskID && IsScopeKey(hk) && ScopeKeysConflict(k, hk) {
					return false
				}
			}
			continue
		}
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
// no-op, so double-release (defensive cleanup paths) is safe. Reports whether
// the task actually held anything, so a caller that released before a wait
// knows whether a matching re-acquire is owed.
func (r *ResourceRegistry) Release(taskID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	held := len(r.byTask[taskID]) > 0
	for _, k := range r.byTask[taskID] {
		if r.holder[k] == taskID {
			delete(r.holder, k)
		}
	}
	delete(r.byTask, taskID)
	return held
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

	// pollInterval bounds how long a wake-less event can stall the queue —
	// the wake channel is in-process, so a task enqueued by another process
	// (task add, plan run, the panel's engine) is only discovered here. 400ms
	// keeps that cross-process pickup latency under a half-second; the poll
	// itself is one cheap SQLite read.
	pollInterval time.Duration
	wake         chan struct{}

	// selfPin resolves a ReadyTask.Pin to "names this node". Injected by the
	// owner (core's resolvePin), nil in tests — a nil resolver treats every
	// pin as local, preserving the pre-feature conservative serialization.
	selfPin func(ctx context.Context, ref string) bool

	// runners tracks in-flight Runner.Run calls so a shutdown can WAIT for
	// them instead of returning while one is still writing. Cancelling ctx
	// stops Run's dispatch loop, but a runner already inside Run does not
	// observe that cancellation until it finishes — and its writes (memory
	// consolidation, artifacts) then land after the owner closed the store.
	wg sync.WaitGroup

	// closeMu orders the transition from dispatching to drained. The
	// WaitGroup's counter is plain memory: a dispatch that is mid-tick when
	// the shutdown arrives can add to a counter Wait is already reading,
	// which is a data race, and the runner it counted would start after the
	// caller believed the queue was empty. Both sides take this mutex, so a
	// runner slot is either counted before the wait begins — and awaited by
	// it — or refused, and the task stays queued. This is the same ordering
	// core's detached artifact transfers use (artMu / artClosing / artWG).
	closeMu sync.Mutex
	closing bool
}

// Wait stops the scheduler from starting anything new and blocks until every
// in-flight runner has returned. Callers bound it with their own deadline: a
// wedged adapter must not hold a shutdown open.
//
// The counter transition happens under closeMu, so a dispatch that is mid-tick
// when the shutdown arrives is ordered against this wait rather than racing
// it: a slot taken before the wait starts is awaited by it, and a slot taken
// after is refused.
func (s *Scheduler) Wait() {
	s.closeMu.Lock()
	s.closing = true
	s.closeMu.Unlock()
	s.wg.Wait()
}

// reserveRunner takes one runner slot, or reports false when the scheduler is
// shutting down and must not start anything new. The slot is released by
// s.wg.Done() once the runner returns — including on the paths where the task
// turns out to be unclaimable, which release it without starting one.
func (s *Scheduler) reserveRunner() bool {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closing {
		return false
	}
	s.wg.Add(1)
	return true
}

// The fallback poll relaxes once the queue has been empty for a while: 400ms
// exists for cross-process pickup latency and stays the cadence while work
// sits in the queue (a budget-bound ready task still needs prompt pickup
// when another process frees a slot), but an empty queue polled forever is
// ~2 SELECTs a second of pure idle churn per attached process — daemon, web
// console and panel each run a scheduler over the same DB. Past ~30s of
// emptiness the poll steps back to 2s; a same-process submit still lands
// instantly through Wake().
const (
	queueIdleAfter = 75 // quiet ticks at 400ms ≈ 30s of an empty queue
	queueIdlePoll  = 2 * time.Second
)

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
		pollInterval: 400 * time.Millisecond,
		wake:         make(chan struct{}, 1),
	}
}

// WithSelfPin installs the resolver that tells a foreign pin from a local
// one (see ReadyTask.Pin). Returns the scheduler for chaining.
func (s *Scheduler) WithSelfPin(fn func(ctx context.Context, ref string) bool) *Scheduler {
	s.selfPin = fn
	return s
}

// Registry exposes the lock table (tests and diagnostics).
func (s *Scheduler) Registry() *ResourceRegistry { return s.registry }

// ReleaseTask frees every resource key held by taskID and wakes the queue so
// a blocked task can start. A task waiting on its own children calls this so
// the children — which inherit the same project key — can actually run: the
// waiter is not touching the worktree while it waits, and without the release
// a child queued behind its own parent's key deadlocks the pair. Reports
// whether anything was held (a delegate-driven task never held queue locks,
// and owes no re-acquire).
func (s *Scheduler) ReleaseTask(taskID string) bool {
	held := s.registry.Release(taskID)
	if held {
		s.Wake()
	}
	return held
}

// AcquireTask blocks until every key is locked for taskID, or ctx ends
// (false). The runner's end-of-run release still fires from its defer, so
// re-acquired keys live exactly as long as the resumed round; waiting for
// whoever took the keys meanwhile is the serialization the keys exist for.
func (s *Scheduler) AcquireTask(ctx context.Context, taskID string, keys []string) bool {
	if len(keys) == 0 {
		return true
	}
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		if s.registry.TryAcquire(keys, taskID) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
}

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
// whatever the policy and the locks allow. The fallback cadence relaxes after
// a sustained empty queue (queueIdleAfter) — in-process wakes are never
// slowed, only the wake-less polling path.
func (s *Scheduler) Run(ctx context.Context) {
	timer := time.NewTimer(s.pollInterval)
	defer timer.Stop()
	quiet := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-s.wake:
			quiet = 0
		}
		if s.tick(ctx) == 0 {
			quiet++
		} else {
			quiet = 0
		}
		timer.Reset(s.pollDelay(quiet))
	}
}

// pollDelay picks the next fallback interval: fast while anything is queued
// (or an error may have hidden a queued task), relaxed once the queue has
// provably stayed empty. Errors count as quiet — a store that cannot answer
// keeps its WARN stream slow instead of repeating every 400ms.
func (s *Scheduler) pollDelay(quietTicks int) time.Duration {
	if quietTicks >= queueIdleAfter {
		return queueIdlePoll
	}
	return s.pollInterval
}

// tick starts every ready task that fits the budget and whose resources are
// free. It is one pass, not a drain loop: a started task may free resources
// only after it finishes, and its completion wakes the next pass. The return
// is the ready-set size, used by Run to tell a lively queue (budget-bound
// work still needs prompt polling) from an idle one eligible for backoff.
func (s *Scheduler) tick(ctx context.Context) int {
	ready, err := s.store.ListReady(ctx)
	if err != nil {
		s.logger.Warn("queue: list ready", "err", err)
		return 0
	}
	if len(ready) == 0 {
		return 0
	}
	sortReady(ready)

	active, err := s.store.CountActive(ctx)
	if err != nil {
		s.logger.Warn("queue: count active", "err", err)
		return len(ready)
	}

	for _, t := range ready {
		if active >= s.max {
			return len(ready) // budget exhausted; a finishing task wakes the next pass
		}
		// Take the runner slot before anything is claimed. The slot is what
		// the shutdown waits on, and a task claimed but never started would
		// sit in the store claimed forever; a scheduler that is shutting down
		// refuses the slot, leaving the task queued for the next process.
		if !s.reserveRunner() {
			return len(ready)
		}
		keys := DefaultKeys(t.ResourceKeys, t.Project, t.WorkDir, t.ID)
		if t.Pin != "" && s.selfPin != nil && !s.selfPin(ctx, t.Pin) {
			// Foreign-pinned work forwards in the runner and returns; the
			// local lock table only guards local execution — taking these
			// keys would park the remote run behind a local holder of the
			// same project (observed live: a node2-pinned audit queued
			// behind a local run's root scope while node2 sat idle).
			keys = nil
		}
		if !s.registry.TryAcquire(keys, t.ID) {
			s.wg.Done()
			continue // a running task holds a needed resource: stay queued
		}
		if err := s.store.Claim(ctx, t.ID); err != nil {
			// Another instance claimed it first (or it vanished): drop the
			// locks and let the winner run it.
			s.registry.Release(t.ID)
			s.wg.Done()
			continue
		}
		active++
		s.logger.Info("queue: starting task", "task", t.ID, "resources", keys)
		guard.Go(s.logger, "queue: runner "+t.ID, nil, func() {
			defer s.wg.Done()
			defer s.registry.Release(t.ID)
			defer s.Wake()
			s.runner.Run(ctx, t.ID)
		})
	}
	return len(ready)
}

// SPDX-License-Identifier: AGPL-3.0-or-later

package queue

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeStore is an in-memory Store for scheduler tests.
type fakeStore struct {
	mu         sync.Mutex
	ready      []ReadyTask
	active     int
	claims     []string
	claimFails map[string]bool
}

func (f *fakeStore) ListReady(context.Context) ([]ReadyTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ReadyTask, len(f.ready))
	copy(out, f.ready)
	return out, nil
}

func (f *fakeStore) CountActive(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active, nil
}

func (f *fakeStore) Claim(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimFails[id] {
		return errors.New("claimed elsewhere")
	}
	// Remove from ready so a second pass does not re-start it.
	for i, t := range f.ready {
		if t.ID == id {
			f.ready = append(f.ready[:i], f.ready[i+1:]...)
			break
		}
	}
	f.claims = append(f.claims, id)
	return nil
}

// gateRunner blocks each Run until released, reporting started tasks.
type gateRunner struct {
	mu      sync.Mutex
	started []string
	release chan string
	done    chan string
}

func newGateRunner() *gateRunner {
	return &gateRunner{release: make(chan string, 8), done: make(chan string, 8)}
}

func (g *gateRunner) Run(_ context.Context, id string) {
	g.mu.Lock()
	g.started = append(g.started, id)
	g.mu.Unlock()
	<-g.release // simulate work until the test releases this task
	g.done <- id
}

func (g *gateRunner) startedIDs() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.started...)
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}

// waitForStarted polls until n tasks have registered as started (the
// scheduler launches each task on its own goroutine).
func waitForStarted(t *testing.T, g *gateRunner, n int) {
	t.Helper()
	waitFor(t, func() bool { return len(g.startedIDs()) == n }, "expected started count")
}

func TestSortReadyPolicy(t *testing.T) {
	ts := []ReadyTask{
		{ID: "fifo-old", Priority: 1, CreatedAt: 10},
		{ID: "dragged-2", Seq: 2, Priority: 2, CreatedAt: 99},
		{ID: "high", Priority: 0, CreatedAt: 20},
		{ID: "dragged-1", Seq: 1, Priority: 2, CreatedAt: 99},
		{ID: "fifo-new", Priority: 1, CreatedAt: 5},
	}
	sortReady(ts)

	want := []string{"dragged-1", "dragged-2", "high", "fifo-new", "fifo-old"}
	for i, w := range want {
		if ts[i].ID != w {
			t.Fatalf("order[%d] = %s, want %s (full: %v)", i, ts[i].ID, w, ts)
		}
	}
}

func TestDefaultKeys(t *testing.T) {
	// A project task holds the legacy project key plus the scope root: the
	// root is what excludes scoped children (scope:panda/<path> leaves),
	// while the legacy key keeps pre-scope rows serializing as before.
	if got := DefaultKeys(nil, "panda", "", "t1"); len(got) != 2 || got[0] != "project:panda" || got[1] != "scope:panda" {
		t.Fatalf("project default = %v", got)
	}
	// An anonymous task locks only itself: two anonymous tasks run in
	// parallel, each in its own derived work dir (core.prepareRunDir).
	if got := DefaultKeys(nil, "", "", "t1"); len(got) != 1 || got[0] != "task:t1" {
		t.Fatalf("anonymous default = %v", got)
	}
	if got := DefaultKeys(nil, "", "", "t2"); len(got) != 1 || got[0] != "task:t2" {
		t.Fatalf("anonymous tasks must not share one lock: %v", got)
	}
	// A submitter-pinned work dir is the shared tree: two tasks pinning the
	// same directory (in any spelling) serialize on it.
	dir := t.TempDir()
	a := DefaultKeys(nil, "", dir, "t1")
	b := DefaultKeys(nil, "", dir+"/", "t2")
	if len(a) != 1 || len(b) != 1 || a[0] != b[0] {
		t.Fatalf("same pinned dir must collide on one key: %v vs %v", a, b)
	}
	c := DefaultKeys(nil, "", dir+"-other", "t3")
	if c[0] == a[0] {
		t.Fatalf("different dirs must not collide: %v vs %v", a, c)
	}
	if got := DefaultKeys([]string{"node:opi"}, "panda", dir, "t1"); got[0] != "node:opi" {
		t.Fatalf("explicit keys must win: %v", got)
	}
}

func TestRegistryAllOrNothing(t *testing.T) {
	r := NewResourceRegistry()
	if !r.TryAcquire([]string{"a", "b"}, "t1") {
		t.Fatal("first acquire must succeed")
	}
	// Partial overlap must fail AND not lock the non-conflicting key.
	if r.TryAcquire([]string{"b", "c"}, "t2") {
		t.Fatal("conflicting acquire must fail")
	}
	if r.HeldBy("c") != "" {
		t.Fatal("failed acquire must not half-lock")
	}
	// Same holder re-acquiring is not a conflict.
	if !r.TryAcquire([]string{"a"}, "t1") {
		t.Fatal("self re-acquire must succeed")
	}
	r.Release("t1")
	if !r.TryAcquire([]string{"a", "b", "c"}, "t2") {
		t.Fatal("acquire after release must succeed")
	}
	r.Release("t2")
	r.Release("t2") // double release must not panic
}

// TestReleaseDuringWaitLetsChildStart is the live deadlock shape: the parent
// (queue-held on project:X) waits on a child that needs the same key, so
// before the fix the child could never start — it queued behind its own
// parent's lock while the parent queued behind the child. The waiting parent
// releases; the child claims and runs; the parent re-acquires before it
// resumes.
func TestReleaseDuringWaitLetsChildStart(t *testing.T) {
	store := &fakeStore{ready: []ReadyTask{
		{ID: "parent", Project: "X", CreatedAt: 1},
		{ID: "child", Project: "X", CreatedAt: 2},
	}}
	runner := newGateRunner()
	s := New(store, runner, 4, nil)

	s.tick(context.Background())
	waitForStarted(t, runner, 1)
	if got := s.Registry().HeldBy(ProjectKey("X")); got != "parent" {
		t.Fatalf("holder = %q, want parent", got)
	}
	// The child cannot start while the parent holds the key.
	s.tick(context.Background())
	if len(runner.startedIDs()) != 1 {
		t.Fatalf("started = %v; the child must stay blocked behind its parent's key", runner.startedIDs())
	}

	// The waiting parent releases: the next pass starts the child.
	if !s.ReleaseTask("parent") {
		t.Fatal("ReleaseTask reported nothing held")
	}
	s.tick(context.Background())
	waitForStarted(t, runner, 2)

	// The child finishes; the parent re-acquires before resuming.
	runner.release <- "child"
	<-runner.done
	s.registry.Release("child")
	keys := []string{ProjectKey("X")}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !s.AcquireTask(ctx, "parent", keys) {
		t.Fatal("re-acquire failed")
	}
	if got := s.Registry().HeldBy(ProjectKey("X")); got != "parent" {
		t.Fatalf("holder = %q, want parent after re-acquire", got)
	}
	// An unheld task owes no re-acquire.
	if s.ReleaseTask("never-held") {
		t.Fatal("ReleaseTask claimed an unheld task")
	}
	runner.release <- "parent"
}

func TestSchedulerParallelDisjointSerialConflict(t *testing.T) {
	store := &fakeStore{ready: []ReadyTask{
		{ID: "opi", Project: "", ResourceKeys: []string{"node:opi"}, CreatedAt: 1},
		{ID: "proj-a", Project: "a", CreatedAt: 2},
		{ID: "proj-a-2", Project: "a", CreatedAt: 3},
	}}
	runner := newGateRunner()
	s := New(store, runner, 4, nil)

	s.tick(context.Background())

	// opi and proj-a are resource-disjoint → both start; proj-a-2 conflicts
	// with proj-a and stays queued despite free budget.
	waitForStarted(t, runner, 2)
	started := runner.startedIDs()
	got := map[string]bool{started[0]: true, started[1]: true}
	if !got["opi"] || !got["proj-a"] {
		t.Fatalf("started = %v, want exactly {opi proj-a}", started)
	}
	if got := s.Registry().HeldBy("project:a"); got != "proj-a" {
		t.Fatalf("project:a held by %q, want proj-a", got)
	}

	// Finish proj-a: the next pass must pick up proj-a-2.
	runner.release <- "proj-a"
	<-runner.done
	s.registry.Release("proj-a")
	s.tick(context.Background())
	waitFor(t, func() bool { return len(runner.startedIDs()) == 3 }, "proj-a-2 never started after conflict freed")
	runner.release <- "opi"
	runner.release <- "proj-a-2"
	<-runner.done
	<-runner.done
}

// Two anonymous tasks — no keys, no project — each take a lock of their own
// now that every anonymous task runs in a private per-task work dir. Before
// the private-dir change they shared one conservative lock and serialized
// despite free budget, which is exactly the serial behaviour this removes.
func TestSchedulerAnonymousTasksRunInParallel(t *testing.T) {
	store := &fakeStore{ready: []ReadyTask{
		{ID: "anon-a", CreatedAt: 1},
		{ID: "anon-b", CreatedAt: 2},
	}}
	runner := newGateRunner()
	s := New(store, runner, 4, nil)

	s.tick(context.Background())
	waitForStarted(t, runner, 2)
	if got := s.Registry().HeldBy("task:anon-a"); got != "anon-a" {
		t.Fatalf("task:anon-a held by %q", got)
	}
	runner.release <- "anon-a"
	runner.release <- "anon-b"
	<-runner.done
	<-runner.done
}

func TestSchedulerRespectsMaxConcurrent(t *testing.T) {
	store := &fakeStore{ready: []ReadyTask{
		{ID: "t1", ResourceKeys: []string{"r1"}, CreatedAt: 1},
		{ID: "t2", ResourceKeys: []string{"r2"}, CreatedAt: 2},
		{ID: "t3", ResourceKeys: []string{"r3"}, CreatedAt: 3},
	}}
	runner := newGateRunner()
	s := New(store, runner, 2, nil)

	s.tick(context.Background())
	waitForStarted(t, runner, 2)

	// Even after the budget frees, a finishing task wakes the next pass.
	runner.release <- "t1"
	<-runner.done
	s.registry.Release("t1")
	store.mu.Lock()
	store.active = 0 // simulate the DB reflecting the finished task
	store.mu.Unlock()
	s.Wake()
	ctx, cancel := context.WithCancel(context.Background())
	go s.Run(ctx)
	waitFor(t, func() bool { return len(runner.startedIDs()) == 3 }, "t3 never started via wake")
	cancel()
	runner.release <- "t2"
	runner.release <- "t3"
	<-runner.done
	<-runner.done
}

func TestSchedulerSkipsLostClaimRace(t *testing.T) {
	store := &fakeStore{
		ready: []ReadyTask{
			{ID: "winner", ResourceKeys: []string{"r"}, CreatedAt: 1},
			{ID: "loser", ResourceKeys: []string{"x"}, CreatedAt: 2},
		},
		claimFails: map[string]bool{"loser": true},
	}
	runner := newGateRunner()
	s := New(store, runner, 4, nil)

	s.tick(context.Background())
	waitForStarted(t, runner, 1)
	started := runner.startedIDs()
	if started[0] != "winner" {
		t.Fatalf("started = %v, want only winner", started)
	}
	// The loser's lock must have been released after the failed claim.
	if got := s.Registry().HeldBy("x"); got != "" {
		t.Fatalf("key x leaked to %q after failed claim", got)
	}
	runner.release <- "winner"
	<-runner.done
}

// TestSchedulerPollPicksUpWakelessEnqueue models a task enqueued by another
// process: the store gains a ready row with no Wake() to announce it, and
// only the fallback ticker can find it. The deadline (4× the poll interval)
// is tighter than the old 2 s poll, so this test fails if the interval
// regresses back to seconds-scale polling.
func TestSchedulerPollPicksUpWakelessEnqueue(t *testing.T) {
	store := &fakeStore{}
	runner := newGateRunner()
	s := New(store, runner, 2, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	// Cross-process enqueue: nobody calls Wake() — the ticker must see it.
	store.mu.Lock()
	store.ready = append(store.ready, ReadyTask{ID: "cross-proc", ResourceKeys: []string{"r"}, CreatedAt: 1})
	store.mu.Unlock()

	deadline := time.Now().Add(4 * s.pollInterval)
	for time.Now().Before(deadline) {
		if len(runner.startedIDs()) == 1 {
			runner.release <- "cross-proc"
			<-runner.done
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("wake-less enqueue not picked up within %v", 4*s.pollInterval)
}

// TestPollDelayBacksOffWhenIdle pins the idle-relaxation contract: the
// fallback poll holds the fast cadence while anything might be queued and
// only steps back once the queue has provably stayed empty — a live queue
// and a same-process wake must never see the slow interval.
func TestPollDelayBacksOffWhenIdle(t *testing.T) {
	s := New(&fakeStore{}, newGateRunner(), 2, nil)
	for _, q := range []int{0, 1, queueIdleAfter - 1} {
		if d := s.pollDelay(q); d != s.pollInterval {
			t.Fatalf("pollDelay(%d) = %v, want fast %v", q, d, s.pollInterval)
		}
	}
	for _, q := range []int{queueIdleAfter, queueIdleAfter * 4} {
		if d := s.pollDelay(q); d != queueIdlePoll {
			t.Fatalf("pollDelay(%d) = %v, want relaxed %v", q, d, queueIdlePoll)
		}
	}
}

// TestForeignPinSkipsLocalResources pins the cross-node parallel case: a task
// hard-pinned to another node must not queue behind a local holder of the same
// project key — it never runs locally (the pin forbids fallback), so local
// locks are pure serialization of remote work. A pin naming THIS node (or no
// resolver at all) keeps the conservative behavior.
func TestForeignPinSkipsLocalResources(t *testing.T) {
	store := &fakeStore{ready: []ReadyTask{
		{ID: "local-a", Project: "needlen", CreatedAt: 1},
		{ID: "pinned-b", Project: "needlen", Pin: "macbook-m1-b", CreatedAt: 2},
		{ID: "pinned-self", Project: "needlen", Pin: "macbook-m1", CreatedAt: 3},
	}}
	runner := newGateRunner()
	s := New(store, runner, 4, nil).WithSelfPin(func(_ context.Context, ref string) bool {
		return ref == "macbook-m1"
	})

	s.tick(context.Background())
	// local-a takes project:needlen; pinned-b starts anyway (foreign pin →
	// no local keys); pinned-self stays queued behind the same project.
	waitForStarted(t, runner, 2)
	started := runner.startedIDs()
	got := map[string]bool{started[0]: true, started[1]: true}
	if !got["local-a"] || !got["pinned-b"] {
		t.Fatalf("started = %v, want exactly {local-a pinned-b}", started)
	}
	if got := s.Registry().HeldBy(ProjectKey("needlen")); got != "local-a" {
		t.Fatalf("holder = %q, want local-a", got)
	}

	// A nil resolver treats every pin as local — the pre-feature behavior.
	runner.release <- "local-a"
	<-runner.done
	s.registry.Release("local-a")
	s2 := New(&fakeStore{ready: []ReadyTask{
		{ID: "pinned-x", Project: "needlen", Pin: "node-x", CreatedAt: 1},
		{ID: "holder", Project: "needlen", CreatedAt: 2},
	}}, newGateRunner(), 4, nil)
	s2.tick(context.Background())
	waitForStarted(t, s2Runner(s2), 1)
	runner.release <- "pinned-b"
}

// s2Runner digs the gate runner back out of a scheduler built inline — the
// tests' fakeStore/runner plumbing only, not production surface.
func s2Runner(s *Scheduler) *gateRunner {
	return s.runner.(*gateRunner)
}

// TestWaitOrdersAgainstDispatch pins the shutdown ordering the race detector
// caught in the panel's teardown: a dispatch that is mid-tick when the
// shutdown starts must not add to the runner counter while Wait is observing
// it.
//
// Go models the two sides deliberately — the first increment of a WaitGroup
// is a read of the waiter's state, the first waiter is a write of it (see
// sync/waitgroup.go) — so an unordered pair of the two is reported as a data
// race. The panel's engine close hit exactly that: WaitQueue observed the
// counter while the queue scheduler's dispatch loop was still inside a tick.
//
// The dispatch runs on its own goroutine and is never released by anything
// that waited for the wait: a channel the test closes after the wait returns
// would order the two accesses and hide the very race this test exists for.
func TestWaitOrdersAgainstDispatch(t *testing.T) {
	store := &fakeStore{ready: []ReadyTask{{ID: "t1"}, {ID: "t2"}}}
	runner := newGateRunner()
	s := New(store, runner, 1, nil)

	// The first pass starts t1 and nothing else: its runner blocks, so the
	// runner counter is 1.
	s.tick(context.Background())
	waitForStarted(t, runner, 1)

	// The dispatch loop, an independent goroutine as it is in production.
	// It ticks after the in-flight runner has returned, so the counter is
	// zero again and its Add is the first increment — the read the runtime
	// pairs with Wait's write.
	dispatchDone := make(chan struct{})
	go func() {
		time.Sleep(150 * time.Millisecond)
		s.tick(context.Background())
		close(dispatchDone)
	}()

	// The shutdown starts while a runner is in flight: Wait registers as the
	// first waiter.
	waitDone := make(chan struct{})
	go func() {
		s.Wait()
		close(waitDone)
	}()

	// Let the wait register before the runner returns. A wait that arrives
	// after the counter already reached zero takes the early return and never
	// becomes a waiter — and then there is no write to pair the dispatch's
	// read with.
	time.Sleep(50 * time.Millisecond)

	// The in-flight runner returns, so the counter reaches zero and the wait
	// is released.
	runner.release <- "t1"
	<-runner.done
	<-waitDone

	<-dispatchDone

	// And the task that was about to start must not start after the wait: the
	// shutdown believes the queue is drained, and a runner that starts
	// afterwards writes into a store the caller is already closing.
	time.Sleep(100 * time.Millisecond)
	if started := runner.startedIDs(); len(started) != 1 {
		t.Fatalf("started = %v; no task may start after the queue was drained", started)
	}
}

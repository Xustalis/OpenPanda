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
	if got := DefaultKeys(nil, "panda"); len(got) != 1 || got[0] != "project:panda" {
		t.Fatalf("project default = %v", got)
	}
	if got := DefaultKeys(nil, ""); len(got) != 1 || got[0] != DefaultResourceKey {
		t.Fatalf("anonymous default = %v", got)
	}
	if got := DefaultKeys([]string{"node:opi"}, "panda"); got[0] != "node:opi" {
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

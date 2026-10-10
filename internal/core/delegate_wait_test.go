// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

// spawnWaitPair builds a registered core with a parent task and a spawned
// child row, ready for awaitChild scenarios.
func spawnWaitPair(t *testing.T, c *Core, ctx context.Context) (Task, Task) {
	t.Helper()
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	parent, _, _, err := c.createTask(ctx, TaskInput{Title: "parent", Intent: "orchestrate"})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	child, err := c.SpawnChildTask(ctx, parent.TaskID, TaskInput{Title: "child", Intent: "sub work"})
	if err != nil {
		t.Fatalf("spawn child: %v", err)
	}
	return parent, child
}

// TestAwaitChildLeaseAwareWait is the regression for the fixed-window wait: a
// child whose executor keeps renewing its lease must hold the parent past any
// single window — the lease bounds silence, not runtime. The goroutine renews
// the child lease for well past one silentCap, then completes it remotely;
// awaitChild must return delivered with the landed result.
func TestAwaitChildLeaseAwareWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c := newCore(t, "wait-parent", "")
	c.leaseTimeout = 1200 * time.Millisecond
	parent, child := spawnWaitPair(t, c, ctx)

	// Executor-side heartbeat: renew the child's lease until the result lands.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		tick := time.NewTicker(300 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				_ = c.store.SetLease(ctx, child.TaskID, 1200)
			}
		}
	}()
	// Complete the child remotely after ~2 silentCaps — long enough that a
	// fixed-window wait would already have given up.
	go func() {
		time.Sleep(2600 * time.Millisecond)
		_ = c.store.CompleteFromRemote(context.WithoutCancel(ctx), child.TaskID, c.nodeID,
			bus.TaskResultPayload{TaskID: child.TaskID, State: StateDone, Stdout: "child product"})
	}()

	waiter := make(chan bus.TaskResultPayload, 1)
	start := time.Now()
	r, delivered, _, err := c.awaitChild(ctx, parent, child, waiter)
	if err != nil {
		t.Fatalf("awaitChild: %v", err)
	}
	if !delivered {
		t.Fatal("a child that keeps renewing its lease must be awaited to completion, not parked")
	}
	if elapsed := time.Since(start); elapsed < 1200*time.Millisecond {
		t.Fatalf("returned after %v — ahead of the silence cap, the liveness wait did not hold", elapsed)
	}
	if r.State != StateDone || r.Stdout != "child product" {
		t.Fatalf("folded payload = %+v, want the child's done result", r)
	}
}

// TestAwaitChildSilentChildGoesPending covers the other half: a child with no
// live lease (parked for a link, waiting claim, orphaned) gets exactly one
// lease-length of grace, then reports pending — it must not block the parent
// forever on a wait nobody is renewing.
func TestAwaitChildSilentChildGoesPending(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c := newCore(t, "wait-parent2", "")
	c.leaseTimeout = 800 * time.Millisecond
	parent, child := spawnWaitPair(t, c, ctx)

	waiter := make(chan bus.TaskResultPayload, 1)
	start := time.Now()
	_, delivered, state, err := c.awaitChild(ctx, parent, child, waiter)
	if err != nil {
		t.Fatalf("awaitChild: %v", err)
	}
	if delivered {
		t.Fatal("a child with no lease must report pending, not delivered")
	}
	if elapsed := time.Since(start); elapsed < 800*time.Millisecond || elapsed > 8*time.Second {
		t.Fatalf("silent grace elapsed %v, want ~one lease (800ms) and far under the test timeout", elapsed)
	}
	if state == "" {
		t.Fatal("pending return should carry the child's last observed state")
	}
}

// TestAwaitChildParentDeadline is the hard bound: even a child whose lease
// stays fresh does not hold the parent past the task's own deadline.
func TestAwaitChildParentDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c := newCore(t, "wait-parent3", "")
	c.leaseTimeout = 1200 * time.Millisecond
	parent, child := spawnWaitPair(t, c, ctx)
	// DeadlineUnix is whole seconds — +3s lands the bound ~2–3s out no
	// matter where in the current second the clock sits.
	parent.DeadlineUnix = time.Now().Add(3 * time.Second).Unix()

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		tick := time.NewTicker(300 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				_ = c.store.SetLease(ctx, child.TaskID, 1200)
			}
		}
	}()

	waiter := make(chan bus.TaskResultPayload, 1)
	start := time.Now()
	_, delivered, _, err := c.awaitChild(ctx, parent, child, waiter)
	if err != nil {
		t.Fatalf("awaitChild: %v", err)
	}
	if delivered {
		t.Fatal("a deadline-exceeded wait must report pending even with a live child")
	}
	if elapsed := time.Since(start); elapsed < 1200*time.Millisecond || elapsed > 8*time.Second {
		// Past silentCap proves the fresh lease held the wait; the deadline,
		// not silence, is what ended it.
		t.Fatalf("deadline-bound wait elapsed %v, want ~2-3s", elapsed)
	}
}

// TestFoldLateChildrenReadsRows proves the late-result fold is row-driven: a
// child whose result landed without any waiter signal (the late frame's
// channel already gone) is still picked up and removed from pending.
func TestFoldLateChildrenReadsRows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := newCore(t, "fold-parent", "")
	_, child := spawnWaitPair(t, c, ctx)

	pending := []pendingChild{{id: child.TaskID, title: child.Title}}
	if notes := c.foldLateChildren(ctx, &pending); len(notes) != 0 {
		t.Fatalf("live child should fold nothing, got %v", notes)
	}
	if len(pending) != 1 {
		t.Fatal("live child must stay pending")
	}

	_ = c.store.CompleteFromRemote(ctx, child.TaskID, c.nodeID,
		bus.TaskResultPayload{TaskID: child.TaskID, State: StateDone, Stdout: "shipped"})
	notes := c.foldLateChildren(ctx, &pending)
	if len(notes) != 1 || pending != nil && len(pending) != 0 {
		t.Fatalf("resolved child should fold one note and leave pending empty: notes=%v pending=%v", notes, pending)
	}
}

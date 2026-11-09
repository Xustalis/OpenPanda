// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

// TestWaiterSetBroadcasts pins the regression the single-slot map caused: a
// second registration for the same task used to displace the first, so the
// displaced Submit blocked until its lease lapsed even though the result had
// already arrived.
func TestWaiterSetBroadcasts(t *testing.T) {
	var w waiterSet
	first := w.add("task-1")
	second := w.add("task-1")

	if got := w.count("task-1"); got != 2 {
		t.Fatalf("registrations = %d, want 2", got)
	}

	w.signal("task-1", bus.TaskResultPayload{TaskID: "task-1", State: "done"})

	for i, rt := range []*resultWaiter{first, second} {
		select {
		case p := <-rt.ch:
			if p.State != "done" {
				t.Fatalf("waiter %d got state %q, want done", i, p.State)
			}
		case <-time.After(time.Second):
			t.Fatalf("waiter %d was never signalled", i)
		}
	}
}

// TestWaiterSetRemoveIsPerRegistration guards the other half of the fix: the
// old deferred Delete dropped whichever channel was in the map, so a finishing
// caller could silently unregister a sibling that was still waiting.
func TestWaiterSetRemoveIsPerRegistration(t *testing.T) {
	var w waiterSet
	leaving := w.add("task-2")
	staying := w.add("task-2")

	w.remove("task-2", leaving)
	if got := w.count("task-2"); got != 1 {
		t.Fatalf("registrations after one remove = %d, want 1", got)
	}

	w.signal("task-2", bus.TaskResultPayload{TaskID: "task-2", State: "failed"})
	select {
	case p := <-staying.ch:
		if p.State != "failed" {
			t.Fatalf("surviving waiter got state %q, want failed", p.State)
		}
	default:
		t.Fatal("removing one waiter unregistered the other")
	}

	w.remove("task-2", staying)
	if got := w.count("task-2"); got != 0 {
		t.Fatalf("registrations after last remove = %d, want 0", got)
	}
	if _, ok := w.byTask["task-2"]; ok {
		t.Fatal("empty task entry was left in the map")
	}
}

// TestWaiterSetSignalNeverBlocks covers the read-loop contract: a caller that
// walked away with its buffer already full must not stall the goroutine that
// delivered the result.
func TestWaiterSetSignalNeverBlocks(t *testing.T) {
	var w waiterSet
	rt := w.add("task-3")
	rt.ch <- bus.TaskResultPayload{TaskID: "task-3", State: "done"}

	done := make(chan struct{})
	go func() {
		w.signal("task-3", bus.TaskResultPayload{TaskID: "task-3", State: "retry"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("signal blocked on a full waiter buffer")
	}

	if got := (<-rt.ch).State; got != "done" {
		t.Fatalf("first payload = %q, want done (late frame must be dropped)", got)
	}
}

// TestWaiterSetClearDropsEveryWaiter covers the fold paths that abandon a
// still-pending child: nothing may stay registered for a reader that is gone,
// and signalling afterwards must be a no-op.
func TestWaiterSetClearDropsEveryWaiter(t *testing.T) {
	var w waiterSet
	a := w.add("task-4")
	b := w.add("task-4")

	w.clear("task-4")
	if got := w.count("task-4"); got != 0 {
		t.Fatalf("registrations after clear = %d, want 0", got)
	}
	w.signal("task-4", bus.TaskResultPayload{TaskID: "task-4", State: "done"})
	for i, rt := range []*resultWaiter{a, b} {
		select {
		case p := <-rt.ch:
			t.Fatalf("waiter %d received %q after clear", i, p.State)
		default:
		}
	}
}

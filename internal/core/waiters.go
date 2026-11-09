// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"sync"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

// resultWaiter is one synchronous caller parked on a task id. The buffered
// channel holds a single payload: a late or duplicate frame after the caller
// already returned is dropped instead of blocking the read loop.
type resultWaiter struct {
	ch chan bus.TaskResultPayload
}

// waiterSet fans one task result out to every caller waiting on the same
// task. It replaces the former `sync.Map[taskID]chan` shape, which held
// exactly one channel per task: a second registration silently replaced the
// first, and the displaced caller then blocked until its lease lapsed even
// though the result had already arrived.
//
// Overlapping registrations are real, not theoretical: a plan child awaited by
// its parent can also be awaited by an explicit `Submit` on the same id, and
// resumeRemote can register while a prior delegation wait is still unwinding.
type waiterSet struct {
	mu     sync.Mutex
	byTask map[string]map[*resultWaiter]struct{}
}

// add registers a fresh waiter for taskID and returns it.
func (w *waiterSet) add(taskID string) *resultWaiter {
	rt := &resultWaiter{ch: make(chan bus.TaskResultPayload, 1)}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.byTask == nil {
		w.byTask = make(map[string]map[*resultWaiter]struct{})
	}
	set := w.byTask[taskID]
	if set == nil {
		set = make(map[*resultWaiter]struct{}, 1)
		w.byTask[taskID] = set
	}
	set[rt] = struct{}{}
	return rt
}

// remove unregisters one waiter. It never touches a sibling registration and
// drops the task's map entry once the last waiter leaves, so a long-lived
// daemon does not accumulate one empty set per task it ever ran.
func (w *waiterSet) remove(taskID string, rt *resultWaiter) {
	if rt == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	set := w.byTask[taskID]
	if set == nil {
		return
	}
	delete(set, rt)
	if len(set) == 0 {
		delete(w.byTask, taskID)
	}
}

// signal delivers p to every live waiter for taskID without blocking: each
// send is non-blocking against a size-1 buffer, so a caller that already gave
// up cannot stall the bus read loop that produced the result.
func (w *waiterSet) signal(taskID string, p bus.TaskResultPayload) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for rt := range w.byTask[taskID] {
		select {
		case rt.ch <- p:
		default:
		}
	}
}

// clear drops every waiter for a task. It backs the fold paths that abandon a
// still-pending child: the row, not a channel, is what a late result lands on,
// so nothing should stay registered for a reader that no longer exists.
func (w *waiterSet) clear(taskID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.byTask, taskID)
}

// count returns the live registrations for a task. Tests assert teardown
// with it; production code has no reason to poll.
func (w *waiterSet) count(taskID string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.byTask[taskID])
}

// SPDX-License-Identifier: AGPL-3.0-or-later

package defense

import (
	"sync"
	"time"
)

// LoopDetector counts failed attempts per task and signals when a task has
// spent its retry budget and should pause instead of retrying again (design
// §14.2 signal C, plan P2-18). It is the deterministic "diminishing returns"
// check: a task that keeps failing is paused for analysis rather than retried
// forever. Keyed by task id; in-memory, so a restart resets the counters.
type LoopDetector struct {
	mu   sync.Mutex
	max  int
	seen map[string]int
	// at timestamps each task's last touch. Tasks that fail permanently or
	// are cancelled never pass through Reset, so without a bound the table
	// keeps one entry per such task for the daemon's lifetime.
	at map[string]time.Time
}

// loopSeenMax / loopSeenTTL bound the retry table: past the entry bound,
// counts untouched for the TTL are dropped (a fresh count only ever grants
// more retries to a task that has long been settled — the in-loop counter is
// what bounds a live retry storm).
const (
	loopSeenMax = 4096
	loopSeenTTL = time.Hour
)

// NewLoopDetector builds a detector allowing up to max retries per task beyond
// the first attempt. A negative max is treated as zero (no retries).
func NewLoopDetector(max int) *LoopDetector {
	if max < 0 {
		max = 0
	}
	return &LoopDetector{max: max, seen: make(map[string]int), at: make(map[string]time.Time)}
}

// Allow reports whether taskID may retry after a failure, incrementing its
// count. With max=2 it returns true for the first two failures and false for
// the third — the caller should pause the task at that point.
func (d *LoopDetector) Allow(taskID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	// Bound the table: past the entry bound, counts untouched for the TTL
	// belong to settled tasks and are dropped (see the at field comment).
	if len(d.at) > loopSeenMax {
		cutoff := time.Now().Add(-loopSeenTTL)
		for k, at := range d.at {
			if at.Before(cutoff) {
				delete(d.at, k)
				delete(d.seen, k)
			}
		}
	}
	d.seen[taskID]++
	d.at[taskID] = time.Now()
	return d.seen[taskID] <= d.max
}

// Reset clears a task's failure count (e.g. after it eventually succeeds).
func (d *LoopDetector) Reset(taskID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.seen, taskID)
	delete(d.at, taskID)
}

package defense

import "sync"

// LoopDetector counts failed attempts per task and signals when a task has
// spent its retry budget and should pause instead of retrying again (design
// §14.2 signal C, plan P2-18). It is the deterministic "diminishing returns"
// check: a task that keeps failing is paused for analysis rather than retried
// forever. Keyed by task id; in-memory, so a restart resets the counters.
type LoopDetector struct {
	mu   sync.Mutex
	max  int
	seen map[string]int
}

// NewLoopDetector builds a detector allowing up to max retries per task beyond
// the first attempt. A negative max is treated as zero (no retries).
func NewLoopDetector(max int) *LoopDetector {
	if max < 0 {
		max = 0
	}
	return &LoopDetector{max: max, seen: make(map[string]int)}
}

// Allow reports whether taskID may retry after a failure, incrementing its
// count. With max=2 it returns true for the first two failures and false for
// the third — the caller should pause the task at that point.
func (d *LoopDetector) Allow(taskID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen[taskID]++
	return d.seen[taskID] <= d.max
}

// Reset clears a task's failure count (e.g. after it eventually succeeds).
func (d *LoopDetector) Reset(taskID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.seen, taskID)
}

package scheduler

// Capacity is a node's allocatable resource pool (design doc §6.3). A zero
// field means "unconstrained" for that dimension (e.g. a node with no GPU).
type Capacity struct {
	CPUCores      int
	RAMGB         int
	GPUVRAMGB     int
	MaxConcurrent int // 0 = unlimited
}

// Usage is the resource slice a single task requires.
type Usage struct {
	CPUCores  int
	RAMGB     int
	GPUVRAMGB int
}

// Account tracks allocated capacity against a node's total pool. It is the
// deterministic heart of capacity-driven parallelism (design doc §6.3): a task
// is accepted only while the remaining capacity covers its needs, so a strong
// node runs several tasks at once instead of a binary idle/busy switch.
type Account struct {
	total     Capacity
	allocated Usage
	running   int
}

// NewAccount builds an account for a node's total capacity.
func NewAccount(total Capacity) *Account {
	return &Account{total: total}
}

// TryAcquire reserves u if it fits within the remaining capacity; otherwise it
// is a no-op and returns false so the caller can queue the task.
func (a *Account) TryAcquire(u Usage) bool {
	if a.total.MaxConcurrent > 0 && a.running >= a.total.MaxConcurrent {
		return false
	}
	if a.total.CPUCores > 0 && a.allocated.CPUCores+u.CPUCores > a.total.CPUCores {
		return false
	}
	if a.total.RAMGB > 0 && a.allocated.RAMGB+u.RAMGB > a.total.RAMGB {
		return false
	}
	if a.total.GPUVRAMGB > 0 && a.allocated.GPUVRAMGB+u.GPUVRAMGB > a.total.GPUVRAMGB {
		return false
	}
	a.allocated.CPUCores += u.CPUCores
	a.allocated.RAMGB += u.RAMGB
	a.allocated.GPUVRAMGB += u.GPUVRAMGB
	a.running++
	return true
}

// Release returns a task's usage to the pool.
func (a *Account) Release(u Usage) {
	a.allocated.CPUCores -= u.CPUCores
	a.allocated.RAMGB -= u.RAMGB
	a.allocated.GPUVRAMGB -= u.GPUVRAMGB
	if a.running > 0 {
		a.running--
	}
}

// Running reports how many tasks currently hold capacity.
func (a *Account) Running() int { return a.running }

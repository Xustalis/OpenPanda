package defense

import (
	"sort"
	"sync"
	"time"
)

// CircuitState is the lifecycle state of a single circuit.
type CircuitState string

const (
	CircuitClosed   CircuitState = "closed"
	CircuitOpen     CircuitState = "open"
	CircuitHalfOpen CircuitState = "half_open"
)

// CircuitBreaker trips a circuit open after a run of failures and re-closes it
// only after a cooldown and one successful trial (design doc §14 Layer 1, plan
// P2-27). Keyed by a dependency such as "agent:claude", it stops routing work
// to something that is failing repeatedly, instead of failing the whole task
// loop one request at a time.
//
// The breaker is in-memory. Cooldowns are short by design, so a daemon restart
// simply re-seats every circuit to closed — the safe default for an MVP.
type CircuitBreaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	states    map[string]*circuitState
}

// circuitState tracks one key. failures counts consecutive failures since the
// last success; openedAt is set when the circuit goes open (zero otherwise).
type circuitState struct {
	state    CircuitState
	failures int
	openedAt time.Time
}

// NewCircuitBreaker builds a breaker. A non-positive threshold defaults to 3
// consecutive failures; a non-positive cooldown defaults to 30s.
func NewCircuitBreaker(threshold int, cooldown time.Duration) *CircuitBreaker {
	if threshold <= 0 {
		threshold = 3
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	return &CircuitBreaker{threshold: threshold, cooldown: cooldown, states: make(map[string]*circuitState)}
}

// Allow reports whether key may run. It returns false while the circuit is
// open. The first call after the cooldown elapses transitions to half-open and
// permits exactly one trial; further calls stay blocked until that trial is
// reported via RecordSuccess or RecordFailure.
func (b *CircuitBreaker) Allow(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.states[key]
	if st == nil || st.state == CircuitClosed {
		return true
	}
	if st.state == CircuitOpen {
		if time.Since(st.openedAt) >= b.cooldown {
			st.state = CircuitHalfOpen
			return true
		}
		return false
	}
	// Half-open: a trial is already in flight; admit no others until it resolves.
	return false
}

// RecordSuccess closes the circuit for key and resets the failure count. A
// closed circuit with zero failures is the same as an absent entry, so it is
// deleted rather than left to accumulate forever in a long-lived daemon (D29).
func (b *CircuitBreaker) RecordSuccess(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.states, key)
}

// RecordFailure counts a consecutive failure and opens the circuit once the
// threshold is reached. A failure during half-open immediately re-opens it. It
// reports whether the circuit transitioned to open (so callers can audit the
// trip).
func (b *CircuitBreaker) RecordFailure(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.states[key]
	if st == nil {
		st = &circuitState{state: CircuitClosed}
		b.states[key] = st
	}
	st.failures++
	if st.failures >= b.threshold {
		st.state = CircuitOpen
		st.openedAt = time.Now()
		return true
	}
	return false
}

// State exposes the current state for diagnostics and tests. An unknown key
// reports closed.
func (b *CircuitBreaker) State(key string) CircuitState {
	b.mu.Lock()
	defer b.mu.Unlock()
	if st := b.states[key]; st != nil {
		return st.state
	}
	return CircuitClosed
}

// Blocked reports whether key is currently unavailable for new work: the
// circuit is open within its cooldown, or a half-open trial is already in
// flight. Unlike Allow it never mutates state, so callers that are merely
// filtering candidates (e.g. dropping a fallback agent before routing) can
// consult it without spending a half-open trial slot on an agent they may
// not end up running.
func (b *CircuitBreaker) Blocked(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.states[key]
	if st == nil || st.state == CircuitClosed {
		return false
	}
	if st.state == CircuitOpen {
		return time.Since(st.openedAt) < b.cooldown
	}
	// Half-open: a trial is already in flight, so the circuit is not clear.
	return true
}

// BlockedKeys enumerates every key currently unavailable for new work (same
// predicate as Blocked), sorted for deterministic wire encoding. Callers
// publish this so OTHER nodes' routing can weigh failure history too — the
// breaker is per-process memory, and without the publication a delegator
// only learns a peer's agent is circuit-open by bouncing a decline off it.
func (b *CircuitBreaker) BlockedKeys() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	now := time.Now()
	for key, st := range b.states {
		switch st.state {
		case CircuitClosed:
			continue
		case CircuitOpen:
			if now.Sub(st.openedAt) < b.cooldown {
				out = append(out, key)
			}
		case CircuitHalfOpen:
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

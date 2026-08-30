package defense

import (
	"reflect"
	"testing"
	"time"
)

func TestCircuitBreakerOpensAfterThreshold(t *testing.T) {
	b := NewCircuitBreaker(3, 30*time.Second)

	for i := 0; i < 2; i++ {
		if !b.Allow("agent:claude") {
			t.Fatalf("failure %d: still closed, want allow", i+1)
		}
		b.RecordFailure("agent:claude")
	}
	if b.State("agent:claude") != CircuitClosed {
		t.Fatalf("state after 2 failures = %s, want closed", b.State("agent:claude"))
	}

	// Third failure opens the circuit.
	b.Allow("agent:claude")
	b.RecordFailure("agent:claude")
	if b.State("agent:claude") != CircuitOpen {
		t.Fatalf("state after 3 failures = %s, want open", b.State("agent:claude"))
	}
	if b.Allow("agent:claude") {
		t.Fatalf("open circuit must reject requests during cooldown")
	}
}

func TestCircuitBreakerHalfOpenRecovers(t *testing.T) {
	b := NewCircuitBreaker(1, time.Millisecond) // open on first failure, tiny cooldown

	if !b.Allow("agent:claude") {
		t.Fatalf("closed circuit must allow")
	}
	b.RecordFailure("agent:claude")
	if b.Allow("agent:claude") {
		t.Fatalf("open circuit must reject before cooldown")
	}

	// Let the cooldown elapse: exactly one half-open trial is admitted.
	time.Sleep(2 * time.Millisecond)
	if !b.Allow("agent:claude") {
		t.Fatalf("first call after cooldown must admit a half-open trial")
	}
	if b.Allow("agent:claude") {
		t.Fatalf("half-open circuit must admit only one trial")
	}

	b.RecordSuccess("agent:claude")
	if b.State("agent:claude") != CircuitClosed {
		t.Fatalf("state after half-open success = %s, want closed", b.State("agent:claude"))
	}
	if !b.Allow("agent:claude") {
		t.Fatalf("recovered circuit must allow")
	}
}

func TestCircuitBreakerHalfOpenFailureReopens(t *testing.T) {
	b := NewCircuitBreaker(1, time.Millisecond)

	b.Allow("agent:claude")
	b.RecordFailure("agent:claude")
	time.Sleep(2 * time.Millisecond)

	if !b.Allow("agent:claude") {
		t.Fatalf("first call after cooldown must admit a half-open trial")
	}
	b.RecordFailure("agent:claude")
	if b.State("agent:claude") != CircuitOpen {
		t.Fatalf("half-open failure must re-open, got %s", b.State("agent:claude"))
	}
}

func TestCircuitBreakerKeysAreIndependent(t *testing.T) {
	b := NewCircuitBreaker(1, time.Second)

	b.Allow("agent:claude")
	b.RecordFailure("agent:claude")

	if b.Allow("agent:claude") {
		t.Fatalf("failed key must be open")
	}
	if !b.Allow("agent:opencode") {
		t.Fatalf("a different key must be unaffected")
	}
}

func TestCircuitBreakerDefaults(t *testing.T) {
	b := NewCircuitBreaker(0, 0)
	if b.threshold != 3 {
		t.Fatalf("default threshold = %d, want 3", b.threshold)
	}
	if b.cooldown != 30*time.Second {
		t.Fatalf("default cooldown = %s, want 30s", b.cooldown)
	}
}

func TestCircuitBreakerEvictsOnSuccess(t *testing.T) {
	b := NewCircuitBreaker(1, time.Millisecond)
	b.Allow("agent:claude")
	b.RecordFailure("agent:claude")
	if len(b.states) != 1 {
		t.Fatalf("states after failure = %d, want 1", len(b.states))
	}
	b.RecordSuccess("agent:claude")
	if len(b.states) != 0 {
		t.Fatalf("states after success = %d, want 0 (D29 eviction)", len(b.states))
	}
	if b.State("agent:claude") != CircuitClosed {
		t.Fatalf("evicted key must read closed")
	}
}

func TestCircuitBreakerBlockedIsReadOnly(t *testing.T) {
	b := NewCircuitBreaker(1, time.Millisecond)
	if b.Blocked("agent:claude") {
		t.Fatalf("unknown key must not be blocked")
	}

	b.Allow("agent:claude")
	b.RecordFailure("agent:claude")
	if !b.Blocked("agent:claude") {
		t.Fatalf("open circuit within cooldown must be blocked")
	}
	if b.State("agent:claude") != CircuitOpen {
		t.Fatalf("Blocked must not transition open -> half-open, got %s", b.State("agent:claude"))
	}

	// Cooldown elapses: Blocked must clear (the circuit may now admit a
	// half-open trial), still without mutating it.
	time.Sleep(2 * time.Millisecond)
	if b.Blocked("agent:claude") {
		t.Fatalf("open circuit after cooldown must not be blocked")
	}
	if b.State("agent:claude") != CircuitOpen {
		t.Fatalf("Blocked after cooldown must leave state open, got %s", b.State("agent:claude"))
	}
}

func TestCircuitBreakerBlockedKeys(t *testing.T) {
	b := NewCircuitBreaker(1, time.Hour)
	if got := b.BlockedKeys(); len(got) != 0 {
		t.Fatalf("fresh breaker has no blocked keys, got %v", got)
	}

	// Trip two circuits open; the enumeration must list both, sorted.
	b.Allow("agent:codex")
	b.RecordFailure("agent:codex")
	b.Allow("agent:claude")
	b.RecordFailure("agent:claude")
	if got, want := b.BlockedKeys(), []string{"agent:claude", "agent:codex"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("BlockedKeys = %v, want %v", got, want)
	}

	// A success closes the circuit and drops the key from the enumeration.
	b.RecordSuccess("agent:claude")
	if got, want := b.BlockedKeys(), []string{"agent:codex"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("BlockedKeys after success = %v, want %v", got, want)
	}

	// Once the cooldown elapses the open circuit stops being blocked, so it
	// must also leave the enumeration (peers may route a half-open trial).
	short := NewCircuitBreaker(1, time.Millisecond)
	short.Allow("agent:claude")
	short.RecordFailure("agent:claude")
	time.Sleep(2 * time.Millisecond)
	if got := short.BlockedKeys(); len(got) != 0 {
		t.Fatalf("BlockedKeys after cooldown = %v, want none", got)
	}
}

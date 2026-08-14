package defense

import (
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

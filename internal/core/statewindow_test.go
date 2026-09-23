package core

import "testing"

func TestStateOscillates(t *testing.T) {
	c := &Core{}
	// A fresh sequence never flags.
	if c.stateOscillates("p", "h1") {
		t.Fatal("first state flagged")
	}
	if c.stateOscillates("p", "h2") {
		t.Fatal("progress flagged")
	}
	if c.stateOscillates("p", "h3") {
		t.Fatal("progress flagged")
	}
	// Re-observing the latest state is stagnation, not oscillation.
	if c.stateOscillates("p", "h3") {
		t.Fatal("unchanged state flagged")
	}
	// Returning to an earlier state is the regression the check exists for.
	if !c.stateOscillates("p", "h1") {
		t.Fatal("regression to h1 not flagged")
	}
	if !c.stateOscillates("p", "h2") {
		t.Fatal("regression to h2 not flagged")
	}
}

func TestStateOscillatesWindowBounds(t *testing.T) {
	c := &Core{}
	for i := 0; i < stateWindowCap+3; i++ {
		c.stateOscillates("p", string(rune('a'+i)))
	}
	if got := len(c.stateWin["p"]); got != stateWindowCap {
		t.Fatalf("window len = %d, want %d", got, stateWindowCap)
	}
}

func TestStateOscillatesKeyIsolation(t *testing.T) {
	c := &Core{}
	c.stateOscillates("a", "h1")
	c.stateOscillates("a", "h2")
	// The same hash under a different key is a different project's state.
	if c.stateOscillates("b", "h1") {
		t.Fatal("state from key a flagged under key b")
	}
	// Empty key/hash inputs are no-ops.
	if c.stateOscillates("", "h1") || c.stateOscillates("c", "") {
		t.Fatal("empty input flagged")
	}
}

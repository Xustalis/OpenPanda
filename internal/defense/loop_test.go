package defense

import "testing"

func TestLoopDetectorBudget(t *testing.T) {
	d := NewLoopDetector(2)
	// First two failures are retryable; the third is not.
	if !d.Allow("a") {
		t.Error("1st failure should allow retry")
	}
	if !d.Allow("a") {
		t.Error("2nd failure should allow retry")
	}
	if d.Allow("a") {
		t.Error("3rd failure should pause")
	}
	// An independent task has its own budget.
	if !d.Allow("b") {
		t.Error("independent task should allow retry")
	}
}

func TestLoopDetectorZero(t *testing.T) {
	d := NewLoopDetector(0)
	if d.Allow("a") {
		t.Error("max 0 should never allow retry")
	}
}

func TestLoopDetectorNegativeDefaultsToZero(t *testing.T) {
	d := NewLoopDetector(-5)
	if d.Allow("a") {
		t.Error("negative max should behave as zero")
	}
}

func TestLoopDetectorReset(t *testing.T) {
	d := NewLoopDetector(1)
	d.Allow("a") // 1st failure, retryable
	d.Allow("a") // 2nd failure, exhausted
	if d.Allow("a") {
		t.Error("exhausted task should pause")
	}
	d.Reset("a")
	if !d.Allow("a") {
		t.Error("reset should restore the budget")
	}
}

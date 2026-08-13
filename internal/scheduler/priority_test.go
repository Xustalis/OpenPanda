package scheduler

import "testing"

func TestPriority(t *testing.T) {
	base := PriorityInput{UserPriority: 5, SchedulerTier: 5, WaitTimeSeconds: 0, ResourceEfficiency: 0.5}
	baseScore := Priority(base)

	// Higher user priority raises the score.
	if got := Priority(PriorityInput{UserPriority: 10, SchedulerTier: 5, ResourceEfficiency: 0.5}); got <= baseScore {
		t.Fatalf("higher user priority should raise score: %v <= %v", got, baseScore)
	}
	// Longer wait raises the score (anti-starvation).
	if got := Priority(PriorityInput{UserPriority: 5, SchedulerTier: 5, WaitTimeSeconds: 100, ResourceEfficiency: 0.5}); got <= baseScore {
		t.Fatalf("longer wait should raise score: %v <= %v", got, baseScore)
	}
	// Zero user priority uses the default 5.
	if got := Priority(PriorityInput{SchedulerTier: 5, ResourceEfficiency: 0.5}); got != baseScore {
		t.Fatalf("zero user priority should default to 5: %v != %v", got, baseScore)
	}
}

func TestPriorityDeterministic(t *testing.T) {
	a := Priority(PriorityInput{UserPriority: 3, SchedulerTier: 1, WaitTimeSeconds: 7, ResourceEfficiency: 0.9})
	b := Priority(PriorityInput{UserPriority: 3, SchedulerTier: 1, WaitTimeSeconds: 7, ResourceEfficiency: 0.9})
	if a != b {
		t.Fatalf("Priority must be deterministic: %v != %v", a, b)
	}
}

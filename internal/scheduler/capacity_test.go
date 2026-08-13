package scheduler

import "testing"

func TestAccountAcquireRelease(t *testing.T) {
	a := NewAccount(Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3})

	if !a.TryAcquire(Usage{CPUCores: 4, RAMGB: 8}) {
		t.Fatal("first task should fit")
	}
	if !a.TryAcquire(Usage{CPUCores: 4, RAMGB: 8}) {
		t.Fatal("second task should fit (4+4 <= 8)")
	}
	// Third task would exceed CPU: 4+4+4 > 8.
	if a.TryAcquire(Usage{CPUCores: 4, RAMGB: 1}) {
		t.Fatal("third task must not fit (cpu 12 > 8)")
	}
	if a.Running() != 2 {
		t.Fatalf("running = %d, want 2", a.Running())
	}

	a.Release(Usage{CPUCores: 4, RAMGB: 8})
	if !a.TryAcquire(Usage{CPUCores: 4, RAMGB: 1}) {
		t.Fatal("task should fit after release")
	}
	if a.Running() != 2 {
		t.Fatalf("running = %d, want 2", a.Running())
	}
}

func TestAccountMaxConcurrent(t *testing.T) {
	// Unlimited CPU/RAM, but a hard cap of 1 concurrent task.
	a := NewAccount(Capacity{MaxConcurrent: 1})
	if !a.TryAcquire(Usage{CPUCores: 1}) {
		t.Fatal("first task should fit")
	}
	if a.TryAcquire(Usage{CPUCores: 1}) {
		t.Fatal("second task must be refused at MaxConcurrent=1")
	}
}

func TestAccountUnboundedDimension(t *testing.T) {
	// Zero CPU means unconstrained; only RAM gates.
	a := NewAccount(Capacity{RAMGB: 8})
	if !a.TryAcquire(Usage{CPUCores: 999, RAMGB: 4}) {
		t.Fatal("unbounded cpu should not gate")
	}
	if a.TryAcquire(Usage{RAMGB: 5}) {
		t.Fatal("ram 4+5 > 8 must gate")
	}
}

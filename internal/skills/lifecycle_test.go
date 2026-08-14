package skills

import (
	"testing"
	"time"
)

func TestShouldCreate(t *testing.T) {
	// Never create when an equivalent skill already exists.
	if ShouldCreate(Stats{Attempts: 5, Successes: 5}, true) {
		t.Errorf("existing skill should suppress creation")
	}
	// Never create from zero successes.
	if ShouldCreate(Stats{Attempts: 5}, false) {
		t.Errorf("zero successes should suppress creation")
	}
	// MUSE quality gate: >=3 attempts, >=70% success.
	if !ShouldCreate(Stats{Attempts: 4, Successes: 3}, false) {
		t.Errorf("3+ attempts at 75%% should create")
	}
	// Exactly 70% passes.
	if !ShouldCreate(Stats{Attempts: 10, Successes: 7}, false) {
		t.Errorf("70%% success should create")
	}
	// Below the attempt gate.
	if ShouldCreate(Stats{Attempts: 2, Successes: 1}, false) {
		t.Errorf("below attempt gate should not create")
	}
	// At the attempt gate but below the success-rate gate.
	if ShouldCreate(Stats{Attempts: 4, Successes: 2}, false) {
		t.Errorf("50%% success should not create")
	}
}

func TestAdvanceLifecycle(t *testing.T) {
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	sk := &Skill{Name: "x", Status: StatusActive, LastUsed: now}

	if got := Advance(sk, now.Add(10*24*time.Hour)); got != StatusActive {
		t.Errorf("10 days idle = %v, want active", got)
	}
	if got := Advance(sk, now.Add(31*24*time.Hour)); got != StatusDormant {
		t.Errorf("31 days idle = %v, want dormant", got)
	}
	if got := Advance(sk, now.Add(91*24*time.Hour)); got != StatusExpired {
		t.Errorf("91 days idle = %v, want expired", got)
	}
	// Pending skills are untouched by the idle clock.
	pending := &Skill{Name: "p", Status: StatusPending}
	if got := Advance(pending, now.Add(200*24*time.Hour)); got != StatusPending {
		t.Errorf("pending = %v, want pending", got)
	}
}

func TestRecordUse(t *testing.T) {
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	sk := &Skill{Name: "x", Status: StatusDormant}
	sk.RecordUse(true, now)
	if sk.UseCount != 1 || sk.SuccessCount != 1 || sk.Status != StatusActive {
		t.Errorf("record use: %+v", sk)
	}
	sk.RecordUse(false, now)
	if sk.UseCount != 2 || sk.SuccessCount != 1 {
		t.Errorf("failed use should not bump success: %+v", sk)
	}
}

func TestApproveAndReject(t *testing.T) {
	store := NewStore(t.TempDir())
	pending := &Skill{Name: "draft", Description: "d", Scope: ScopeGlobal, Status: StatusPending, Body: "x"}
	if err := store.Save(pending); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Rejecting an active (non-pending) skill must fail.
	active := &Skill{Name: "live", Description: "d", Scope: ScopeGlobal, Status: StatusActive}
	if err := store.Save(active); err != nil {
		t.Fatalf("save active: %v", err)
	}
	if err := store.Reject(ScopeGlobal, "", "live"); err == nil {
		t.Errorf("rejecting active skill should error")
	}

	if err := store.Approve(ScopeGlobal, "", "draft"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	got, _ := store.Load(ScopeGlobal, "", "draft")
	if got == nil || got.Status != StatusActive {
		t.Errorf("approved skill = %+v, want active", got)
	}
	// Approving again must fail (already active).
	if err := store.Approve(ScopeGlobal, "", "draft"); err == nil {
		t.Errorf("approving non-pending should error")
	}
}

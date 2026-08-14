package skills

import (
	"strings"
	"testing"
)

func TestClassKey(t *testing.T) {
	if got := ClassKey([]string{"build:macos", "lint"}); got != "build:macos+lint" {
		t.Errorf("ClassKey = %q, want sorted join", got)
	}
}

func TestGenerate(t *testing.T) {
	records := []Record{
		{Title: "build the core", Success: true},
		{Title: "build the core again", Success: false},
	}
	sk := Generate(ScopeGlobal, "", "lint+build", "build the core", records)
	if sk.Name == "" || sk.Status != StatusPending || sk.Description != "build the core" {
		t.Errorf("generate fields: %+v", sk)
	}
	if !strings.Contains(sk.Body, "1 次成功 / 共 2 次执行") {
		t.Errorf("body should carry the success summary, got %q", sk.Body)
	}
}

func TestTrackerGeneratesSkill(t *testing.T) {
	store := NewStore(t.TempDir())
	tracker := NewTracker(store)
	abilities := []string{"lint", "build"}

	// Below the >=3 gate: no skill yet.
	for i := 0; i < 2; i++ {
		sk, err := tracker.Record("panda", abilities, "build panda", true)
		if err != nil || sk != nil {
			t.Fatalf("run %d: sk=%v err=%v, want nil nil", i, sk, err)
		}
	}
	// Third success clears the gate and generates a pending project skill.
	sk, err := tracker.Record("panda", abilities, "build panda", true)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if sk == nil || sk.Status != StatusPending || sk.Scope != ScopeProject || sk.Project != "panda" {
		t.Fatalf("want pending project skill, got %+v", sk)
	}
	got, _ := store.Load(ScopeProject, "panda", sk.Name)
	if got == nil || got.Status != StatusPending {
		t.Errorf("generated skill not persisted: %+v", got)
	}
}

func TestTrackerNoDuplicate(t *testing.T) {
	store := NewStore(t.TempDir())
	tracker := NewTracker(store)
	for i := 0; i < 3; i++ {
		if _, err := tracker.Record("", []string{"a"}, "t", true); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	// The class already has a pending skill: no duplicate.
	sk, err := tracker.Record("", []string{"a"}, "t", true)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if sk != nil {
		t.Errorf("existing skill should suppress duplicate generation")
	}
}

func TestTrackerBelowSuccessRate(t *testing.T) {
	store := NewStore(t.TempDir())
	tracker := NewTracker(store)
	// 4 runs, 1 success = 25% — below the 70% gate.
	for i, ok := range []bool{true, false, false, false} {
		sk, err := tracker.Record("", []string{"x"}, "t", ok)
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		if sk != nil {
			t.Errorf("run %d should not generate (low success rate)", i)
		}
	}
}

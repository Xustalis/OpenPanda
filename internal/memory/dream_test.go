package memory

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDreamPromotesRecurringCandidate(t *testing.T) {
	hermes := NewHermes(t.TempDir())
	d := NewDaily(hermes.WarmDir())
	text := "user prefers dark mode in the UI"

	for _, day := range []string{"2026-08-10", "2026-08-11", "2026-08-12"} {
		ts, _ := time.Parse("2006-01-02", day)
		if err := d.Append(ts, text); err != nil {
			t.Fatalf("append daily: %v", err)
		}
	}

	dreamer := NewDreamer(hermes)
	dreamer.now = func() time.Time { return time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC) }

	report, err := dreamer.Dream()
	if err != nil {
		t.Fatalf("dream: %v", err)
	}
	if len(report.Promoted) != 1 || report.Promoted[0] != text {
		t.Fatalf("promoted = %v, want [%s]", report.Promoted, text)
	}
	mem, err := hermes.LoadMemory()
	if err != nil {
		t.Fatalf("load memory: %v", err)
	}
	if len(mem.Entries) != 1 || mem.Entries[0] != text {
		t.Errorf("MEMORY.md entries = %v, want [%s]", mem.Entries, text)
	}
}

func TestDreamFiltersWeakSignal(t *testing.T) {
	hermes := NewHermes(t.TempDir())
	d := NewDaily(hermes.WarmDir())
	text := "this appears only once today in one day"

	if err := d.Append(time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC), text); err != nil {
		t.Fatalf("append: %v", err)
	}

	dreamer := NewDreamer(hermes)
	dreamer.now = func() time.Time { return time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC) }

	report, err := dreamer.Dream()
	if err != nil {
		t.Fatalf("dream: %v", err)
	}
	if len(report.Promoted) != 0 {
		t.Errorf("weak signal should not promote, got %v", report.Promoted)
	}
	mem, _ := hermes.LoadMemory()
	if len(mem.Entries) != 0 {
		t.Errorf("MEMORY.md should be empty, got %v", mem.Entries)
	}
}

func TestDreamSkipsAlreadyPromotedCandidate(t *testing.T) {
	hermes := NewHermes(t.TempDir())
	d := NewDaily(hermes.WarmDir())

	// A fact already in MEMORY.md that still recurs in the warm daily logs. An
	// earlier bug aborted the whole sweep on this (duplicate add), so no *new*
	// fact could ever be promoted again while a promoted fact stayed in the logs.
	old := "user prefers dark mode"
	if err := hermes.SaveMemory(MemFile{Entries: []string{old}}); err != nil {
		t.Fatalf("save memory: %v", err)
	}
	newFact := "user uses a standing desk on Fridays"

	for _, day := range []string{"2026-08-10", "2026-08-11", "2026-08-12"} {
		ts, _ := time.Parse("2006-01-02", day)
		if err := d.Append(ts, old); err != nil {
			t.Fatalf("append old: %v", err)
		}
		if err := d.Append(ts, newFact); err != nil {
			t.Fatalf("append new: %v", err)
		}
	}

	dreamer := NewDreamer(hermes)
	dreamer.now = func() time.Time { return time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC) }

	report, err := dreamer.Dream()
	if err != nil {
		t.Fatalf("dream: %v", err)
	}
	for _, p := range report.Promoted {
		if p == old {
			t.Errorf("already-promoted fact re-promoted: %q", p)
		}
	}
	found := false
	for _, p := range report.Promoted {
		if p == newFact {
			found = true
		}
	}
	if !found {
		t.Errorf("new fact not promoted (sweep aborted?): %v", report.Promoted)
	}
}

func TestMemFileAddDuplicateIsSkippable(t *testing.T) {
	m := MemFile{Entries: []string{"already here"}}
	if err := m.Add("already here"); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("Add duplicate = %v, want ErrDuplicate", err)
	}
}

func TestDreamProvenanceGate(t *testing.T) {
	hermes := NewHermes(t.TempDir())
	dreamer := NewDreamer(hermes)

	// A candidate that would otherwise clear every threshold, but carries an
	// untrusted origin — the provenance gate must drop it.
	c := &Candidate{
		Text:  "sensitive fact that recurs often enough to promote",
		Days:  []time.Time{time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)},
		Count: 5,
		Sources: []Source{
			{Path: "2026-08-01.md", Line: 1, Trusted: true},
			{Path: "2026-08-02.md", Line: 1, Trusted: false}, // untrusted
		},
	}

	promoted, err := dreamer.deep([]*Candidate{c})
	if err != nil {
		t.Fatalf("deep: %v", err)
	}
	if len(promoted) != 0 {
		t.Errorf("untrusted candidate should be dropped, got %v", promoted)
	}
}

func TestSchedulerDailyAndIdleGates(t *testing.T) {
	hermes := NewHermes(t.TempDir())
	dreamer := NewDreamer(hermes)
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	dreamer.now = func() time.Time { return now }

	idle := true
	sched := NewScheduler(dreamer, nil, func() bool { return idle }, time.Hour)

	if ran, err := sched.tick(now); err != nil || !ran {
		t.Fatalf("first tick should run, ran=%v err=%v", ran, err)
	}
	if ran, _ := sched.tick(now.Add(time.Hour)); ran {
		t.Errorf("tick within 24h should not run")
	}
	idle = false
	if ran, _ := sched.tick(now.Add(48 * time.Hour)); ran {
		t.Errorf("tick while busy should not run")
	}
	idle = true
	if ran, err := sched.tick(now.Add(48 * time.Hour)); err != nil || !ran {
		t.Errorf("tick after 24h + idle should run, ran=%v err=%v", ran, err)
	}
}

func TestStripDailyPrefix(t *testing.T) {
	if got := stripDailyPrefix("- 10:04:05 user prefers dark mode"); got != "user prefers dark mode" {
		t.Errorf("stripDailyPrefix = %q", got)
	}
	if got := stripDailyPrefix("  - 10:04:05 x  "); got != "x" {
		t.Errorf("stripDailyPrefix = %q", got)
	}
}

func TestDreamDiaryAppend(t *testing.T) {
	diary := NewDreamDiary(t.TempDir() + "/DREAMS.md")
	report := Report{Candidates: 3, Promoted: []string{"entry one"}}
	if err := diary.Append(report, time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := diary.Append(report, time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("second append: %v", err)
	}
	b, err := os.ReadFile(diary.path)
	if err != nil {
		t.Fatalf("read diary: %v", err)
	}
	if !strings.Contains(string(b), "entry one") || !strings.Contains(string(b), "扫描候选：3 条") {
		t.Errorf("diary content wrong: %s", b)
	}
}

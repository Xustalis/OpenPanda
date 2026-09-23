package reminders

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/storage"
)

// testDB opens a temporary SQLite database and runs all migrations so the
// reminders table (v8) is available. The caller must close the returned *sql.DB.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := storage.Migrate(db); err != nil {
		db.Close()
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestAdd_EmptyMessage(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	s := NewStore(db)

	_, err := s.Add(context.Background(), "", time.Now().Add(time.Hour), "cli")
	if err == nil {
		t.Fatal("expected error for empty message, got nil")
	}
}

func TestAdd_DefaultSource(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	s := NewStore(db)

	r, err := s.Add(context.Background(), "hello", time.Now().Add(time.Hour), "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if r.Source != "cli" {
		t.Errorf("expected default source 'cli', got %q", r.Source)
	}
	if r.ID == 0 {
		t.Error("expected non-zero ID")
	}
}

func TestList_PendingAndFired(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()

	// Add two reminders: one due now, one in the future.
	_, _ = s.Add(ctx, "due now", time.Now().Add(-time.Minute), "cli")
	_, _ = s.Add(ctx, "future", time.Now().Add(time.Hour), "tool")

	// List pending only.
	pending, err := s.List(ctx, false)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending, got %d", len(pending))
	}
	// Due-first ordering.
	if pending[0].Message != "due now" {
		t.Errorf("expected 'due now' first, got %q", pending[0].Message)
	}

	// Claim the due one.
	claimed, err := s.ClaimDue(ctx, time.Now(), 50)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 1 || claimed[0].Message != "due now" {
		t.Fatalf("expected 1 claimed 'due now', got %+v", claimed)
	}

	// List pending again — only the future one remains.
	pending, _ = s.List(ctx, false)
	if len(pending) != 1 || pending[0].Message != "future" {
		t.Errorf("expected 1 pending 'future', got %+v", pending)
	}

	// List including fired.
	all, err := s.List(ctx, true)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 total (pending + fired), got %d", len(all))
	}
}

func TestDelete(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()

	r, _ := s.Add(ctx, "to delete", time.Now().Add(time.Hour), "cli")

	ok, err := s.Delete(ctx, r.ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !ok {
		t.Error("expected delete to return true")
	}

	// Deleting a non-existent ID returns false.
	ok, err = s.Delete(ctx, 99999)
	if err != nil {
		t.Fatalf("delete non-existent: %v", err)
	}
	if ok {
		t.Error("expected false for non-existent ID")
	}
}

func TestClaimDue_LimitAndAtomicity(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()

	// Add 5 due reminders.
	for i := range 5 {
		_, err := s.Add(ctx, "r"+string(rune('a'+i)), time.Now().Add(-time.Minute), "cli")
		if err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}

	// Claim with limit 2 — only 2 should be returned.
	claimed, err := s.ClaimDue(ctx, time.Now(), 2)
	if err != nil {
		t.Fatalf("claim limit: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("expected 2 claimed, got %d", len(claimed))
	}

	// Claim again — the remaining 3 are due.
	claimed2, err := s.ClaimDue(ctx, time.Now(), 50)
	if err != nil {
		t.Fatalf("claim rest: %v", err)
	}
	if len(claimed2) != 3 {
		t.Fatalf("expected 3 remaining, got %d", len(claimed2))
	}

	// No more due.
	claimed3, err := s.ClaimDue(ctx, time.Now(), 50)
	if err != nil {
		t.Fatalf("claim empty: %v", err)
	}
	if len(claimed3) != 0 {
		t.Errorf("expected 0, got %d", len(claimed3))
	}
}

func TestClaimDue_ConcurrentDisjoint(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()

	// Add 10 due reminders.
	for i := range 10 {
		_, err := s.Add(ctx, "concurrent"+string(rune('a'+i)), time.Now().Add(-time.Minute), "cli")
		if err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}

	// Two concurrent ClaimDue callers should get disjoint sets.
	var mu sync.Mutex
	var allClaimed []Reminder
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := s.ClaimDue(ctx, time.Now(), 50)
			if err != nil {
				t.Errorf("concurrent claim: %v", err)
				return
			}
			mu.Lock()
			allClaimed = append(allClaimed, claimed...)
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(allClaimed) != 10 {
		t.Errorf("expected 10 total claimed across both callers, got %d", len(allClaimed))
	}
	// Check uniqueness — no reminder claimed twice.
	seen := map[int64]bool{}
	for _, r := range allClaimed {
		if seen[r.ID] {
			t.Errorf("reminder %d claimed by both callers", r.ID)
		}
		seen[r.ID] = true
	}
}

func TestScanner_RunFiresDueReminders(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, _ = s.Add(ctx, "scanner test", time.Now().Add(-time.Second), "web")

	var fired []Reminder
	var mu sync.Mutex
	sc := NewScanner(s, 50*time.Millisecond, func(r Reminder) {
		mu.Lock()
		fired = append(fired, r)
		mu.Unlock()
	}, nil)

	go sc.Run(ctx)

	// Wait for the scanner to pick it up.
	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		n := len(fired)
		mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("scanner did not fire reminder within 3s")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	cancel()

	mu.Lock()
	defer mu.Unlock()
	if fired[0].Message != "scanner test" {
		t.Errorf("expected 'scanner test', got %q", fired[0].Message)
	}
}

func TestClaimDue_RecurringReschedules(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()

	base := time.Now().Add(-time.Minute).Unix()
	r, err := s.AddEvery(ctx, "standup", time.Unix(base, 0), 30*time.Second, "cli")
	if err != nil {
		t.Fatalf("add recurring: %v", err)
	}
	if r.RepeatSeconds != 30 {
		t.Fatalf("expected repeat_seconds=30, got %d", r.RepeatSeconds)
	}

	// First claim fires once and pushes due_at to the next slot after now.
	claimed, err := s.ClaimDue(ctx, time.Now(), 50)
	if err != nil {
		t.Fatalf("claim recurring: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("expected 1 claimed, got %d", len(claimed))
	}
	if claimed[0].DueAt != base {
		t.Fatalf("claim should report the occurrence that fired (%d), got %d", base, claimed[0].DueAt)
	}

	// The row is still pending with a future due_at — not retired.
	pending, err := s.List(ctx, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pending) != 1 || pending[0].RepeatSeconds != 30 {
		t.Fatalf("recurring row should stay pending, got %+v", pending)
	}
	if pending[0].DueAt <= time.Now().Unix() {
		t.Fatalf("due_at should be rescheduled into the future, got %d", pending[0].DueAt)
	}
	if pending[0].FiredAt == 0 {
		t.Fatal("fired_at should record the last fire time")
	}

	// Nothing is due right now — the next claim finds it empty.
	claimed2, err := s.ClaimDue(ctx, time.Now(), 50)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(claimed2) != 0 {
		t.Fatalf("expected no immediate refire, got %d", len(claimed2))
	}
}

func TestClaimDue_RecurringConcurrentDisjoint(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()

	_, _ = s.AddEvery(ctx, "recurring", time.Now().Add(-time.Minute), time.Minute, "cli")

	var mu sync.Mutex
	total := 0
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := s.ClaimDue(ctx, time.Now(), 50)
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			mu.Lock()
			total += len(claimed)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if total != 1 {
		t.Fatalf("a recurring reminder must fire exactly once across scanners, got %d", total)
	}
}

func TestNextDue(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()

	if _, ok := s.NextDue(ctx); ok {
		t.Fatal("empty board should report no next due")
	}

	future := time.Now().Add(time.Hour)
	_, _ = s.Add(ctx, "later", future, "cli")
	next, ok := s.NextDue(ctx)
	if !ok || !next.Equal(future.Truncate(time.Second)) {
		t.Fatalf("expected next due %v, got %v (ok=%v)", future, next, ok)
	}

	// A recurring row always has a next occurrence.
	past := time.Now().Add(-time.Minute)
	_, _ = s.AddEvery(ctx, "recurring", past, time.Minute, "cli")
	next2, ok := s.NextDue(ctx)
	if !ok || !next2.Before(future) {
		t.Fatalf("recurring next due should precede the far one-shot, got %v", next2)
	}
}

func TestScanner_SleepForAdaptive(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()

	sc := NewScanner(s, time.Minute, nil, nil)

	// Empty board → the polling ceiling.
	if d := sc.sleepFor(ctx); d != time.Minute {
		t.Fatalf("empty board should sleep the full cadence, got %v", d)
	}

	// A near-future reminder shrinks the sleep to its due time.
	_, _ = s.Add(ctx, "soon", time.Now().Add(5*time.Second), "cli")
	d := sc.sleepFor(ctx)
	if d < 4*time.Second || d > 6*time.Second {
		t.Fatalf("expected ~5s sleep for near reminder, got %v", d)
	}
}

// SPDX-License-Identifier: AGPL-3.0-or-later

package reminders

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/testdb"
	"modernc.org/sqlite"
)

// testDB returns a private file-backed database with the current schema.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	return testdb.OpenFile(t, filepath.Join(dir, "test.db"))
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

// A batch that fails after some rows were claimed must still deliver them:
// those rows are already stamped fired_at, so a later scan never sees them.
func TestScanOnceFiresClaimedRowsWhenBatchFails(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()

	first, err := store.Add(ctx, "first due", time.Now().Add(-2*time.Minute), "cli")
	if err != nil {
		t.Fatalf("add first: %v", err)
	}
	second, err := store.Add(ctx, "second due", time.Now().Add(-time.Minute), "cli")
	if err != nil {
		t.Fatalf("add second: %v", err)
	}
	// Fail the claim of the second row only — the first is already claimed
	// when the batch aborts.
	if _, err := db.Exec(`CREATE TRIGGER test_fail_second BEFORE UPDATE ON reminders
		WHEN OLD.message = 'second due' BEGIN SELECT RAISE(FAIL, 'injected batch failure'); END`); err != nil {
		t.Fatalf("trigger: %v", err)
	}

	fired := map[int64]int{}
	sc := NewScanner(store, time.Second, func(r Reminder) { fired[r.ID]++ },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	sc.scanOnce(ctx)

	if fired[first.ID] != 1 {
		t.Fatalf("a row claimed before the failure fired %d times, want 1", fired[first.ID])
	}
	if fired[second.ID] != 0 {
		t.Fatalf("the failing row fired %d times, want 0", fired[second.ID])
	}

	// The row that never got claimed is still pending, so the next scan
	// delivers it — and the first is not delivered twice.
	if _, err := db.Exec(`DROP TRIGGER test_fail_second`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	sc.scanOnce(ctx)
	if fired[second.ID] != 1 {
		t.Fatalf("the deferred row fired %d times after recovery, want 1", fired[second.ID])
	}
	if fired[first.ID] != 1 {
		t.Fatalf("the already-delivered row fired %d times, want 1", fired[first.ID])
	}
}

// A claim that succeeds but cannot be read back has no payload to deliver;
// it must be rolled back rather than retired, or the notification is lost.
func TestClaimDueRollsBackUnreadableClaim(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()

	r, err := store.Add(ctx, "unreadable", time.Now().Add(-time.Minute), "cli")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	// Corrupt the row between the claim UPDATE and its read-back: due_at is
	// an INTEGER column, so scanning the text into int64 fails the way a
	// damaged row would.
	if _, err := db.Exec(`CREATE TRIGGER test_corrupt_due AFTER UPDATE ON reminders
		WHEN OLD.due_at <> 'boom' BEGIN UPDATE reminders SET due_at = 'boom' WHERE id = NEW.id; END`); err != nil {
		t.Fatalf("trigger: %v", err)
	}

	claimed, err := store.ClaimDue(ctx, time.Now(), 50)
	if err == nil {
		t.Fatal("a row that cannot be read back must report an error")
	}
	if len(claimed) != 0 {
		t.Fatalf("claimed %d rows with no payload, want 0", len(claimed))
	}

	var firedAt sql.NullInt64
	var dueAt int64
	if err := db.QueryRow(`SELECT fired_at, due_at FROM reminders WHERE id = ?`, r.ID).Scan(&firedAt, &dueAt); err != nil {
		t.Fatalf("read row back: %v", err)
	}
	if firedAt.Valid {
		t.Fatalf("the claim was not rolled back: fired_at = %d", firedAt.Int64)
	}
	if dueAt != r.DueAt {
		t.Fatalf("rollback did not restore due_at: %d, want %d", dueAt, r.DueAt)
	}

	// With the damage gone the row fires on the next scan, exactly once.
	if _, err := db.Exec(`DROP TRIGGER test_corrupt_due`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	fired := 0
	sc := NewScanner(store, time.Second, func(Reminder) { fired++ }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	sc.scanOnce(ctx)
	if fired != 1 {
		t.Fatalf("recovered row fired %d times, want 1", fired)
	}
}

// cancelAfterClaimConnector wraps the real SQLite driver and cancels the scan
// context once the claim UPDATE has committed — the window where the row is
// already stamped fired but its payload has not been read back yet. Every
// statement and all state still come from the real database.
type cancelAfterClaimConnector struct {
	path   string
	cancel context.CancelFunc
	once   sync.Once
}

func (c *cancelAfterClaimConnector) Driver() driver.Driver { return &sqlite.Driver{} }

func (c *cancelAfterClaimConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := (&sqlite.Driver{}).Open(c.path)
	if err != nil {
		return nil, err
	}
	return &cancelAfterClaimConn{Conn: conn, owner: c}, nil
}

type cancelAfterClaimConn struct {
	driver.Conn
	owner *cancelAfterClaimConnector
}

func (c *cancelAfterClaimConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	res, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
	if err == nil && strings.Contains(q, "UPDATE reminders SET fired_at") {
		c.owner.once.Do(c.owner.cancel)
	}
	return res, err
}

func (c *cancelAfterClaimConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}

// A scan context cancelled between the claim and its read-back must not leave
// the row retired: the rollback runs on its own context, so the reminder is
// still pending when the next scan runs.
func TestClaimDueRollsBackWhenContextCancelled(t *testing.T) {
	for _, recurring := range []bool{false, true} {
		t.Run(map[bool]string{false: "one-shot", true: "recurring"}[recurring], func(t *testing.T) {
			fixture := testDB(t)
			var seq int
			var name, path string
			if err := fixture.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
				t.Fatalf("database_list: %v", err)
			}
			every := time.Duration(0)
			if recurring {
				every = time.Minute
			}
			before, err := NewStore(fixture).AddEvery(context.Background(), "cancel at committed claim",
				time.Now().Add(-2*time.Minute), every, "cli")
			if err != nil {
				t.Fatalf("add: %v", err)
			}
			if err := fixture.Close(); err != nil {
				t.Fatalf("close fixture: %v", err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db := sql.OpenDB(&cancelAfterClaimConnector{path: path, cancel: cancel})
			defer db.Close()
			db.SetMaxOpenConns(1)
			store := NewStore(db)

			claimed, claimErr := store.ClaimDue(ctx, time.Now(), 50)
			if ctx.Err() == nil || claimErr == nil || len(claimed) != 0 {
				t.Fatalf("did not reach the cancellation window: ctx=%v err=%v claimed=%d",
					ctx.Err(), claimErr, len(claimed))
			}

			var fired, due int64
			if err := db.QueryRow(`SELECT COALESCE(fired_at, 0), due_at FROM reminders WHERE id = ?`,
				before.ID).Scan(&fired, &due); err != nil {
				t.Fatalf("read row: %v", err)
			}
			if fired != 0 || due != before.DueAt {
				t.Fatalf("committed claim was not rolled back: fired_at=%d due_at=%d, want 0 and %d",
					fired, due, before.DueAt)
			}

			// The next scan, on a live context, delivers it exactly once.
			recovered, err := store.ClaimDue(context.Background(), time.Now(), 50)
			if err != nil || len(recovered) != 1 || recovered[0].ID != before.ID {
				t.Fatalf("recovery claim = %d rows, %v; want the cancelled reminder", len(recovered), err)
			}
		})
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

// Package reminders implements scheduled user reminders (design P1-28):
// "提醒我 5 分钟后开会" — a reminder is persisted in SQLite, and a Scanner
// running in the daemon (and/or the web panel) claims each reminder when it
// comes due and fires a callback (log line, Web Push notification, SSE
// change signal). Claiming is atomic — UPDATE ... WHERE fired_at IS NULL —
// so the daemon and the panel can both run scanners against the same
// database without double-firing.
package reminders

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// Reminder is one scheduled notification. Times are Unix seconds.
type Reminder struct {
	ID            int64  `json:"id"`
	Message       string `json:"message"`
	DueAt         int64  `json:"due_at"`
	CreatedAt     int64  `json:"created_at"`
	FiredAt       int64  `json:"fired_at"`       // last fire time; 0 = never fired
	Source        string `json:"source"`         // "tool" (ask engine) | "cli" | "web"
	RepeatSeconds int64  `json:"repeat_seconds"` // >0 = reschedule on every claim instead of retiring
}

// Store is the SQLite persistence for reminders.
type Store struct {
	db *sql.DB
}

// NewStore wraps db for reminder CRUD. The table is created by
// storage.Migrate (v8).
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Add inserts a new reminder due at dueAt.
func (s *Store) Add(ctx context.Context, message string, dueAt time.Time, source string) (Reminder, error) {
	return s.AddEvery(ctx, message, dueAt, 0, source)
}

// AddEvery inserts a reminder that refires every `every` interval (0 = one
// shot). The first occurrence is dueAt; each claim then pushes due_at forward
// by whole intervals until it lands in the future.
func (s *Store) AddEvery(ctx context.Context, message string, dueAt time.Time, every time.Duration, source string) (Reminder, error) {
	now := time.Now().Unix()
	if message == "" {
		return Reminder{}, fmt.Errorf("reminder message must not be empty")
	}
	if source == "" {
		source = "cli"
	}
	if every < 0 {
		every = 0
	}
	r := Reminder{
		Message:       message,
		DueAt:         dueAt.Unix(),
		CreatedAt:     now,
		Source:        source,
		RepeatSeconds: int64(every / time.Second),
	}
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO reminders (message, due_at, created_at, fired_at, source, repeat_seconds)
		VALUES (?, ?, ?, NULL, ?, ?) RETURNING id`,
		message, r.DueAt, now, source, r.RepeatSeconds).Scan(&r.ID)
	if err != nil {
		return Reminder{}, fmt.Errorf("add reminder: %w", err)
	}
	return r, nil
}

// reminderCols is the SELECT column list shared by List and the claim
// read-back, so a schema field cannot be added to one and forgotten in the
// other. reminderColsRaw is the same list without the COALESCE — UNION inner
// selects must project the bare column name or the outer SELECT cannot see
// fired_at.
const reminderCols = `id, message, due_at, created_at, COALESCE(fired_at, 0), source, repeat_seconds`
const reminderColsRaw = `id, message, due_at, created_at, fired_at, source, repeat_seconds`

// List returns pending reminders (due order) first, then the most recently
// fired ones. includeFired controls whether fired rows appear at all.
// A recurring reminder stays in the pending half between occurrences —
// repeat_seconds > 0 means its fired_at is the *last* fire, not a retirement.
func (s *Store) List(ctx context.Context, includeFired bool) ([]Reminder, error) {
	q := `SELECT ` + reminderCols + `
		FROM reminders WHERE fired_at IS NULL OR repeat_seconds > 0 ORDER BY due_at ASC`
	if includeFired {
		q = `SELECT ` + reminderCols + ` FROM (
			SELECT ` + reminderColsRaw + ` FROM reminders
			WHERE fired_at IS NULL OR repeat_seconds > 0 ORDER BY due_at ASC LIMIT 200
		) UNION ALL SELECT ` + reminderCols + ` FROM (
			SELECT ` + reminderColsRaw + ` FROM reminders
			WHERE fired_at IS NOT NULL AND repeat_seconds = 0 ORDER BY fired_at DESC LIMIT 50
		)`
	}
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reminder
	for rows.Next() {
		var r Reminder
		if err := rows.Scan(&r.ID, &r.Message, &r.DueAt, &r.CreatedAt, &r.FiredAt, &r.Source, &r.RepeatSeconds); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Delete removes a reminder (pending or fired) by id.
func (s *Store) Delete(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM reminders WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ClaimDue atomically wins each due reminder and returns the ones this call
// got. Concurrent callers (daemon + panel sharing the database) each get a
// disjoint set:
//
//   - one-shot rows take the classic `fired_at IS NULL → fired_at = now` claim;
//   - recurring rows (repeat_seconds > 0) reschedule instead: a CAS on due_at
//     pushes it to the next occurrence and stamps fired_at, so a competing
//     scanner sees the row has already moved and wins nothing.
func (s *Store) ClaimDue(ctx context.Context, now time.Time, limit int) ([]Reminder, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, due_at, repeat_seconds FROM reminders
		WHERE (fired_at IS NULL OR repeat_seconds > 0) AND due_at <= ?
		ORDER BY due_at ASC LIMIT ?`,
		now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	type candidate struct{ id, dueAt, repeat int64 }
	var cands []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.dueAt, &c.repeat); err != nil {
			rows.Close()
			return nil, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var claimed []Reminder
	for _, c := range cands {
		var res sql.Result
		var err error
		if c.repeat > 0 {
			// Next occurrence strictly after now, stepped by whole intervals —
			// a reminder that was three periods behind fires once and lands on
			// schedule rather than storm-firing the backlog.
			next := c.dueAt + c.repeat*((now.Unix()-c.dueAt)/c.repeat+1)
			res, err = s.db.ExecContext(ctx,
				`UPDATE reminders SET fired_at = ?, due_at = ? WHERE id = ? AND due_at = ?`,
				now.Unix(), next, c.id, c.dueAt)
		} else {
			res, err = s.db.ExecContext(ctx,
				`UPDATE reminders SET fired_at = ? WHERE id = ? AND fired_at IS NULL`,
				now.Unix(), c.id)
		}
		if err != nil {
			return claimed, err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			continue // another process claimed/rescheduled it first
		}
		var r Reminder
		err = s.db.QueryRowContext(ctx,
			`SELECT `+reminderCols+` FROM reminders WHERE id = ?`,
			c.id).Scan(&r.ID, &r.Message, &r.DueAt, &r.CreatedAt, &r.FiredAt, &r.Source, &r.RepeatSeconds)
		if err != nil {
			return claimed, err
		}
		if r.RepeatSeconds > 0 {
			// Report the occurrence that fired, not the rescheduled one, so
			// log lines and push payloads read "the 15:00 reminder" rather
			// than "the 15:30 reminder".
			r.DueAt = c.dueAt
		}
		claimed = append(claimed, r)
	}
	return claimed, nil
}

// NextDue returns the earliest pending due_at after `from`, or false when the
// board is empty — the adaptive scanner's sleep target. Recurring rows always
// have a pending due_at (their next occurrence).
func (s *Store) NextDue(ctx context.Context) (time.Time, bool) {
	var due int64
	err := s.db.QueryRowContext(ctx, `
		SELECT MIN(due_at) FROM reminders
		WHERE fired_at IS NULL OR repeat_seconds > 0`).Scan(&due)
	if err != nil {
		// MIN over an empty set returns NULL — sql.Rows scans it as an error
		// into int64 on some drivers, into 0 on others; either way "no rows".
		return time.Time{}, false
	}
	return time.Unix(due, 0), due > 0
}

// Scanner polls the store and invokes OnFire for each reminder as it comes
// due. Run it in a goroutine; it stops when ctx ends.
type Scanner struct {
	Store  *Store
	Every  time.Duration
	OnFire func(Reminder)
	Logger *slog.Logger
}

// NewScanner builds a Scanner with sensible defaults (15s cadence).
func NewScanner(store *Store, every time.Duration, onFire func(Reminder), logger *slog.Logger) *Scanner {
	if every <= 0 {
		every = 15 * time.Second
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	return &Scanner{Store: store, Every: every, OnFire: onFire, Logger: logger}
}

// scanOnce claims everything currently due and fires it. It is also the probe
// Run calls after every wake — the scanner is event-driven, so "due" items are
// always taken immediately rather than waiting for the next cadence.
func (s *Scanner) scanOnce(ctx context.Context) {
	due, err := s.Store.ClaimDue(ctx, time.Now(), 50)
	if err != nil {
		if ctx.Err() == nil {
			s.Logger.Warn("reminders scan", "err", err)
		}
		return
	}
	for _, r := range due {
		s.Logger.Info("reminder fired", "id", r.ID, "message", r.Message, "repeat", r.RepeatSeconds)
		if s.OnFire != nil {
			s.OnFire(r)
		}
	}
}

// sleepFor computes how long until the next wake: the earlier of the polling
// ceiling (s.Every — the safety net for rows added by a process that cannot
// signal us) and the next pending due_at. A busy board wakes promptly; an idle
// one sleeps the full cadence instead of polling a dead table.
func (s *Scanner) sleepFor(ctx context.Context) time.Duration {
	d := s.Every
	if next, ok := s.Store.NextDue(ctx); ok {
		if until := time.Until(next); until < d {
			d = max(until, time.Second)
		}
	}
	return d
}

// Run loops until ctx is cancelled. Each iteration fires what is due, then
// sleeps until the next scheduled row (or the Every ceiling, whichever comes
// first) — so a reminder lands within a second of its due time when the board
// is quiet, while errors and empty boards fall back to the cadence. Errors are
// logged and retried — a transient database hiccup must not kill reminder
// delivery.
func (s *Scanner) Run(ctx context.Context) {
	for {
		s.scanOnce(ctx)
		t := time.NewTimer(s.sleepFor(ctx))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

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
	ID        int64  `json:"id"`
	Message   string `json:"message"`
	DueAt     int64  `json:"due_at"`
	CreatedAt int64  `json:"created_at"`
	FiredAt   int64  `json:"fired_at"` // 0 = pending
	Source    string `json:"source"`   // "tool" (ask engine) | "cli" | "web"
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
	now := time.Now().Unix()
	if message == "" {
		return Reminder{}, fmt.Errorf("reminder message must not be empty")
	}
	if source == "" {
		source = "cli"
	}
	r := Reminder{
		Message:   message,
		DueAt:     dueAt.Unix(),
		CreatedAt: now,
		Source:    source,
	}
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO reminders (message, due_at, created_at, fired_at, source)
		VALUES (?, ?, ?, NULL, ?) RETURNING id`,
		message, r.DueAt, now, source).Scan(&r.ID)
	if err != nil {
		return Reminder{}, fmt.Errorf("add reminder: %w", err)
	}
	return r, nil
}

// List returns pending reminders (due order) first, then the most recently
// fired ones. includeFired controls whether fired rows appear at all.
func (s *Store) List(ctx context.Context, includeFired bool) ([]Reminder, error) {
	q := `SELECT id, message, due_at, created_at, COALESCE(fired_at, 0), source
		FROM reminders WHERE fired_at IS NULL ORDER BY due_at ASC`
	if includeFired {
		q = `SELECT id, message, due_at, created_at, COALESCE(fired_at, 0), source FROM (
			SELECT id, message, due_at, created_at, fired_at, source FROM reminders
			WHERE fired_at IS NULL ORDER BY due_at ASC LIMIT 200
		) UNION ALL SELECT id, message, due_at, created_at, COALESCE(fired_at, 0), source FROM (
			SELECT id, message, due_at, created_at, fired_at, source FROM reminders
			WHERE fired_at IS NOT NULL ORDER BY fired_at DESC LIMIT 50
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
		if err := rows.Scan(&r.ID, &r.Message, &r.DueAt, &r.CreatedAt, &r.FiredAt, &r.Source); err != nil {
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

// ClaimDue atomically marks due, unfired reminders as fired and returns the
// ones this call won. Concurrent callers (daemon + panel sharing the
// database) each get a disjoint set: the UPDATE's WHERE fired_at IS NULL
// guard makes the claim exclusive.
func (s *Store) ClaimDue(ctx context.Context, now time.Time, limit int) ([]Reminder, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM reminders WHERE fired_at IS NULL AND due_at <= ? ORDER BY due_at ASC LIMIT ?`,
		now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var claimed []Reminder
	for _, id := range ids {
		res, err := s.db.ExecContext(ctx,
			`UPDATE reminders SET fired_at = ? WHERE id = ? AND fired_at IS NULL`,
			now.Unix(), id)
		if err != nil {
			return claimed, err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			continue // another process claimed it first
		}
		var r Reminder
		err = s.db.QueryRowContext(ctx,
			`SELECT id, message, due_at, created_at, COALESCE(fired_at, 0), source FROM reminders WHERE id = ?`,
			id).Scan(&r.ID, &r.Message, &r.DueAt, &r.CreatedAt, &r.FiredAt, &r.Source)
		if err != nil {
			return claimed, err
		}
		claimed = append(claimed, r)
	}
	return claimed, nil
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

// Run loops until ctx is cancelled. Errors are logged and retried — a
// transient database hiccup must not kill reminder delivery.
func (s *Scanner) Run(ctx context.Context) {
	t := time.NewTicker(s.Every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			due, err := s.Store.ClaimDue(ctx, time.Now(), 50)
			if err != nil {
				if ctx.Err() == nil {
					s.Logger.Warn("reminders scan", "err", err)
				}
				continue
			}
			for _, r := range due {
				s.Logger.Info("reminder fired", "id", r.ID, "message", r.Message)
				if s.OnFire != nil {
					s.OnFire(r)
				}
			}
		}
	}
}

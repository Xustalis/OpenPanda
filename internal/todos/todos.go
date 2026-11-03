// SPDX-License-Identifier: AGPL-3.0-or-later

// Package todos implements the user's own to-do list: the board beside the
// task queue where a person writes down what they mean to do, in the order
// they mean to do it.
//
// It is deliberately NOT a task. A task is a unit of agent work with a state
// machine, an executor, a lease and a result; a todo is a note that only
// becomes work when its owner says so ("开始"). The queue's 待办事项 column
// holds submitted tasks waiting for a free slot — it drains itself. This table
// holds intentions, and it only moves when a human moves it.
//
// The shape follows the to-do board WorkBuddy ships (project plan board):
// title + description, a status lane (pending → running → paused → done /
// cancelled), priority, a due date, tags, the project it belongs to, and a
// link to the task that was started from it. Times are Unix seconds; 0 means
// "unset" everywhere.
package todos

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Status values. These are wire identifiers — the console switches on them.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusPaused    = "paused"
	StatusDone      = "done"
	StatusCancelled = "cancelled"
)

// Priority values mirror the task queue's, so a todo promoted to a task keeps
// its weight.
const (
	PriorityHigh   = "high"
	PriorityNormal = "normal"
	PriorityLow    = "low"
)

// ErrNotFound is returned by Get/Patch/Delete for an unknown id.
var ErrNotFound = errors.New("no such todo")

// Todo is one row of the board.
type Todo struct {
	ID          int64    `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	Priority    string   `json:"priority"`
	DueAt       int64    `json:"due_at"`     // deadline; 0 = none
	StartedAt   int64    `json:"started_at"` // first time it entered running
	DoneAt      int64    `json:"done_at"`    // when it reached done/cancelled
	Tags        []string `json:"tags"`       //
	Project     string   `json:"project"`    // project name, "" = unfiled
	TaskID      string   `json:"task_id"`    // task started from this todo
	Seq         int64    `json:"seq"`        // manual board order; 0 = untouched
	CreatedAt   int64    `json:"created_at"` //
	UpdatedAt   int64    `json:"updated_at"` //
}

// Store is the SQLite persistence for todos. The table is created by
// storage.Migrate (v61).
type Store struct {
	db *sql.DB
}

// NewStore wraps db for todo CRUD.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// todoCols is the single SELECT list — adding a column means touching one
// place, so a scan can never drift from the schema.
const todoCols = `id, title, description, status, priority, due_at, started_at, done_at,
	tags, project, task_id, seq, created_at, updated_at`

// NormalizeStatus maps empty/unknown input onto a real lane. An unknown status
// from an old client falls back to pending rather than hiding the row.
func NormalizeStatus(s string) string {
	switch s {
	case StatusRunning, StatusPaused, StatusDone, StatusCancelled, StatusPending:
		return s
	default:
		return StatusPending
	}
}

// NormalizePriority maps empty/unknown input onto high|normal|low.
func NormalizePriority(p string) string {
	switch p {
	case PriorityHigh, PriorityLow, PriorityNormal:
		return p
	default:
		return PriorityNormal
	}
}

// CleanTags trims, drops blanks, de-duplicates and caps the tag list — a tag
// chip strip is a label, not a document.
func CleanTags(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		if len(t) > 24 {
			t = t[:24]
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
		if len(out) >= 8 {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func encodeTags(tags []string) string {
	if len(tags) == 0 {
		return "[]"
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func decodeTags(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func scanTodo(sc interface{ Scan(...any) error }) (Todo, error) {
	var (
		t    Todo
		tags string
	)
	err := sc.Scan(&t.ID, &t.Title, &t.Description, &t.Status, &t.Priority,
		&t.DueAt, &t.StartedAt, &t.DoneAt, &tags, &t.Project, &t.TaskID,
		&t.Seq, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return Todo{}, err
	}
	t.Tags = decodeTags(tags)
	return t, nil
}

// Filter narrows List. Zero values mean "no constraint".
type Filter struct {
	Status  string // "" = every status
	Project string // "" = every project
	Query   string // case-insensitive substring over title + description + tags
}

// escapeLike neutralizes LIKE's wildcards so a search term is matched
// literally. The escape character is a backslash, matching the ESCAPE clause
// in List.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// List returns the board, ordered the way the lanes read it: manual seq first
// (untouched rows last), then priority, then due date (undated last), then
// creation order. Sorting here rather than in SQL keeps the "0 means unset"
// semantics in one place.
func (s *Store) List(ctx context.Context, f Filter) ([]Todo, error) {
	q := `SELECT ` + todoCols + ` FROM todos WHERE 1 = 1`
	var args []any
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, NormalizeStatus(f.Status))
	}
	if f.Project != "" {
		q += ` AND project = ?`
		args = append(args, f.Project)
	}
	if q2 := strings.TrimSpace(f.Query); q2 != "" {
		// A user searching for "%" or "_" means those characters, not LIKE
		// wildcards. Escape them (and the escape character itself) so the
		// query matches the text that was typed.
		q += ` AND (title LIKE ? ESCAPE '\' OR description LIKE ? ESCAPE '\' OR tags LIKE ? ESCAPE '\')`
		like := "%" + escapeLike(q2) + "%"
		args = append(args, like, like, like)
	}
	q += ` LIMIT 2000`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Todo
	for rows.Next() {
		t, err := scanTodo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortBoard(out)
	return out, nil
}

// prioWeight mirrors the scheduler's policy order.
func prioWeight(p string) int {
	switch NormalizePriority(p) {
	case PriorityHigh:
		return 0
	case PriorityLow:
		return 2
	default:
		return 1
	}
}

// sortBoard applies the board's reading order in place.
func sortBoard(list []Todo) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		as, bs := a.Seq, b.Seq
		if as > 0 || bs > 0 {
			if as == 0 {
				return false
			}
			if bs == 0 {
				return true
			}
			if as != bs {
				return as < bs
			}
		}
		if ap, bp := prioWeight(a.Priority), prioWeight(b.Priority); ap != bp {
			return ap < bp
		}
		// An undated todo is "someday", which sorts after every dated one.
		if (a.DueAt > 0) != (b.DueAt > 0) {
			return a.DueAt > 0
		}
		if a.DueAt != b.DueAt {
			return a.DueAt < b.DueAt
		}
		return a.CreatedAt < b.CreatedAt
	})
}

// Get loads one todo.
func (s *Store) Get(ctx context.Context, id int64) (Todo, error) {
	t, err := scanTodo(s.db.QueryRowContext(ctx,
		`SELECT `+todoCols+` FROM todos WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Todo{}, ErrNotFound
	}
	return t, err
}

// CreateInput is the POST body, already parsed.
type CreateInput struct {
	Title       string
	Description string
	Status      string
	Priority    string
	DueAt       int64
	Tags        []string
	Project     string
}

// Create inserts a todo. An empty status starts it in the pending lane; a
// status of running/done stamps the matching timestamp, so a row created
// straight into a lane reads consistently.
func (s *Store) Create(ctx context.Context, in CreateInput) (Todo, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return Todo{}, errors.New("title must not be empty")
	}
	now := time.Now().Unix()
	t := Todo{
		Title:       title,
		Description: strings.TrimSpace(in.Description),
		Status:      NormalizeStatus(in.Status),
		Priority:    NormalizePriority(in.Priority),
		DueAt:       in.DueAt,
		Tags:        CleanTags(in.Tags),
		Project:     strings.TrimSpace(in.Project),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if t.Status == StatusRunning {
		t.StartedAt = now
	}
	if t.Status == StatusDone || t.Status == StatusCancelled {
		t.DoneAt = now
	}
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO todos (title, description, status, priority, due_at, started_at,
			done_at, tags, project, task_id, seq, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', 0, ?, ?) RETURNING id`,
		t.Title, t.Description, t.Status, t.Priority, t.DueAt, t.StartedAt,
		t.DoneAt, encodeTags(t.Tags), t.Project, now, now).Scan(&t.ID)
	if err != nil {
		return Todo{}, fmt.Errorf("create todo: %w", err)
	}
	return t, nil
}

// Patch is a partial update: nil fields are left alone. Pointers rather than
// zero values, because "clear the due date" (0) and "don't touch it" (nil)
// must be distinguishable.
type Patch struct {
	Title       *string
	Description *string
	Status      *string
	Priority    *string
	DueAt       *int64
	Tags        *[]string
	Project     *string
	TaskID      *string
	Seq         *int64
}

// Patch applies a partial update and returns the row as it now stands.
//
// Status is the interesting field: entering running stamps started_at the
// first time (a resumed todo keeps its original start), and entering
// done/cancelled stamps done_at while returning to pending/running clears it.
// Reordering (Seq) does not count as an edit of the content, but it still
// bumps updated_at — the console sorts "recently touched" by it.
func (s *Store) Patch(ctx context.Context, id int64, p Patch) (Todo, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return Todo{}, err
	}
	now := time.Now().Unix()
	sets := []string{"updated_at = ?"}
	args := []any{now}

	if p.Title != nil {
		title := strings.TrimSpace(*p.Title)
		if title == "" {
			return Todo{}, errors.New("title must not be empty")
		}
		sets = append(sets, "title = ?")
		args = append(args, title)
	}
	if p.Description != nil {
		sets = append(sets, "description = ?")
		args = append(args, strings.TrimSpace(*p.Description))
	}
	if p.Priority != nil {
		sets = append(sets, "priority = ?")
		args = append(args, NormalizePriority(*p.Priority))
	}
	if p.DueAt != nil {
		sets = append(sets, "due_at = ?")
		args = append(args, *p.DueAt)
	}
	if p.Tags != nil {
		sets = append(sets, "tags = ?")
		args = append(args, encodeTags(CleanTags(*p.Tags)))
	}
	if p.Project != nil {
		sets = append(sets, "project = ?")
		args = append(args, strings.TrimSpace(*p.Project))
	}
	if p.TaskID != nil {
		sets = append(sets, "task_id = ?")
		args = append(args, strings.TrimSpace(*p.TaskID))
	}
	if p.Seq != nil {
		sets = append(sets, "seq = ?")
		args = append(args, *p.Seq)
	}
	if p.Status != nil {
		next := NormalizeStatus(*p.Status)
		sets = append(sets, "status = ?")
		args = append(args, next)
		switch next {
		case StatusRunning:
			if cur.StartedAt == 0 {
				sets = append(sets, "started_at = ?")
				args = append(args, now)
			}
			sets = append(sets, "done_at = 0")
		case StatusDone, StatusCancelled:
			sets = append(sets, "done_at = ?")
			args = append(args, now)
			if cur.StartedAt == 0 {
				// A todo can be ticked off without ever being "started";
				// stamping the start keeps elapsed-time maths sane.
				sets = append(sets, "started_at = ?")
				args = append(args, now)
			}
		default: // pending, paused
			sets = append(sets, "done_at = 0")
		}
	}

	args = append(args, id)
	res, err := s.db.ExecContext(ctx,
		`UPDATE todos SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return Todo{}, fmt.Errorf("patch todo: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Todo{}, ErrNotFound
	}
	return s.Get(ctx, id)
}

// Delete removes a todo. A started todo's task is untouched — deleting the
// note must not cancel work that is already running.
func (s *Store) Delete(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM todos WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Reorder rewrites seq for exactly the ids it receives (1..n), the same
// contract the task board's drag has: the client sends the whole lane order.
func (s *Store) Reorder(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`UPDATE todos SET seq = ?, updated_at = ? WHERE id = ?`, i+1, now, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Fingerprint digests the board so the SSE feed can signal a change. Only
// identity and mutation stamps take part: a due date passing is a clock
// event, not a board edit.
func (s *Store) Fingerprint(ctx context.Context) (string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, status, updated_at FROM todos ORDER BY id`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var sum uint64 = 1469598103934665603
	for rows.Next() {
		var (
			id, updated int64
			status      string
		)
		if err := rows.Scan(&id, &status, &updated); err != nil {
			return "", err
		}
		for _, b := range []byte(fmt.Sprintf("%d:%s:%d;", id, status, updated)) {
			sum = (sum ^ uint64(b)) * 1099511628211
		}
	}
	return fmt.Sprintf("%x", sum), rows.Err()
}

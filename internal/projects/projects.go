// Package projects makes a project a first-class entity rather than a name that
// happens to appear on some tasks.
//
// Before this package a "project" was one Markdown file under projects/. Tasks
// carried a project name, the queue could filter on it, and that was the whole
// model: there was no record of what a project is, no directory where its work
// happens, and no notion of the one you are currently in — so every ask had to
// name it again, and a task delegated to another machine arrived carrying a name
// that machine had never heard of.
//
// Two things live here. The metadata (name, work dir, description, timestamps)
// is what makes a project addressable and portable; the work dir in particular
// is the tree a task runs in and therefore the tree that travels with a
// delegation. And the active-project pointer, which has to be on disk rather
// than in memory because `panda ask` is a one-shot process: a pointer held in
// RAM would be forgotten between two consecutive commands.
//
// The project's *memory* stays where it was, in internal/memory.Projects — this
// package deliberately does not own it. The isolation wall (design §17.2) is
// structural, and moving project memory behind a store that also knows about
// work directories and active state would be the first crack in it.
package projects

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// activeKey is the settings row holding the current project. One row, because
// "which project am I in" is a property of this machine, not of a session: the
// REPL, a bare `panda ask` and the web console are all looking at the same node.
const activeKey = "active_project"

// ErrNotFound reports a project name that is not in the table.
var ErrNotFound = errors.New("projects: no such project")

// ErrExists reports a create/rename onto a name already taken.
var ErrExists = errors.New("projects: project already exists")

// Project is one project's metadata. WorkDir is absolute when set; empty means
// the project has no tree of its own and its tasks run in the node's work
// directory, which is how a memory-only project (every project that existed
// before this table) keeps working.
type Project struct {
	Name        string    `json:"name"`
	WorkDir     string    `json:"work_dir,omitempty"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Store is the projects table plus the settings row that names the active one.
type Store struct {
	db  *sql.DB
	now func() int64
}

// NewStore wraps an already-migrated database.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db, now: func() int64 { return time.Now().Unix() }}
}

// ValidateName rejects names that cannot be used as a path segment or a wire
// value. A project name reaches the filesystem (its memory file), the bus (the
// delegation payload) and the CLI (as an argument), so the safe set is the
// intersection of all three rather than whatever SQLite would accept.
func ValidateName(name string) error {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return fmt.Errorf("projects: name is empty")
	case len(name) > 64:
		return fmt.Errorf("projects: name is longer than 64 characters")
	case name == "." || name == "..":
		return fmt.Errorf("projects: name %q is a path segment", name)
	case strings.ContainsAny(name, `/\:*?"<>|`):
		return fmt.Errorf("projects: name %q contains a path or shell character", name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("projects: name contains a control character")
		}
	}
	return nil
}

// Create inserts a project. workDir is resolved to an absolute path so a project
// created from one directory still points at the same tree when a task runs from
// another — the daemon's cwd is not the cwd of whoever typed the command.
func (s *Store) Create(name, workDir, description string) (Project, error) {
	if err := ValidateName(name); err != nil {
		return Project{}, err
	}
	if workDir != "" {
		abs, err := filepath.Abs(workDir)
		if err != nil {
			return Project{}, fmt.Errorf("projects: resolve work dir: %w", err)
		}
		workDir = abs
	}
	ts := s.now()
	_, err := s.db.Exec(
		`INSERT INTO projects (name, work_dir, description, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`, name, workDir, description, ts, ts)
	if err != nil {
		if isUniqueViolation(err) {
			return Project{}, fmt.Errorf("%w: %s", ErrExists, name)
		}
		return Project{}, err
	}
	return s.Get(name)
}

// Get reads one project.
func (s *Store) Get(name string) (Project, error) {
	var p Project
	var created, updated int64
	err := s.db.QueryRow(
		`SELECT name, work_dir, description, created_at, updated_at FROM projects WHERE name = ?`,
		name).Scan(&p.Name, &p.WorkDir, &p.Description, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err != nil {
		return Project{}, err
	}
	p.CreatedAt = time.Unix(created, 0)
	p.UpdatedAt = time.Unix(updated, 0)
	return p, nil
}

// List returns every project, newest activity first.
func (s *Store) List() ([]Project, error) {
	rows, err := s.db.Query(
		`SELECT name, work_dir, description, created_at, updated_at FROM projects
		 ORDER BY updated_at DESC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		var created, updated int64
		if err := rows.Scan(&p.Name, &p.WorkDir, &p.Description, &created, &updated); err != nil {
			return nil, err
		}
		p.CreatedAt = time.Unix(created, 0)
		p.UpdatedAt = time.Unix(updated, 0)
		out = append(out, p)
	}
	return out, rows.Err()
}

// Update rewrites a project's work dir and description. An empty string clears
// the field, which is how a project gives up its tree.
func (s *Store) Update(name, workDir, description string) (Project, error) {
	if workDir != "" {
		abs, err := filepath.Abs(workDir)
		if err != nil {
			return Project{}, fmt.Errorf("projects: resolve work dir: %w", err)
		}
		workDir = abs
	}
	res, err := s.db.Exec(
		`UPDATE projects SET work_dir = ?, description = ?, updated_at = ? WHERE name = ?`,
		workDir, description, s.now(), name)
	if err != nil {
		return Project{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Project{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return s.Get(name)
}

// Rename moves a project to a new name, carrying the active pointer with it. The
// two writes are one transaction: a rename that renamed the row but left the
// pointer behind would leave the user "inside" a project that no longer exists.
//
// The caller is responsible for the project's memory file and for any tasks that
// reference the old name — renaming is metadata here, and the CLI wires the rest.
func (s *Store) Rename(oldName, newName string) (Project, error) {
	if err := ValidateName(newName); err != nil {
		return Project{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Project{}, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.Exec(`UPDATE projects SET name = ?, updated_at = ? WHERE name = ?`,
		newName, s.now(), oldName)
	if err != nil {
		if isUniqueViolation(err) {
			return Project{}, fmt.Errorf("%w: %s", ErrExists, newName)
		}
		return Project{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Project{}, fmt.Errorf("%w: %s", ErrNotFound, oldName)
	}
	if _, err := tx.Exec(`UPDATE settings SET value = ? WHERE key = ? AND value = ?`,
		newName, activeKey, oldName); err != nil {
		return Project{}, err
	}
	if err := tx.Commit(); err != nil {
		return Project{}, err
	}
	return s.Get(newName)
}

// Delete removes a project's metadata and clears the active pointer when it named
// this one. It does not touch the work directory: deleting a project is an act of
// bookkeeping, and silently removing a tree the user pointed at would be the one
// irreversible thing in this package.
func (s *Store) Delete(name string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.Exec(`DELETE FROM projects WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if _, err := tx.Exec(`DELETE FROM settings WHERE key = ? AND value = ?`, activeKey, name); err != nil {
		return err
	}
	return tx.Commit()
}

// Active returns the current project's name, or "" when none is set. A pointer at
// a project that has since been deleted reads as "" rather than as an error: the
// caller's next question is always "which project am I in", and "none" is the
// honest answer.
func (s *Store) Active() (string, error) {
	var name string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, activeKey).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if _, err := s.Get(name); err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	return name, nil
}

// SetActive points at a project, which must exist — entering a project that was
// never created would make every later "which project am I in" answer a lie.
func (s *Store) SetActive(name string) error {
	if _, err := s.Get(name); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, activeKey, name)
	return err
}

// ClearActive leaves whatever project was current. Idempotent: leaving when you
// are not in a project is not an error, it is already true.
func (s *Store) ClearActive() error {
	_, err := s.db.Exec(`DELETE FROM settings WHERE key = ?`, activeKey)
	return err
}

// Touch moves a project's updated_at, which is what orders `panda project list`.
// Called when work happens in a project rather than when its row is edited, so
// the list reads as "what I have been working on".
func (s *Store) Touch(name string) error {
	_, err := s.db.Exec(`UPDATE projects SET updated_at = ? WHERE name = ?`, s.now(), name)
	return err
}

// EnsureFromName creates a metadata row for a project that only ever existed as a
// memory file. Projects predate this table, so a name that appears on a task or
// in projects/<name>/ must still resolve to a project; this is the adoption path,
// and it is idempotent.
func (s *Store) EnsureFromName(name string) (Project, error) {
	if p, err := s.Get(name); err == nil {
		return p, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Project{}, err
	}
	return s.Create(name, "", "")
}

// isUniqueViolation reports whether err is SQLite's primary-key conflict. The
// pure-Go driver reports it as a message rather than a typed error, so the string
// is what there is to match on.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "constraint failed")
}

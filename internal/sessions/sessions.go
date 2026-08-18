// Package sessions implements the web console's conversation model: each
// session is one chat thread plus — when the working directory is a git
// repository — a dedicated git worktree, so changes made while answering a
// session's prompts land on an isolated branch instead of the user's checkout
// (the codex / claude-code working model).
//
// Storage is one JSON file per session under a root directory, kept trivially
// inspectable and dependency-free like the rest of the node's file-backed
// stores.
package sessions

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/util"
)

// Turn is one stored conversation message.
type Turn struct {
	Role string `json:"role"` // "user" | "assistant"
	Text string `json:"text"`
	// Kind marks how the turn was produced: "answer" prose, or "task" with the
	// task id in Ref so the UI can deep-link.
	Kind string `json:"kind,omitempty"`
	Ref  string `json:"ref,omitempty"`
}

// Session is one chat thread. Worktree/Branch are empty when the work path is
// not a git repository (sessions then run in the shared work dir).
type Session struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Branch    string    `json:"branch,omitempty"`
	Worktree  string    `json:"worktree,omitempty"`
	Turns     []Turn    `json:"turns"`
}

// Store persists sessions as JSON files under root (created on demand).
// All methods are safe for concurrent use.
type Store struct {
	root string
	mu   sync.Mutex
}

// NewStore returns a Store rooted at dir.
func NewStore(dir string) *Store {
	return &Store{root: dir}
}

// ErrNotFound is returned for unknown session ids.
var ErrNotFound = errors.New("sessions: no such session")

// Create starts a new session titled after the first prompt (the title is
// derived lazily by the caller; Create accepts it directly).
func (s *Store) Create(title string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return nil, fmt.Errorf("sessions: mkdir: %w", err)
	}
	now := time.Now()
	id, err := util.UUIDv7()
	if err != nil {
		return nil, fmt.Errorf("sessions: id: %w", err)
	}
	sess := &Session{
		ID:        strings.ReplaceAll(id, "-", "")[:16],
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.save(sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// Get loads one session.
func (s *Store) Get(id string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(id)
}

// List returns all sessions, newest first.
func (s *Store) List() ([]*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		sess, err := s.load(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue // a corrupt file never breaks the listing
		}
		out = append(out, sess)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// AppendTurn appends a turn and refreshes the session timestamp (and the
// title from the first user turn while the title is still unset).
func (s *Store) AppendTurn(id string, turn Turn) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.load(id)
	if err != nil {
		return nil, err
	}
	sess.Turns = append(sess.Turns, turn)
	if turn.Role == "user" && sess.Title == "" {
		sess.Title = truncateTitle(turn.Text)
	}
	sess.UpdatedAt = time.Now()
	if err := s.save(sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// SetWorktree records the worktree path/branch on a session.
func (s *Store) SetWorktree(id, path, branch string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.load(id)
	if err != nil {
		return err
	}
	sess.Worktree = path
	sess.Branch = branch
	sess.UpdatedAt = time.Now()
	return s.save(sess)
}

// Delete removes the session file (worktree cleanup is the caller's job).
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.path(id)); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func (s *Store) path(id string) string { return filepath.Join(s.root, id+".json") }

func (s *Store) load(id string) (*Session, error) {
	if id == "" || strings.Contains(id, "/") || strings.Contains(id, "..") {
		return nil, ErrNotFound
	}
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("sessions: parse %s: %w", id, err)
	}
	return &sess, nil
}

func (s *Store) save(sess *Session) error {
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(sess.ID), data, 0o644)
}

func truncateTitle(s string) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > 40 {
		return string([]rune(s)[:40]) + "…"
	}
	return s
}

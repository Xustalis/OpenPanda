// SPDX-License-Identifier: AGPL-3.0-or-later

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
	"context"
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

// SummaryMarker prefixes the synthetic context note that carries a session's
// compacted-history digest when it is replayed. It is wire contract with the
// model, not UI text, so it stays English in every locale.
const SummaryMarker = "[earlier conversation, summarized]"

// DefaultHistoryBudget is the replay window (characters) a session's thread
// is compacted to. It matches the bare-mode conversation budget: both feed
// the same entry prompt, so the constraint is prompt size, not storage.
const DefaultHistoryBudget = 24000

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
	Project   string    `json:"project,omitempty"`
	// ParentID/ForkIndex describe a session forked from another: the child
	// starts with a copy of the parent's first ForkIndex turns. Empty
	// ParentID marks a root thread.
	ParentID  string `json:"parent_id,omitempty"`
	ForkIndex int    `json:"fork_index,omitempty"`
	// Summary is a model-written digest of evicted turns. Compaction moves
	// the oldest exchanges here instead of dropping them, so replays keep
	// early context within the replay budget.
	Summary       string            `json:"summary,omitempty"`
	Turns         []Turn            `json:"turns"`
	AgentSessions map[string]string `json:"agent_sessions,omitempty"`
	Operation     *Operation        `json:"operation,omitempty"`
	// Pinned keeps a session at the top of List regardless of recency. It is
	// a user-facing ordering flag only — it does not protect the session
	// from deletion.
	Pinned bool `json:"pinned,omitempty"`
}

// Operation is the latest durable ask operation for a session. It lets a
// client reconnect after its SSE response disappears and observe the exact
// generation it started instead of inferring completion from transcript shape.
type Operation struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	TaskID    string    `json:"task_id,omitempty"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
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

// ValidID reports whether id is safe to use inside a filesystem path or git
// ref. Session ids are generated hex, so anything containing a separator,
// a parent traversal, or a drive/anchor is not a session — this is the store
// layer's guard against callers (HTTP path values included) smuggling a path.
func ValidID(id string) bool {
	if id == "" || strings.Contains(id, "..") || strings.ContainsAny(id, "/\\") {
		return false
	}
	return !filepath.IsAbs(id)
}

// Create starts a new session titled after the first prompt (the title is
// derived lazily by the caller; Create accepts it directly). An optional
// project associates the session with that project.
func (s *Store) Create(title string, project ...string) (*Session, error) {
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
	var proj string
	if len(project) > 0 {
		proj = strings.TrimSpace(project[0])
	}
	sess := &Session{
		ID:        strings.ReplaceAll(id, "-", "")[:16],
		Title:     title,
		Project:   proj,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.save(sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// Fork creates a child session that starts with a copy of the parent's
// first atTurn turns (atTurn <= 0 or beyond the parent's length copies the
// whole thread). The child is a new root for all purposes after creation —
// later parent turns do not propagate — but it remembers where it split
// (ParentID + ForkIndex) so listings can render the conversation tree.
// The parent's title/project/summary carry over; its worktree does not —
// callers carve a fresh one from the parent's branch when in a repository.
func (s *Store) Fork(id string, atTurn int) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	parent, err := s.load(id)
	if err != nil {
		return nil, err
	}
	if atTurn <= 0 || atTurn > len(parent.Turns) {
		atTurn = len(parent.Turns)
	}
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return nil, fmt.Errorf("sessions: mkdir: %w", err)
	}
	now := time.Now()
	uid, err := util.UUIDv7()
	if err != nil {
		return nil, fmt.Errorf("sessions: id: %w", err)
	}
	child := &Session{
		ID:        strings.ReplaceAll(uid, "-", "")[:16],
		Title:     parent.Title,
		Project:   parent.Project,
		ParentID:  parent.ID,
		ForkIndex: atTurn,
		Summary:   parent.Summary,
		Turns:     append([]Turn(nil), parent.Turns[:atTurn]...),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.save(child); err != nil {
		return nil, err
	}
	return child, nil
}

// Children returns the sessions forked directly from id, oldest first.
func (s *Store) Children(id string) ([]*Session, error) {
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
			continue
		}
		if sess.ParentID == id {
			out = append(out, sess)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// Get loads one session.
func (s *Store) Get(id string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(id)
}

// List returns all sessions, pinned first and then newest first.
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
	Sort(out)
	return out, nil
}

// Sort orders a session listing the way the rail renders it: pinned threads
// first, then newest activity first within each group.
func Sort(list []*Session) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].Pinned != list[j].Pinned {
			return list[i].Pinned
		}
		return list[i].UpdatedAt.After(list[j].UpdatedAt)
	})
}

// Stamp is the lightest identity-of-change a session file exposes: name,
// size and mtime. The SSE change feed hashes stamps instead of unmarshalling
// every thread once per poll — an O(n) stat scan instead of O(total bytes).
type Stamp struct {
	ID          string
	Size        int64
	ModUnixNano int64
}

// Stamps returns one stamp per stored session file, sorted by id.
func (s *Store) Stamps() ([]Stamp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Stamp
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Stamp{
			ID:          strings.TrimSuffix(e.Name(), ".json"),
			Size:        info.Size(),
			ModUnixNano: info.ModTime().UnixNano(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
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

// SplitForBudget partitions a thread into (evicted, kept) where kept fits
// budget characters of Text. Eviction is pair-aligned like the bare-mode
// convo trim: a user turn never survives without its assistant answer, and
// the newest exchange is never evicted.
func SplitForBudget(turns []Turn, budget int) (evicted, kept []Turn) {
	total := 0
	for _, t := range turns {
		total += len(t.Text)
	}
	kept = turns
	for total > budget && len(kept) > 2 {
		drop := 2
		if len(kept)%2 == 1 {
			drop = 1
		}
		for i := 0; i < drop && i < len(kept); i++ {
			total -= len(kept[i].Text)
		}
		evicted = append(evicted, kept[:drop]...)
		kept = kept[drop:]
	}
	return evicted, kept
}

// CompactHistory auto-compacts one session: while the thread exceeds budget,
// the oldest exchanges are folded into the session's Summary by summarize
// (the previous digest is prepended so the model merges it forward). It
// returns the possibly-updated session. When the thread fits, when no
// summarizer is given, or when summarization fails, the session is returned
// unchanged — compaction degrades, it never corrupts.
//
// The summarizer is a model call (seconds, not microseconds), so it runs
// outside the store lock: the split happens under the mutex, the digest
// happens unlocked, and the write re-loads and re-splits so turns appended
// meanwhile survive in the kept tail. A concurrent compaction that already
// published a newer Summary wins — this call then returns the store's state
// rather than overwriting with a stale digest.
func (s *Store) CompactHistory(ctx context.Context, id string, budget int, summarize func(context.Context, []Turn) (string, error)) (*Session, error) {
	s.mu.Lock()
	sess, err := s.load(id)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	evicted, _ := SplitForBudget(sess.Turns, budget)
	priorSummary := sess.Summary
	s.mu.Unlock()
	if len(evicted) == 0 || summarize == nil {
		return sess, nil
	}
	input := evicted
	if priorSummary != "" {
		input = append([]Turn{{Role: "user", Text: SummaryMarker + "\n" + priorSummary}}, evicted...)
	}
	digest, err := summarize(ctx, input)
	if err != nil || strings.TrimSpace(digest) == "" {
		return sess, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	fresh, err := s.load(id)
	if err != nil {
		return nil, err
	}
	if fresh.Summary != priorSummary {
		return fresh, nil // another compaction published meanwhile
	}
	if len(fresh.Turns) < len(evicted) {
		// A racing compaction wrote the SAME digest text (the CAS above
		// compares Summary, not turn count): the thread is already trimmed —
		// do not slice past its end.
		return fresh, nil
	}
	// Turns only ever change by append (or by a compaction the CAS above
	// already caught), so the evicted set is still a strict prefix of the
	// stored thread: everything past it was not summarized and must be kept.
	// The thread may sit slightly over budget until the next compaction —
	// a soft overshoot, never a silent drop of unsummarized turns.
	kept2 := append([]Turn(nil), fresh.Turns[len(evicted):]...)
	fresh.Summary = strings.TrimSpace(digest)
	fresh.Turns = kept2
	fresh.UpdatedAt = time.Now()
	if err := s.save(fresh); err != nil {
		return nil, err
	}
	return fresh, nil
}

// SetOperation replaces the session's current durable operation snapshot.
// Callers must pass the exact operation id when updating an existing snapshot;
// a stale completion can therefore never overwrite a newer generation.
func (s *Store) SetOperation(id string, op Operation) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.load(id)
	if err != nil {
		return false, err
	}
	if sess.Operation != nil && sess.Operation.ID != op.ID {
		return false, nil
	}
	op.UpdatedAt = time.Now()
	sess.Operation = &op
	sess.UpdatedAt = op.UpdatedAt
	if err := s.save(sess); err != nil {
		return false, err
	}
	return true, nil
}

// StartOperation installs a new operation generation unconditionally. A later
// SetOperation must carry this id, which gives durable compare-and-set behavior.
func (s *Store) StartOperation(id string, op Operation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.load(id)
	if err != nil {
		return err
	}
	op.UpdatedAt = time.Now()
	sess.Operation = &op
	sess.UpdatedAt = op.UpdatedAt
	return s.save(sess)
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

// SetAgentSession records an agent's native session id on a session.
func (s *Store) SetAgentSession(id, agent, agentSessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.load(id)
	if err != nil {
		return err
	}
	if sess.AgentSessions == nil {
		sess.AgentSessions = make(map[string]string)
	}
	sess.AgentSessions[agent] = agentSessionID
	sess.UpdatedAt = time.Now()
	return s.save(sess)
}

// SetTitle updates the title of a session.
func (s *Store) SetTitle(id, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.load(id)
	if err != nil {
		return err
	}
	sess.Title = strings.TrimSpace(title)
	sess.UpdatedAt = time.Now()
	return s.save(sess)
}

// SetPinned marks or clears a session's pinned flag.
func (s *Store) SetPinned(id string, pinned bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.load(id)
	if err != nil {
		return err
	}
	sess.Pinned = pinned
	sess.UpdatedAt = time.Now()
	return s.save(sess)
}

// ListByProject returns all sessions belonging to project in List order
// (pinned first, then newest).
// If project is empty, it returns unassigned sessions (where Project is empty).
func (s *Store) ListByProject(project string) ([]*Session, error) {
	list, err := s.List()
	if err != nil {
		return nil, err
	}
	project = strings.TrimSpace(project)
	var out []*Session
	for _, sess := range list {
		if sess.Project == project {
			out = append(out, sess)
		}
	}
	return out, nil
}

// SetProject updates the project association for a session.
// An empty project string disassociates the session.
func (s *Store) SetProject(id, project string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.load(id)
	if err != nil {
		return err
	}
	sess.Project = strings.TrimSpace(project)
	sess.UpdatedAt = time.Now()
	return s.save(sess)
}

// RenameProject updates all sessions associated with oldName to newName.
// It returns the number of updated sessions.
func (s *Store) RenameProject(oldName, newName string) (int, error) {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if oldName == "" || oldName == newName {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var count int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		sess, err := s.load(id)
		if err != nil {
			continue
		}
		if sess.Project == oldName {
			sess.Project = newName
			sess.UpdatedAt = time.Now()
			if err := s.save(sess); err == nil {
				count++
			}
		}
	}
	return count, nil
}

// Delete removes the session file (worktree cleanup is the caller's job).
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidID(id) {
		return ErrNotFound
	}
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
	if !ValidID(id) {
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
	// Atomic: a crash mid-write would leave a truncated file that fails to
	// parse — invisible in List and unreadable in Get, i.e. a silently lost
	// conversation.
	return util.WriteFileAtomic(s.path(sess.ID), data, 0o644)
}

func truncateTitle(s string) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > 40 {
		return string([]rune(s)[:40]) + "…"
	}
	return s
}

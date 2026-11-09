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

	"github.com/Xustalis/OpenPanda/internal/defense"
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

// minReplayBudget/maxReplayBudget bound ReplayBudget's derivation. Below the
// floor a couple of exchanges would not fit at all; the ceiling keeps a huge
// advertised window from buying an unbounded replay — whatever falls out of
// the window is the input the compaction call itself must read.
const (
	minReplayBudget = 8000
	maxReplayBudget = 160000
)

// ReplayBudget derives the replay window (characters) for a thread from the
// serving model's advertised context window (tokens). History is one section
// of the entry prompt — alongside the device list, memory wall and tool
// specs — so it claims at most an eighth of the window. The character
// estimate stays conservative on purpose (CJK text is ~1 char/token, the
// mixed-script average nearer 2): 3/16 of the window lands exactly on
// DefaultHistoryBudget for a 128K model, the setting this replaces. An
// unknown window keeps the historical default.
func ReplayBudget(contextWindowTokens int) int {
	if contextWindowTokens <= 0 {
		return DefaultHistoryBudget
	}
	budget := contextWindowTokens * 3 / 16
	if budget < minReplayBudget {
		return minReplayBudget
	}
	if budget > maxReplayBudget {
		return maxReplayBudget
	}
	return budget
}

// Turn is one stored conversation message.
type Turn struct {
	RoundID string `json:"round_id,omitempty"`
	// IdempotencyKey lets durable producers safely replay an append after a
	// crash or a bookkeeping failure. Empty keeps ordinary append semantics;
	// non-empty keys are unique within the session, first writer wins.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	Role           string `json:"role"` // "user" | "assistant"
	Text           string `json:"text"`
	// Kind marks how the turn was produced: "answer" prose, or "task" with the
	// task id in Ref so the UI can deep-link.
	Kind string `json:"kind,omitempty"`
	Ref  string `json:"ref,omitempty"`
	// Code carries the machine reason of an "error"-kind turn (e.g.
	// "model_config"), so a reloaded transcript can still offer the matching
	// fix-it affordance the live stream's error event carried.
	Code string `json:"code,omitempty"`
	// ErrKind is the classified model-error kind (entry.ErrKind*) an
	// "error"-kind turn came from — it picks which troubleshooting block the
	// console folds under the bubble. ErrDetail is the provider's bounded
	// reply text, rendered first inside the expansion.
	ErrKind   string `json:"err_kind,omitempty"`
	ErrDetail string `json:"err_detail,omitempty"`
	// DurationMS is how long the turn took, from the ask being accepted to the
	// reply landing. Assistant turns only; absent on user turns and on
	// transcripts written before it existed. The console prints it above the
	// reply, and it has to survive a reload: the live timer that measures the
	// turn lives in the browser, so a reloaded thread would otherwise lose
	// every duration it had.
	DurationMS int64 `json:"duration_ms,omitempty"`
	// Tokens is the ask's billed total (input + output) for this reply. Filed
	// with the turn for the same reason as DurationMS: the live counter lives
	// in the browser stream, and a reloaded thread would otherwise lose every
	// figure it had shown.
	Tokens int64 `json:"tokens,omitempty"`
	// Model is the entry model that wrote this reply (the ask's classify /
	// answer calls). Stored with the turn so the console can still say which
	// model answered after the thread is reloaded — the live result object
	// carries it, but that object is never written to disk.
	Model string `json:"model,omitempty"`
	// TS is when the turn was written (Unix seconds). It exists for the usage
	// calendar: a thread that stays open across midnight has its messages
	// counted on the days they actually happened, which the session's own
	// CreatedAt/UpdatedAt cannot express. Zero on transcripts written before
	// the field existed — readers fall back to the session stamps there.
	TS int64 `json:"ts,omitempty"`
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
	// CompactedTurnKeys preserves exactly-once delivery after keyed turns
	// have been folded into Summary. A digest must not erase acknowledgements.
	CompactedTurnKeys []string          `json:"compacted_turn_keys,omitempty"`
	Summary           string            `json:"summary,omitempty"`
	Turns             []Turn            `json:"turns"`
	AgentSessions     map[string]string `json:"agent_sessions,omitempty"`
	Operation         *Operation        `json:"operation,omitempty"`
	// Pinned keeps a session at the top of List regardless of recency. It is
	// a user-facing ordering flag only — it does not protect the session
	// from deletion.
	Pinned bool `json:"pinned,omitempty"`
	// Team is the roster of agents (registry/card keys) this thread may hand
	// work to — the "who works on this" pick the composer makes when a thread
	// starts. Empty means no restriction: the scheduler routes every task by
	// capability, exactly as a thread with no pick behaves. It rides the
	// session rather than the turn because it describes the conversation,
	// not one message, and a thread reopened on another device must remember
	// which hands are allowed to touch it.
	Team []string `json:"team,omitempty"`
	// PermissionMode is the conversation's permission policy (defense.Mode*):
	// how much this thread may do before asking. It lives on the session for
	// the same reason Team does — it describes the conversation, not one
	// message, and a thread reopened on another device must remember how much
	// autonomy the user granted it. Empty means "never picked", which reads
	// as auto.
	PermissionMode string `json:"permission_mode,omitempty"`
	// SideChats are the conversations beside this thread. Each has its own
	// transcript, which may read, write and dispatch like the thread but never
	// appends to Turns and never rewrites the thread's summary. They live on
	// the session so a side question outlives the pane that asked it — a
	// reload, a second device, or a panel restart still finds every one of
	// them, and a thread can hold several without them seeing each other.
	SideChats []SideChat `json:"side_chats,omitempty"`
	// SideActive names the side chat the console is showing. It is the
	// reader's place rather than a fact about the thread, but it is stored so
	// reopening the pane lands where they left off instead of jumping to
	// whichever conversation was written last.
	SideActive string `json:"side_active,omitempty"`
	// SideTurns and SideOperation are the one-conversation-per-thread shape
	// that predates SideChats. They are read once and folded into SideChats by
	// normalizeSide; every write persists the list and drops them.
	SideTurns     []SideTurn `json:"side_turns,omitempty"`
	SideOperation *Operation `json:"side_operation,omitempty"`
}

// SideChat is one conversation beside a thread. Its transcript is separate
// from the thread's Turns and from every other side chat: switching chats in
// the pane changes what the reader sees and what the next question replays,
// and nothing else.
type SideChat struct {
	ID string `json:"id"`
	// Title is the first question, trimmed to one line — a list of rows all
	// named "side chat" is a list you cannot navigate.
	Title string     `json:"title,omitempty"`
	Turns []SideTurn `json:"turns,omitempty"`
	// Operation is this chat's latest ask, tracked separately from the
	// thread's Operation so a side question never looks like its own turn.
	Operation *Operation `json:"operation,omitempty"`
	CreatedAt int64      `json:"created_at,omitempty"`
	// UpdatedAt is the last message's clock. The pane sorts by it.
	UpdatedAt int64 `json:"updated_at,omitempty"`
}

// SideTurn is one message in a session's side conversation. It carries the
// display-only trace (the tools behind an answer) and the billed tokens, so a
// reloaded pane shows the same evidence the live stream did. It is never
// replayed as a thread turn.
type SideTurn struct {
	Role string `json:"role"` // "user" | "assistant"
	Text string `json:"text"`
	// Tools are the tool calls behind an assistant answer, in order, each as
	// "name subject" (workspace_read side.go). Display-only.
	Tools []string `json:"tools,omitempty"`
	// Tokens is the ask's billed total for this answer.
	Tokens int64 `json:"tokens,omitempty"`
	// TaskID is the task this answer dispatched (the last one for a batch), so
	// a reloaded pane still offers the way into the work.
	TaskID string `json:"task_id,omitempty"`
	// DurationMS is how long the answer took.
	DurationMS int64 `json:"duration_ms,omitempty"`
	// Error marks a failed answer; the pane renders it as the error note it
	// was, instead of passing an error string off as prose.
	Error string `json:"error,omitempty"`
	TS    int64  `json:"ts,omitempty"`
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
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return nil, fmt.Errorf("sessions: mkdir: %w", err)
	}
	// Transcripts hold verbatim user text — including pasted secrets — so a
	// session root created by an older build keeps its looser mode no longer.
	_ = os.Chmod(s.root, 0o700)
	now := time.Now()
	id, err := s.newIDLocked()
	if err != nil {
		return nil, err
	}
	var proj string
	if len(project) > 0 {
		proj = strings.TrimSpace(project[0])
	}
	sess := &Session{
		ID:        id,
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
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return nil, fmt.Errorf("sessions: mkdir: %w", err)
	}
	_ = os.Chmod(s.root, 0o700)
	now := time.Now()
	uid, err := s.newIDLocked()
	if err != nil {
		return nil, err
	}
	child := &Session{
		ID:                uid,
		Title:             parent.Title,
		Project:           parent.Project,
		ParentID:          parent.ID,
		ForkIndex:         atTurn,
		Summary:           parent.Summary,
		CompactedTurnKeys: append([]string(nil), parent.CompactedTurnKeys...),
		Team:              append([]string(nil), parent.Team...),
		Turns:             append([]Turn(nil), parent.Turns[:atTurn]...),
		CreatedAt:         now,
		UpdatedAt:         now,
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
// title from the first user turn while the title is still unset). A turn
// arriving without a stamp gets one here, so every transcript written from
// now on can be placed on the usage calendar.
func (s *Store) AppendTurn(id string, turn Turn) (*Session, error) {
	return s.AppendTurns(id, turn)
}

// AppendTurns appends several turns under one lock and one atomic write, so a
// caller holding a complete exchange never persists half of it: a question
// stored without its answer would be replayed back-to-back with the next
// prompt, which the Messages API rejects, wedging every later turn. Title
// derivation and timestamps follow AppendTurn.
//
// A batch that files a finished answer leaves a rewind point (see
// checkpoint.go). Best-effort and after the lock is released: the snapshot
// shells out to git and must not block another surface's append, and a missing
// snapshot costs one rewind target, not the turn itself. Threads without a
// worktree cost a file read and return immediately.
func (s *Store) AppendTurns(id string, turns ...Turn) (*Session, error) {
	sess, appended, err := s.appendTurns(id, turns...)
	if err != nil {
		return nil, err
	}
	if !appended {
		return sess, nil
	}
	for _, turn := range turns {
		if turn.Role == "assistant" {
			_ = s.Checkpoint(context.Background(), id)
			break
		}
	}
	return sess, nil
}

// appendTurns is AppendTurns' locked half: the cross-process and in-process
// read-modify-write, with no snapshotting in the middle of it.
func (s *Store) appendTurns(id string, turns ...Turn) (*Session, bool, error) {
	if len(turns) == 0 {
		sess, err := s.Get(id)
		return sess, false, err
	}
	// Cross-process first, then in-process: the CLI and a panel can both be
	// appending to one thread, and the read-modify-write below would drop one
	// side's turn without the file lock. Order matters — the file lock is the
	// outer one so a goroutine waiting in-process cannot hold a half-claimed
	// file lock.
	unlock, err := lockStore(s.root)
	if err != nil {
		return nil, false, fmt.Errorf("sessions: lock: %w", err)
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.load(id)
	if err != nil {
		return nil, false, err
	}
	keys := make(map[string]bool)
	for _, key := range sess.CompactedTurnKeys {
		keys[key] = true
	}
	for _, turn := range sess.Turns {
		if turn.IdempotencyKey != "" {
			keys[turn.IdempotencyKey] = true
		}
	}
	appended := false
	now := time.Now()
	for _, turn := range turns {
		if turn.IdempotencyKey != "" {
			if keys[turn.IdempotencyKey] {
				continue
			}
			keys[turn.IdempotencyKey] = true
		}
		appended = true
		if turn.TS == 0 {
			turn.TS = now.Unix()
		}
		sess.Turns = append(sess.Turns, turn)
		if turn.Role == "user" && sess.Title == "" {
			sess.Title = truncateTitle(turn.Text)
		}
	}
	if !appended {
		return sess, false, nil
	}
	sess.UpdatedAt = now
	if err := s.save(sess); err != nil {
		return nil, false, err
	}
	return sess, true, nil
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

	// The write-back is a fresh read-modify-write of the same file every other
	// surface edits: the file lock has to cover it, or an AppendTurn landing
	// between this load and save is overwritten by the trimmed copy. It is
	// taken only here — summarize above can run for seconds and holds nothing.
	// File lock before the mutex, like every other writer in this package: the
	// reverse order deadlocks against a SetTitle holding the mutex and waiting
	// for the same lock.
	unlock, lerr := lockStore(s.root)
	if lerr != nil {
		return nil, fmt.Errorf("sessions: lock: %w", lerr)
	}
	defer unlock()
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
	// Archive the producer keys in the same atomic save as the compaction.
	// A crash/replayed finalizer then cannot resurrect a summarized message.
	keys := make(map[string]bool, len(fresh.CompactedTurnKeys))
	for _, key := range fresh.CompactedTurnKeys {
		keys[key] = true
	}
	for _, turn := range fresh.Turns[:len(evicted)] {
		if key := turn.IdempotencyKey; key != "" && !keys[key] {
			fresh.CompactedTurnKeys = append(fresh.CompactedTurnKeys, key)
			keys[key] = true
		}
	}
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
	unlock, err := lockStore(s.root)
	if err != nil {
		return false, fmt.Errorf("sessions: lock: %w", err)
	}
	defer unlock()
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
	unlock, err := lockStore(s.root)
	if err != nil {
		return fmt.Errorf("sessions: lock: %w", err)
	}
	defer unlock()
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

// maxSideTurns bounds one stored side conversation. A side thread is a
// thinking space, not an archive: the newest 200 messages keep the working
// context without letting a pane grow the session file without bound. The
// bound is per chat — a thread holding five of them keeps five transcripts,
// each still bounded.
const maxSideTurns = 200

// legacySideChatID names the single conversation a pre-list session carries.
// Fixed rather than generated so two reads of the same file before any write
// see the same id.
const legacySideChatID = "legacy"

// SideChatByID returns the named side chat, or nil. The pointer is into the
// session's slice: a caller holding the session may edit it and save.
func (sess *Session) SideChatByID(id string) *SideChat {
	if id == "" {
		return nil
	}
	for i := range sess.SideChats {
		if sess.SideChats[i].ID == id {
			return &sess.SideChats[i]
		}
	}
	return nil
}

// ActiveSideChat returns the chat the console is showing, falling back to the
// newest one when the pointer is stale or unset.
func (sess *Session) ActiveSideChat() *SideChat {
	if chat := sess.SideChatByID(sess.SideActive); chat != nil {
		return chat
	}
	if len(sess.SideChats) == 0 {
		return nil
	}
	return &sess.SideChats[len(sess.SideChats)-1]
}

// normalizeSide folds the one-conversation shape into the list. It runs on
// every load, so a file written before side chats were a list keeps its
// transcript without a migration pass: the first write persists the new shape
// and the legacy fields fall away.
func (sess *Session) normalizeSide() {
	if len(sess.SideChats) == 0 && (len(sess.SideTurns) > 0 || sess.SideOperation != nil) {
		chat := SideChat{ID: legacySideChatID, Turns: sess.SideTurns, Operation: sess.SideOperation}
		chat.Title = sideChatTitle(chat.Turns)
		if n := len(chat.Turns); n > 0 {
			chat.CreatedAt = chat.Turns[0].TS
			chat.UpdatedAt = chat.Turns[n-1].TS
		}
		sess.SideChats = []SideChat{chat}
	}
	sess.SideTurns = nil
	sess.SideOperation = nil
	if len(sess.SideChats) == 0 {
		sess.SideActive = ""
		return
	}
	if sess.SideChatByID(sess.SideActive) == nil {
		sess.SideActive = sess.SideChats[len(sess.SideChats)-1].ID
	}
}

// sideChatTitle is the first question on one line, short enough to scan in a
// 320px list.
func sideChatTitle(turns []SideTurn) string {
	for _, t := range turns {
		if t.Role != "user" {
			continue
		}
		line := strings.Join(strings.Fields(t.Text), " ")
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > 40 {
			return string(r[:40]) + "…"
		}
		return line
	}
	return ""
}

// trimSideChat keeps the newest maxSideTurns messages, dropping whole
// exchanges so the oldest row on screen is never a question without its
// answer.
func trimSideChat(chat *SideChat) {
	excess := len(chat.Turns) - maxSideTurns
	if excess <= 0 {
		return
	}
	if excess%2 == 1 {
		excess++
	}
	chat.Turns = append([]SideTurn(nil), chat.Turns[excess:]...)
}

// addSideChatLocked appends a fresh, empty chat and makes it the active one.
// The caller holds the store lock.
func (s *Store) addSideChatLocked(sess *Session) *SideChat {
	now := time.Now().Unix()
	id := ""
	for i := 0; i < 8 && id == ""; i++ {
		uid, err := util.UUIDv7()
		if err != nil {
			break
		}
		if candidate := strings.ReplaceAll(uid, "-", ""); sess.SideChatByID(candidate) == nil {
			id = candidate
		}
	}
	if id == "" {
		id = fmt.Sprintf("side-%d", time.Now().UnixNano())
	}
	sess.SideChats = append(sess.SideChats, SideChat{ID: id, CreatedAt: now})
	sess.SideActive = id
	return &sess.SideChats[len(sess.SideChats)-1]
}

// sideChatForLocked resolves the chat a write targets: the named one, else the
// active one, else a new one. A named chat that is gone (deleted on another
// device) is not a reason to fail the ask — the question lands in a fresh
// conversation instead of being dropped.
func (s *Store) sideChatForLocked(sess *Session, chatID string) *SideChat {
	if chat := sess.SideChatByID(chatID); chat != nil {
		return chat
	}
	if chatID == "" {
		if chat := sess.ActiveSideChat(); chat != nil {
			return chat
		}
	}
	return s.addSideChatLocked(sess)
}

// removeSideChatLocked drops one chat and keeps the active pointer on a
// conversation that still exists.
func (s *Store) removeSideChatLocked(sess *Session, chatID string) (*Session, error) {
	kept := sess.SideChats[:0]
	for _, c := range sess.SideChats {
		if c.ID != chatID {
			kept = append(kept, c)
		}
	}
	sess.SideChats = kept
	if sess.SideActive == chatID {
		sess.SideActive = ""
		if n := len(sess.SideChats); n > 0 {
			sess.SideActive = sess.SideChats[n-1].ID
		}
	}
	if err := s.save(sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// AppendSideTurn appends one message to the thread's active side conversation.
// It deliberately leaves the session's UpdatedAt alone: side chatter is not
// thread activity, and letting it reorder the console's rail would make the
// sidebar jump every time the reader asks a side question.
func (s *Store) AppendSideTurn(id string, turn SideTurn) (*Session, error) {
	return s.AppendSideTurnTo(id, "", turn)
}

// AppendSideTurnTo appends one message to a named side chat ("" = the active
// one), creating the chat when the thread has none.
func (s *Store) AppendSideTurnTo(id, chatID string, turn SideTurn) (*Session, error) {
	unlock, err := lockStore(s.root)
	if err != nil {
		return nil, fmt.Errorf("sessions: lock: %w", err)
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.load(id)
	if err != nil {
		return nil, err
	}
	chat := s.sideChatForLocked(sess, chatID)
	if turn.TS == 0 {
		turn.TS = time.Now().Unix()
	}
	if turn.Role == "user" && chat.Title == "" {
		chat.Title = sideChatTitle([]SideTurn{turn})
	}
	chat.Turns = append(chat.Turns, turn)
	trimSideChat(chat)
	chat.UpdatedAt = turn.TS
	sess.SideActive = chat.ID
	if err := s.save(sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// StartSideOperation installs a new side operation generation on the active
// chat. Like a side turn, it does not touch the session's UpdatedAt: the
// thread's recency is its own conversation, not the questions asked beside it.
func (s *Store) StartSideOperation(id string, op Operation) error {
	return s.StartSideOperationFor(id, "", op)
}

// StartSideOperationFor installs a new side operation generation on one chat.
func (s *Store) StartSideOperationFor(id, chatID string, op Operation) error {
	unlock, err := lockStore(s.root)
	if err != nil {
		return fmt.Errorf("sessions: lock: %w", err)
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.load(id)
	if err != nil {
		return err
	}
	chat := s.sideChatForLocked(sess, chatID)
	op.UpdatedAt = time.Now()
	chat.Operation = &op
	sess.SideActive = chat.ID
	return s.save(sess)
}

// SetSideOperation updates a side operation only while the stored record still
// carries op.ID. Unlike SetOperation, a cleared record (the chat was deleted)
// is not recreated: a late finalizer from a cancelled ask must not resurrect a
// conversation the user just discarded.
func (s *Store) SetSideOperation(id string, op Operation) (bool, error) {
	unlock, err := lockStore(s.root)
	if err != nil {
		return false, fmt.Errorf("sessions: lock: %w", err)
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.load(id)
	if err != nil {
		return false, err
	}
	// The operation id is unique per ask, so the chat that owns it is the chat
	// this write belongs to — the reader may have switched away since.
	for i := range sess.SideChats {
		chat := &sess.SideChats[i]
		if chat.Operation == nil || chat.Operation.ID != op.ID {
			continue
		}
		op.UpdatedAt = time.Now()
		chat.Operation = &op
		if err := s.save(sess); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// FinishSideOperation files an answer and settles the operation in one locked
// write. A reader must never observe "done" before the answer is on disk, and
// an answer from an ask whose chat was deleted (or whose question was
// superseded) must not land in another transcript.
func (s *Store) FinishSideOperation(id, operationID string, turn SideTurn, op Operation) (bool, error) {
	unlock, err := lockStore(s.root)
	if err != nil {
		return false, fmt.Errorf("sessions: lock: %w", err)
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.load(id)
	if err != nil {
		return false, err
	}
	var chat *SideChat
	for i := range sess.SideChats {
		if c := &sess.SideChats[i]; c.Operation != nil && c.Operation.ID == operationID {
			chat = c
			break
		}
	}
	if chat == nil {
		return false, nil
	}
	if turn.TS == 0 {
		turn.TS = time.Now().Unix()
	}
	chat.Turns = append(chat.Turns, turn)
	trimSideChat(chat)
	chat.UpdatedAt = turn.TS
	op.UpdatedAt = time.Now()
	chat.Operation = &op
	if err := s.save(sess); err != nil {
		return false, err
	}
	return true, nil
}

// ResetSide discards the side conversation the pane is showing — `/side close`
// in the TUI, and a DELETE from a console that predates the list. The other
// conversations beside this thread are left alone.
func (s *Store) ResetSide(id string) (*Session, error) {
	unlock, err := lockStore(s.root)
	if err != nil {
		return nil, fmt.Errorf("sessions: lock: %w", err)
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.load(id)
	if err != nil {
		return nil, err
	}
	chat := sess.ActiveSideChat()
	if chat == nil {
		return sess, nil
	}
	return s.removeSideChatLocked(sess, chat.ID)
}

// NewSideChat starts another conversation beside the thread and makes it the
// active one. Nothing with a transcript is discarded: the previous chats stay
// in the list. An empty chat is a draft the writer never sent, so starting
// another one replaces it instead of stacking blanks in the file.
func (s *Store) NewSideChat(id string) (*Session, error) {
	unlock, err := lockStore(s.root)
	if err != nil {
		return nil, fmt.Errorf("sessions: lock: %w", err)
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.load(id)
	if err != nil {
		return nil, err
	}
	kept := sess.SideChats[:0]
	for _, c := range sess.SideChats {
		if len(c.Turns) > 0 {
			kept = append(kept, c)
		}
	}
	sess.SideChats = kept
	s.addSideChatLocked(sess)
	if err := s.save(sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// SelectSideChat moves the active pointer to a conversation that exists. It
// writes only that pointer: the chats' own clocks are their last message.
func (s *Store) SelectSideChat(id, chatID string) (*Session, error) {
	unlock, err := lockStore(s.root)
	if err != nil {
		return nil, fmt.Errorf("sessions: lock: %w", err)
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.load(id)
	if err != nil {
		return nil, err
	}
	if sess.SideChatByID(chatID) == nil {
		return nil, ErrNotFound
	}
	sess.SideActive = chatID
	if err := s.save(sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// DeleteSideChat removes one conversation beside the thread.
func (s *Store) DeleteSideChat(id, chatID string) (*Session, error) {
	unlock, err := lockStore(s.root)
	if err != nil {
		return nil, fmt.Errorf("sessions: lock: %w", err)
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.load(id)
	if err != nil {
		return nil, err
	}
	if sess.SideChatByID(chatID) == nil {
		return nil, ErrNotFound
	}
	return s.removeSideChatLocked(sess, chatID)
}

// TrimSideTurns returns the tail of a side conversation that fits budget
// characters, dropping whole exchanges and always keeping the newest one. The
// stored conversation keeps every message; this only bounds what is replayed
// to the model on the next question.
func TrimSideTurns(turns []SideTurn, budget int) []SideTurn {
	if budget <= 0 || len(turns) == 0 {
		return append([]SideTurn(nil), turns...)
	}
	total := 0
	for _, t := range turns {
		total += len(t.Text)
	}
	kept := turns
	for total > budget && len(kept) > 2 {
		drop := 2
		if len(kept)%2 == 1 {
			drop = 1
		}
		for i := 0; i < drop && i < len(kept); i++ {
			total -= len(kept[i].Text)
		}
		kept = kept[drop:]
	}
	return append([]SideTurn(nil), kept...)
}

// SetWorktree records the worktree path/branch on a session.
func (s *Store) SetWorktree(id, path, branch string) error {
	unlock, err := lockStore(s.root)
	if err != nil {
		return fmt.Errorf("sessions: lock: %w", err)
	}
	defer unlock()
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
	unlock, err := lockStore(s.root)
	if err != nil {
		return fmt.Errorf("sessions: lock: %w", err)
	}
	defer unlock()
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

// SetTeam records which agents this thread may delegate to. An empty list
// clears the restriction (the scheduler routes by capability again), which is
// also what a thread that never picked a team carries.
func (s *Store) SetTeam(id string, team []string) error {
	// Validate here as well as at the HTTP/CLI edge: SetTeam is the one write
	// path every surface funnels through, and a malformed name that slips past
	// a caller would make every task in the thread unroutable.
	clean, err := CleanTeam(team)
	if err != nil {
		return err
	}
	unlock, err := lockStore(s.root)
	if err != nil {
		return fmt.Errorf("sessions: lock: %w", err)
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.load(id)
	if err != nil {
		return err
	}
	// Copy: the caller keeps its slice (the panel reuses the decoded request
	// body) and a later mutation must not rewrite a saved thread's roster.
	sess.Team = clean
	sess.UpdatedAt = time.Now()
	return s.save(sess)
}

// SetPermissionMode writes the conversation's permission policy. An empty
// mode clears it (back to auto). Validation lives here because every surface
// funnels through this setter: a typo must be refused, never normalized into
// a policy the user did not choose.
func (s *Store) SetPermissionMode(id, mode string) error {
	if mode != "" && !defense.ValidMode(mode) {
		return fmt.Errorf("sessions: unknown permission mode %q", mode)
	}
	unlock, err := lockStore(s.root)
	if err != nil {
		return fmt.Errorf("sessions: lock: %w", err)
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.load(id)
	if err != nil {
		return err
	}
	if mode == defense.ModeAuto {
		mode = ""
	}
	sess.PermissionMode = mode
	sess.UpdatedAt = time.Now()
	return s.save(sess)
}

// SetTitle updates a session's title.
func (s *Store) SetTitle(id, title string) error {
	unlock, err := lockStore(s.root)
	if err != nil {
		return fmt.Errorf("sessions: lock: %w", err)
	}
	defer unlock()
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
	unlock, err := lockStore(s.root)
	if err != nil {
		return fmt.Errorf("sessions: lock: %w", err)
	}
	defer unlock()
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
	unlock, err := lockStore(s.root)
	if err != nil {
		return fmt.Errorf("sessions: lock: %w", err)
	}
	defer unlock()
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
	unlock, lerr := lockStore(s.root)
	if lerr != nil {
		return 0, fmt.Errorf("sessions: lock: %w", lerr)
	}
	defer unlock()
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
// The thread's rewind snapshots go with it: they live in the worktree's git
// directory and nothing would name them once the file is gone.
func (s *Store) Delete(id string) error {
	var worktree string
	if sess, err := s.Get(id); err == nil {
		worktree = sess.Worktree
	}
	if err := s.deleteFile(id); err != nil {
		return err
	}
	if worktree != "" {
		dropCheckpoints(context.Background(), worktree, id)
	}
	return nil
}

// deleteFile is Delete's locked half.
func (s *Store) deleteFile(id string) error {
	unlock, err := lockStore(s.root)
	if err != nil {
		return fmt.Errorf("sessions: lock: %w", err)
	}
	defer unlock()
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

// newIDLocked allocates a session id that no file currently uses. It keeps
// the full UUIDv7 hex (128 bits, timestamp first) rather than a truncation:
// an earlier build cut it to 16 hex characters, of which only 12 were
// random, and two sessions created in the same millisecond then collided
// with probability 1/4096 — save() overwrote the first transcript while both
// callers reported success. The existence check also covers an id supplied
// by a clock that moved backwards.
func (s *Store) newIDLocked() (string, error) {
	for i := 0; i < 8; i++ {
		uid, err := util.UUIDv7()
		if err != nil {
			return "", fmt.Errorf("sessions: id: %w", err)
		}
		id := strings.ReplaceAll(uid, "-", "")
		switch _, err := os.Stat(s.path(id)); {
		case os.IsNotExist(err):
			return id, nil
		case err != nil:
			return "", fmt.Errorf("sessions: id check: %w", err)
		}
	}
	return "", errors.New("sessions: could not allocate a unique id")
}

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
	// The single-conversation shape becomes a list of one on the way in, so
	// every caller downstream reads one model. A file that predates the list
	// keeps its transcript; the first write persists the new shape.
	sess.normalizeSide()
	return &sess, nil
}

func (s *Store) save(sess *Session) error {
	// load validates the id; save must too, because the id becomes a file
	// name. A session object written by an importer or hand-edited on disk
	// with "../x" would otherwise put the transcript outside the root.
	if !ValidID(sess.ID) {
		return fmt.Errorf("sessions: invalid session id %q", sess.ID)
	}
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	// Atomic: a crash mid-write would leave a truncated file that fails to
	// parse — invisible in List and unreadable in Get, i.e. a silently lost
	// conversation. 0o600, not 0o644: a transcript is verbatim user text and
	// routinely carries pasted secrets the config store already guards.
	return util.WriteFileAtomic(s.path(sess.ID), data, 0o600)
}

// Title derives a thread title from a prompt: one rail row, at most 48 runes.
// Both the console and the CLI create threads with it, so a conversation has
// the same label whichever surface opened it.
func Title(prompt string) string {
	prompt = strings.Join(strings.Fields(prompt), " ")
	if prompt == "" {
		return ""
	}
	runes := []rune(prompt)
	if len(runes) > 48 {
		return string(runes[:48]) + "…"
	}
	return prompt
}

func truncateTitle(s string) string {
	return Title(s)
}

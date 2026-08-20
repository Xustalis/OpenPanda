package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Xustalis/OpenPanda/internal/util"
)

// Hermes manages the Hermes personal-assistant memory store rooted at a
// memory/ directory (design §17.2). Following Hermes Agent, personal memory is
// split into two files with distinct roles and caps:
//
//	USER.md   who the user is — role, preferences, communication style
//	MEMORY.md what the agent knows — environment facts, conventions, corrections
//
// On top of the two core files, optional topic files under memory/topics/
// extend the hot layer into multiple files (A3 multi-file memory); agents
// load them selectively instead of receiving everything in every prompt.
//
// The remaining layers are the warm daily logs (daily/) and the cold Dreaming
// state (.dreams/, OpenClaw layout, Sprint 3.2).
type Hermes struct {
	root   string
	limits Limits     // configured caps; zero fields fall back to the package constants
	mu     sync.Mutex // serializes whole-file writes so concurrent saves cannot interleave
}

// NewHermes wraps a memory/ root directory with the historical compile-time
// caps. Configured deployments use NewHermesWithLimits instead.
func NewHermes(root string) *Hermes {
	return NewHermesWithLimits(root, Limits{})
}

// NewHermesWithLimits wraps a memory/ root directory with configurable
// character caps (config memory.limits); non-positive fields fall back to the
// package constants.
func NewHermesWithLimits(root string, limits Limits) *Hermes {
	return &Hermes{root: root, limits: limits}
}

// LoadUser reads USER.md with the user-profile limit applied, so subsequent
// Add/Replace enforce the cap. A missing file yields an empty MemFile.
func (h *Hermes) LoadUser() (MemFile, error) {
	return h.load(UserPath(h.root), h.limits.user())
}

// LoadMemory reads MEMORY.md with the hot-layer limit applied.
func (h *Hermes) LoadMemory() (MemFile, error) {
	return h.load(MemoryPath(h.root), h.limits.memory())
}

// SaveUser writes USER.md, enforcing the user-profile cap.
func (h *Hermes) SaveUser(m MemFile) error {
	return h.save(UserPath(h.root), m, h.limits.user())
}

// SaveMemory writes MEMORY.md, enforcing the hot-layer cap.
func (h *Hermes) SaveMemory(m MemFile) error {
	return h.save(MemoryPath(h.root), m, h.limits.memory())
}

// TopicsDir is the memory/topics/ directory holding optional topic files.
func (h *Hermes) TopicsDir() string { return filepath.Join(h.root, "topics") }

// TopicPath returns the on-disk path of a topic file, validating the name
// first so it cannot escape the topics directory (directory traversal).
func (h *Hermes) TopicPath(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	return filepath.Join(h.TopicsDir(), name+".md"), nil
}

// ListTopics returns the names (file stem, sorted) of the topic files that
// exist under topics/. A missing directory yields an empty list.
func (h *Hermes) ListTopics() ([]string, error) {
	entries, err := os.ReadDir(h.TopicsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("memory: list topics: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if stem, ok := strings.CutSuffix(e.Name(), ".md"); ok && ValidateName(stem) == nil {
			names = append(names, stem)
		}
	}
	sort.Strings(names)
	return names, nil
}

// LoadTopic reads one topic file with the hot-layer (memory) limit applied. A
// missing file yields an empty MemFile, like the core files.
func (h *Hermes) LoadTopic(name string) (MemFile, error) {
	path, err := h.TopicPath(name)
	if err != nil {
		return MemFile{}, err
	}
	return h.load(path, h.limits.memory())
}

// SaveTopic writes one topic file (creating the topics/ directory as needed),
// enforcing the hot-layer cap and writing atomically, same as the core files.
func (h *Hermes) SaveTopic(name string, m MemFile) error {
	path, err := h.TopicPath(name)
	if err != nil {
		return err
	}
	return h.save(path, m, h.limits.memory())
}

// DeleteTopic removes one topic file. Deleting a missing topic is an error so
// callers can tell a typo from a removal.
func (h *Hermes) DeleteTopic(name string) error {
	path, err := h.TopicPath(name)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("memory: no such topic %q", name)
		}
		return fmt.Errorf("memory: delete topic %q: %w", name, err)
	}
	return nil
}

// summaryRunes caps the per-file summary in the manifest index — long enough
// to be a real hint, short enough that the manifest itself stays cheap.
const summaryRunes = 40

// FileSummary indexes one memory file for selective loading (A3) and the
// console: where it lives, how big it is, and a one-line hint of its content.
type FileSummary struct {
	Name    string // "USER.md", "MEMORY.md", "topics/<name>.md"
	Path    string // absolute on-disk path
	Entries int
	Chars   int
	Summary string // first entry, truncated to summaryRunes
}

// Files indexes the non-empty personal memory files: USER.md, MEMORY.md and
// every topic file. Empty or missing files are omitted — a file with nothing
// in it is not worth an agent's read.
func (h *Hermes) Files() ([]FileSummary, error) {
	type src struct {
		name string
		file MemFile
	}
	srcs := make([]src, 0, 4)
	if m, err := h.LoadUser(); err != nil {
		return nil, err
	} else if len(m.Entries) > 0 {
		srcs = append(srcs, src{"USER.md", m})
	}
	if m, err := h.LoadMemory(); err != nil {
		return nil, err
	} else if len(m.Entries) > 0 {
		srcs = append(srcs, src{"MEMORY.md", m})
	}
	names, err := h.ListTopics()
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		m, err := h.LoadTopic(name)
		if err != nil || len(m.Entries) == 0 {
			continue // unreadable or empty topic: not worth listing
		}
		srcs = append(srcs, src{"topics/" + name + ".md", m})
	}
	out := make([]FileSummary, 0, len(srcs))
	for _, s := range srcs {
		out = append(out, FileSummary{
			Name:    s.name,
			Path:    h.absPath(filepath.Join(h.root, s.name)),
			Entries: len(s.file.Entries),
			Chars:   s.file.Chars(),
			Summary: summarize(s.file.Entries[0]),
		})
	}
	return out, nil
}

// absPath normalizes a memory file path to absolute so agents and the
// orchestration layer can read it regardless of their working directory.
func (h *Hermes) absPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// summarize truncates an entry into a short index hint.
func summarize(entry string) string {
	r := []rune(entry)
	if len(r) <= summaryRunes {
		return entry
	}
	return string(r[:summaryRunes]) + "…"
}

// load reads one memory file and applies its limit. The returned MemFile is a
// frozen snapshot: later writes do not alter it, matching Hermes's
// load-once-at-session-start behavior (which also preserves prompt caching).
func (h *Hermes) load(path string, limit int) (MemFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return MemFile{Limit: limit}, nil
		}
		return MemFile{}, fmt.Errorf("memory: read %s: %w", filepath.Base(path), err)
	}
	m := ParseMem(data)
	m.Limit = limit
	return m, nil
}

// save writes one memory file, creating its directory as needed. It enforces
// the character cap and refuses an over-limit file with ErrOverLimit rather
// than silently truncating.
func (h *Hermes) save(path string, m MemFile, defaultLimit int) error {
	limit := m.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if m.Chars() > limit {
		return fmt.Errorf("%w: at %d/%d chars", ErrOverLimit, m.Chars(), limit)
	}
	// Serialize the write so a concurrent SaveMemory/SaveUser cannot truncate and
	// interleave with this one, which would leave a torn MEMORY.md on disk.
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("memory: create memory dir: %w", err)
	}
	if err := util.WriteFileAtomic(path, m.Bytes(), 0o644); err != nil {
		return fmt.Errorf("memory: write %s: %w", filepath.Base(path), err)
	}
	return nil
}

// UserPath is the USER.md location for a memory root.
func UserPath(root string) string { return filepath.Join(root, "USER.md") }

// MemoryPath is the MEMORY.md location for a memory root.
func MemoryPath(root string) string { return filepath.Join(root, "MEMORY.md") }

// WarmDir is the daily/ directory.
func (h *Hermes) WarmDir() string { return filepath.Join(h.root, "daily") }

// ColdDir is the .dreams/ directory holding Dreaming engine state (OpenClaw's
// machine-state location, Sprint 3.2).
func (h *Hermes) ColdDir() string { return filepath.Join(h.root, ".dreams") }

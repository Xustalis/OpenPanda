package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/xenith/openpanda/internal/util"
)

// Hermes manages the Hermes personal-assistant memory store rooted at a
// memory/ directory (design §17.2). Following Hermes Agent, personal memory is
// split into two files with distinct roles and caps:
//
//	USER.md   who the user is — role, preferences, communication style (1375 chars)
//	MEMORY.md what the agent knows — environment facts, conventions, corrections (2200 chars)
//
// The remaining layers are the warm daily logs (daily/) and the cold Dreaming
// state (.dreams/, OpenClaw layout, Sprint 3.2).
type Hermes struct {
	root string
	mu   sync.Mutex // serializes whole-file writes so concurrent saves cannot interleave
}

// NewHermes wraps a memory/ root directory.
func NewHermes(root string) *Hermes {
	return &Hermes{root: root}
}

// LoadUser reads USER.md with the user-profile limit applied, so subsequent
// Add/Replace enforce the cap. A missing file yields an empty MemFile.
func (h *Hermes) LoadUser() (MemFile, error) {
	return h.load(UserPath(h.root), UserCharLimit)
}

// LoadMemory reads MEMORY.md with the hot-layer limit applied.
func (h *Hermes) LoadMemory() (MemFile, error) {
	return h.load(MemoryPath(h.root), MemoryCharLimit)
}

// SaveUser writes USER.md, enforcing the user-profile cap (defaulting to
// UserCharLimit when unset).
func (h *Hermes) SaveUser(m MemFile) error {
	return h.save(UserPath(h.root), m, UserCharLimit)
}

// SaveMemory writes MEMORY.md, enforcing the hot-layer cap.
func (h *Hermes) SaveMemory(m MemFile) error {
	return h.save(MemoryPath(h.root), m, MemoryCharLimit)
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

// save writes one memory file, creating the directory as needed. It enforces
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
	if err := os.MkdirAll(h.root, 0o755); err != nil {
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

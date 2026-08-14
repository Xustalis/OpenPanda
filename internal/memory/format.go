// Package memory implements the two-layer memory system (design doc §17):
// Hermes personal-assistant memory and per-project memory, kept physically
// isolated by an isolation wall. The on-disk format and hot-layer model follow
// Hermes Agent (Nous Research); the cold-layer layout follows OpenClaw.
//
// Layout (design doc §17.2, aligned to upstream):
//
//	memory/                  Hermes personal memory
//	  MEMORY.md              hot layer — §-separated entries, 2200-char cap
//	  daily/YYYY-MM-DD.md    warm layer — operational logs (30d archive → 90d delete)
//	  .dreams/               cold layer — Dreaming engine state (OpenClaw, Sprint 3.2)
//	  DREAMS.md              human-readable dream diary (Sprint 3.2)
//	projects/{name}/MEMORY.md project memory — one §-separated file per project
//
// The isolation wall is structural: Hermes memory is injected only into
// conversation/short-task system prompts, and project memory is injected only
// into that project's agent execution context. The two never cross.
package memory

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Character caps follow Hermes Agent: limits are measured in characters, not
// tokens, so they stay model-agnostic. Hermes splits memory into two files —
// USER.md (who the user is, ~500 tokens) and MEMORY.md (what the agent knows
// about the world, ~800 tokens) — and PANDA gives project memory more room
// because it is injected into a full agent context (design §17.2).
const (
	UserCharLimit    = 1375 // Hermes USER.md (~500 tokens)
	MemoryCharLimit  = 2200 // Hermes MEMORY.md (~800 tokens)
	ProjectCharLimit = 8000 // per-project MEMORY.md (PANDA-specific)
)

// entrySep is the Hermes delimiter between memory entries (U+00A7).
const entrySep = "§"

// ErrOverLimit reports that an add/replace would exceed the file's character
// limit. The caller (or the agent) should consolidate entries and retry, which
// is the Hermes workflow: merge proactively above ~80% capacity.
var ErrOverLimit = errors.New("memory: character limit exceeded")

// MemFile is an in-memory MEMORY.md: an ordered list of entries separated on
// disk by "§". The zero value is an empty file. Limit is the character cap; a
// non-positive value means unlimited.
type MemFile struct {
	Entries []string
	Limit   int
}

// ParseMem decodes MEMORY.md bytes into a MemFile. Entries are split on "§" and
// surrounding whitespace is trimmed. The Limit is not read from the file — the
// caller sets it from the store that owns the file.
func ParseMem(data []byte) MemFile {
	s := strings.TrimSpace(string(data))
	if s == "" {
		return MemFile{}
	}
	parts := strings.Split(s, entrySep)
	entries := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			entries = append(entries, t)
		}
	}
	return MemFile{Entries: entries}
}

// Bytes serializes the entries back to the on-disk format ("§"-separated).
func (m MemFile) Bytes() []byte {
	return []byte(strings.Join(m.Entries, "\n"+entrySep+"\n"))
}

// Chars returns the rune count of the serialized file (entries plus
// separators), the measure the character limit applies to.
func (m MemFile) Chars() int {
	return utf8.RuneCountInString(string(m.Bytes()))
}

// Add appends an entry, rejecting empty and exact-duplicate entries and
// enforcing the character limit when set. An over-limit add is rolled back and
// reported with current usage so the agent can consolidate before retrying.
func (m *MemFile) Add(entry string) error {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return fmt.Errorf("memory: entry must not be empty")
	}
	for _, e := range m.Entries {
		if e == entry {
			return fmt.Errorf("memory: duplicate entry")
		}
	}
	before := m.Chars()
	m.Entries = append(m.Entries, entry)
	if m.Limit > 0 && m.Chars() > m.Limit {
		m.Entries = m.Entries[:len(m.Entries)-1]
		return fmt.Errorf("%w: at %d/%d chars", ErrOverLimit, before, m.Limit)
	}
	return nil
}

// Replace swaps the entry containing old with new. Matching is substring-based
// (Hermes semantics): old must match exactly one entry, else the change is
// rejected so the caller can supply a more specific substring. An over-limit
// replacement is rolled back so the MemFile stays consistent.
func (m *MemFile) Replace(old, new string) error {
	idx, ok := m.match(old)
	if !ok {
		return fmt.Errorf("memory: %q matches no unique entry", old)
	}
	prev := m.Entries[idx]
	m.Entries[idx] = strings.TrimSpace(new)
	if m.Limit > 0 && m.Chars() > m.Limit {
		m.Entries[idx] = prev // roll back
		return fmt.Errorf("%w: at %d/%d chars", ErrOverLimit, m.Chars(), m.Limit)
	}
	return nil
}

// Remove deletes the entry containing old, again requiring a unique substring
// match. An empty replacement is not used here — callers drop entries outright.
func (m *MemFile) Remove(old string) error {
	idx, ok := m.match(old)
	if !ok {
		return fmt.Errorf("memory: %q matches no unique entry", old)
	}
	m.Entries = append(m.Entries[:idx], m.Entries[idx+1:]...)
	return nil
}

// match returns the index of the entry containing sub and whether the match is
// unique. A substring present in multiple entries is not unique.
func (m *MemFile) match(sub string) (int, bool) {
	idx, found := -1, false
	for i, e := range m.Entries {
		if strings.Contains(e, sub) {
			if found {
				return -1, false
			}
			idx, found = i, true
		}
	}
	return idx, found
}

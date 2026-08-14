package memory

import (
	"errors"
	"strings"
	"testing"
)

func TestHermesSaveLoadUser(t *testing.T) {
	h := NewHermes(t.TempDir())
	m := MemFile{Entries: []string{"prefers dark mode", "communication: terse"}}
	if err := h.SaveUser(m); err != nil {
		t.Fatalf("save user: %v", err)
	}
	got, err := h.LoadUser()
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	if len(got.Entries) != 2 || got.Entries[0] != "prefers dark mode" {
		t.Errorf("round-trip mismatch: %v", got.Entries)
	}
	if got.Limit != UserCharLimit {
		t.Errorf("load should apply the user limit, got %d", got.Limit)
	}
}

func TestHermesSaveLoadMemory(t *testing.T) {
	h := NewHermes(t.TempDir())
	m := MemFile{Entries: []string{"core is Go", "deploy to Orange Pi"}}
	if err := h.SaveMemory(m); err != nil {
		t.Fatalf("save memory: %v", err)
	}
	got, err := h.LoadMemory()
	if err != nil {
		t.Fatalf("load memory: %v", err)
	}
	if len(got.Entries) != 2 || got.Entries[0] != "core is Go" {
		t.Errorf("round-trip mismatch: %v", got.Entries)
	}
	if got.Limit != MemoryCharLimit {
		t.Errorf("load should apply the memory limit, got %d", got.Limit)
	}
}

func TestHermesLoadMissing(t *testing.T) {
	h := NewHermes(t.TempDir())
	user, err := h.LoadUser()
	if err != nil || len(user.Entries) != 0 || user.Limit != UserCharLimit {
		t.Fatalf("missing USER.md: got %v limit=%d err=%v", user.Entries, user.Limit, err)
	}
	mem, err := h.LoadMemory()
	if err != nil || len(mem.Entries) != 0 || mem.Limit != MemoryCharLimit {
		t.Fatalf("missing MEMORY.md: got %v limit=%d err=%v", mem.Entries, mem.Limit, err)
	}
}

func TestHermesSaveEnforcesCap(t *testing.T) {
	h := NewHermes(t.TempDir())
	m := MemFile{Entries: []string{strings.Repeat("x", MemoryCharLimit+100)}}
	if err := h.SaveMemory(m); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("save over cap: got %v, want ErrOverLimit", err)
	}
	got, err := h.LoadMemory()
	if err != nil {
		t.Fatalf("load after failed save: %v", err)
	}
	if len(got.Entries) != 0 {
		t.Errorf("failed save should write nothing, got %v", got.Entries)
	}
}

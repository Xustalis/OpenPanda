// SPDX-License-Identifier: AGPL-3.0-or-later

package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// TestHermesConcurrentSaveNoTornWrite verifies whole-file saves are serialized:
// many goroutines writing distinct snapshots must leave a single complete
// snapshot on disk, never an interleaved/torn one (P2-11 data-loss class).
func TestHermesConcurrentSaveNoTornWrite(t *testing.T) {
	h := NewHermes(t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = h.SaveMemory(MemFile{Entries: []string{fmt.Sprintf("entry-%02d", i)}})
		}(i)
	}
	wg.Wait()

	data, err := os.ReadFile(MemoryPath(h.root))
	if err != nil {
		t.Fatal(err)
	}
	got := ParseMem(data)
	if len(got.Entries) != 1 {
		t.Fatalf("torn write: %d entries, want 1 complete snapshot: %q", len(got.Entries), data)
	}
	if !strings.HasPrefix(got.Entries[0], "entry-") {
		t.Fatalf("unexpected content: %q", got.Entries[0])
	}
}

// TestReadDirOrAbsentSeparatesMissingFromCorrupt pins the platform difference
// that broke Windows CI: a file sitting where memory/topics/ or memory/daily/
// belongs must read as a broken store, not as an empty one. Windows reports
// that case as "not found", so the helper stats the path before believing it.
func TestReadDirOrAbsentSeparatesMissingFromCorrupt(t *testing.T) {
	root := t.TempDir()

	if entries, err := ReadDirOrAbsent(filepath.Join(root, "absent")); err != nil || entries != nil {
		t.Fatalf("missing directory: entries=%v err=%v, want nil, nil", entries, err)
	}

	corrupt := filepath.Join(root, "topics")
	if err := os.WriteFile(corrupt, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDirOrAbsent(corrupt); err == nil {
		t.Fatal("a file where the directory belongs must report an error, not an empty listing")
	}

	real := filepath.Join(root, "daily")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "2026-01-02.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadDirOrAbsent(real)
	if err != nil || len(entries) != 1 {
		t.Fatalf("populated directory: entries=%d err=%v, want 1, nil", len(entries), err)
	}
}

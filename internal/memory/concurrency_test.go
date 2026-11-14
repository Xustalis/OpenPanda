// SPDX-License-Identifier: AGPL-3.0-or-later

package memory

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// TestToolAddLosesNothingUnderConcurrency pins the read-modify-write lock. The
// tool used to load the file, mutate the copy and save it back as three steps:
// two adds that interleave each read a file without the other's entry, and the
// second save drops the first — both report success, and nothing on disk says
// an entry is missing. Two subagents of one round adding a fact at the same
// moment is that case.
func TestToolAddLosesNothingUnderConcurrency(t *testing.T) {
	root := t.TempDir()
	tool := NewTool(NewHermes(root), NewProjects(root))

	const n = 24
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			entry := fmt.Sprintf("entry-%02d", i)
			if _, err := tool.Execute(ToolAdd, map[string]any{
				"target": "memory", "entry": entry, "category": "convention", "key": entry,
			}); err != nil {
				errs <- fmt.Errorf("%s: %w", entry, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent add: %v", err)
	}

	// A second store instance reads what actually landed on disk — the point is
	// the file, not the in-memory copies the writers held.
	m, err := NewHermes(root).LoadMemory()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	seen := make(map[string]bool, len(m.Entries))
	for _, e := range m.Entries {
		fact, err := ClassifyFact(e, "", "")
		if err != nil {
			t.Fatal(err)
		}
		seen[fact.Text] = true
	}
	var missing []string
	for i := 0; i < n; i++ {
		if entry := fmt.Sprintf("entry-%02d", i); !seen[entry] {
			missing = append(missing, entry)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%d/%d entries lost to interleaved writes: %v", len(missing), n, missing)
	}
}

// TestUpdateAbortsOnCallbackError pins that a rejected mutation writes nothing:
// the callback is a validation step (over-limit, duplicate, ambiguous match),
// and persisting the half-built copy after it says no would be worse than the
// error it returned.
func TestUpdateAbortsOnCallbackError(t *testing.T) {
	root := t.TempDir()
	h := NewHermes(root)
	if err := h.SaveMemory(MemFile{Entries: []string{"keep me"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	sentinel := errors.New("nope")
	err := h.UpdateMemory(func(m *MemFile) error {
		m.Entries = append(m.Entries, "should never land")
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("update error = %v, want the callback's", err)
	}
	got, err := h.LoadMemory()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(got.Entries) != 1 || got.Entries[0] != "keep me" {
		t.Fatalf("entries = %v, want only the seeded entry", got.Entries)
	}
}

// TestProjectUpdateIsAtomic is the project-memory counterpart: one project file
// shared by two writers must keep both edits.
func TestProjectUpdateIsAtomic(t *testing.T) {
	root := t.TempDir()
	p := NewProjects(root)
	if err := p.Save("panda", MemFile{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			entry := fmt.Sprintf("fact-%02d", i)
			if err := p.Update("panda", func(m *MemFile) error { return m.Add(entry) }); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent update: %v", err)
	}
	m, err := p.Load("panda")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(m.Entries) != n {
		t.Fatalf("entries = %d, want %d (lost update)", len(m.Entries), n)
	}
}

// TestUpdateSkipsUnchangedWrite pins the no-op case: a dream sweep that ends up
// promoting nothing, or a callback that only read, must not rewrite the file.
// The mtime is pinned to a fixed past instant first, so a rewrite shows up
// regardless of the filesystem's timestamp resolution — WriteFileAtomic renames
// a freshly written file, whose mtime is now.
func TestUpdateSkipsUnchangedWrite(t *testing.T) {
	root := t.TempDir()
	h := NewHermes(root)
	if err := h.SaveMemory(MemFile{Entries: []string{"stable fact"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pinned := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	path := MemoryPath(root)
	if err := os.Chtimes(path, pinned, pinned); err != nil {
		t.Fatalf("pin mtime: %v", err)
	}

	if err := h.UpdateMemory(func(m *MemFile) error { return nil }); err != nil {
		t.Fatalf("no-op update: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.ModTime().Equal(pinned) {
		t.Fatalf("mtime = %v, want the file left untouched", info.ModTime())
	}

	// A real change still writes.
	if err := h.UpdateMemory(func(m *MemFile) error { return m.Add("new fact") }); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := h.LoadMemory()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("entries = %v, want the added fact", got.Entries)
	}
}

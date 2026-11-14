// SPDX-License-Identifier: AGPL-3.0-or-later

package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateName(t *testing.T) {
	valid := []string{"panda", "demo-project", "my-project", "a.b"}
	for _, n := range valid {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}
	invalid := []string{"", ".", "..", "../etc", "a/b", `a\b`, "/etc"}
	for _, n := range invalid {
		if err := ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", n)
		}
	}
}

func TestProjectsSaveLoad(t *testing.T) {
	p := NewProjects(t.TempDir())
	m := MemFile{Entries: []string{"Go core + Python glue", "deploy to Orange Pi"}}
	if err := p.Save("panda", m); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := p.Load("panda")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Entries) != 2 || got.Entries[0] != "Go core + Python glue" {
		t.Errorf("round-trip mismatch: %v", got.Entries)
	}
	if got.Limit != ProjectCharLimit {
		t.Errorf("load should apply the project limit, got %d", got.Limit)
	}
}

// TestProjectsRenameRefusesExistingTarget pins the destination guard.
func TestProjectsRenameRefusesExistingTarget(t *testing.T) {
	p := NewProjects(t.TempDir())
	if err := p.Save("old", MemFile{Entries: []string{"old entry"}}); err != nil {
		t.Fatalf("save old: %v", err)
	}
	if err := p.Save("taken", MemFile{Entries: []string{"target entry"}}); err != nil {
		t.Fatalf("save target: %v", err)
	}
	if err := p.Rename("old", "taken"); err == nil {
		t.Fatal("rename onto an existing project must be refused")
	}
	// Both sides are untouched.
	for name, want := range map[string]string{"old": "old entry", "taken": "target entry"} {
		m, err := p.Load(name)
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		if len(m.Entries) != 1 || m.Entries[0] != want {
			t.Fatalf("%s entries = %v, want %q", name, m.Entries, want)
		}
	}
}

// TestProjectsRenameMovesMemory pins the successful path: the memory lands on
// the new name and the old directory is gone.
func TestProjectsRenameMovesMemory(t *testing.T) {
	p := NewProjects(t.TempDir())
	if err := p.Save("old", MemFile{Entries: []string{"entry"}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := p.Rename("old", "new"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	got, err := p.Load("new")
	if err != nil {
		t.Fatalf("load new: %v", err)
	}
	if len(got.Entries) != 1 || got.Entries[0] != "entry" {
		t.Fatalf("new entries = %v, want the moved entry", got.Entries)
	}
	if old, err := p.Load("old"); err != nil || len(old.Entries) != 0 {
		t.Fatalf("old project still has entries: %v (err %v)", old.Entries, err)
	}
}

func TestProjectsLoadMissing(t *testing.T) {
	p := NewProjects(t.TempDir())
	got, err := p.Load("nope")
	if err != nil {
		t.Fatalf("load missing: %v", err)
	}
	if len(got.Entries) != 0 {
		t.Errorf("missing project should yield empty MemFile, got %v", got.Entries)
	}
}

func TestProjectsPathRejectsTraversal(t *testing.T) {
	p := NewProjects(t.TempDir())
	if _, err := p.Path("../escape"); err == nil {
		t.Errorf("Path with traversal should error")
	}
}

// TestProjectsSaveEnforcesCap verifies Save rejects an over-limit project memory
// rather than silently truncating it (P2-11), mirroring Hermes.save.
func TestProjectsSaveEnforcesCap(t *testing.T) {
	p := NewProjects(t.TempDir())
	m := MemFile{Entries: []string{strings.Repeat("x", ProjectCharLimit+100)}}
	if err := p.Save("panda", m); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("save over cap: got %v, want ErrOverLimit", err)
	}
	got, err := p.Load("panda")
	if err != nil {
		t.Fatalf("load after failed save: %v", err)
	}
	if len(got.Entries) != 0 {
		t.Errorf("failed save should write nothing, got %v", got.Entries)
	}
}

// TestProjectsRenameCarriesMemory pins the directory move contract: the new
// name has the entries, and an invalid new name must not touch the source.
func TestProjectsRenameCarriesMemory(t *testing.T) {
	p := NewProjects(t.TempDir())
	if err := p.Save("old", MemFile{Entries: []string{"one", "two"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := p.Rename("old", "new"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	got, err := p.Load("new")
	if err != nil {
		t.Fatalf("load new: %v", err)
	}
	if len(got.Entries) != 2 || got.Entries[0] != "one" {
		t.Fatalf("new entries = %v", got.Entries)
	}
	if _, err := os.Stat(filepath.Join(p.root, "old")); !os.IsNotExist(err) {
		t.Fatalf("old directory survived the rename: %v", err)
	}
}

func TestProjectsRenameRejectsBadNewName(t *testing.T) {
	p := NewProjects(t.TempDir())
	if err := p.Save("keep", MemFile{Entries: []string{"payload"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := p.Rename("keep", "../escape"); err == nil {
		t.Fatal("expected a traversal-shaped new name to be rejected")
	}
	got, err := p.Load("keep")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("source lost by a rejected rename: %v", got.Entries)
	}
}

func TestProjectsRenameSameNameIsNoop(t *testing.T) {
	p := NewProjects(t.TempDir())
	if err := p.Save("same", MemFile{Entries: []string{"entry-1"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := p.Rename("same", "same"); err != nil {
		t.Fatalf("rename same name: %v", err)
	}
	got, err := p.Load("same")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Entries) != 1 || got.Entries[0] != "entry-1" {
		t.Fatalf("entries lost by same-name rename: %v", got.Entries)
	}
}

func TestProjectsRenameWithRollsBackOnMetadataFailure(t *testing.T) {
	for _, withMemory := range []bool{false, true} {
		t.Run(fmt.Sprint(withMemory), func(t *testing.T) {
			p := NewProjects(t.TempDir())
			if withMemory {
				if err := p.Save("old", MemFile{Entries: []string{"project uses Go"}}); err != nil {
					t.Fatal(err)
				}
			}
			want := errors.New("metadata rejected rename")
			if err := p.RenameWith("old", "new", func() error { return want }); !errors.Is(err, want) {
				t.Fatalf("rename = %v, want %v", err, want)
			}
			if _, err := os.Stat(filepath.Join(p.root, "new")); !os.IsNotExist(err) {
				t.Fatalf("failed rename left destination: %v", err)
			}
			m, err := p.Load("old")
			if err != nil || (withMemory && len(m.Entries) != 1) || (!withMemory && len(m.Entries) != 0) {
				t.Fatalf("source changed: %v, %v", m.Entries, err)
			}
		})
	}
}

func TestProjectsRenamePreservesDailyHistoryAndSkills(t *testing.T) {
	p := NewProjects(t.TempDir())
	daily, err := p.Daily("old")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	if err := daily.AppendExternal(at, "project history"); err != nil {
		t.Fatal(err)
	}
	if err := p.Save("old", MemFile{Entries: []string{"project uses Go"}}); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(p.root, "old", "skills", "build")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("build guide"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.Rename("old", "new"); err != nil {
		t.Fatal(err)
	}
	moved, _ := p.Daily("new")
	data, err := os.ReadFile(moved.PathFor(at))
	if err != nil || !strings.Contains(string(data), "[ext] project history") {
		t.Fatalf("daily history lost: %s %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(p.root, "new", "skills", "build", "SKILL.md"))
	if err != nil || string(data) != "build guide" {
		t.Fatalf("skill lost: %s %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(p.root, "old")); !os.IsNotExist(err) {
		t.Fatalf("old directory remains: %v", err)
	}
}

func TestProjectDailyUsesProjectMutationLock(t *testing.T) {
	p := NewProjects(t.TempDir())
	daily, err := p.Daily("alpha")
	if err != nil {
		t.Fatal(err)
	}
	release, err := lockRoot(p.root)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	done := make(chan error, 1)
	go func() { done <- daily.AppendExternal(time.Now(), "task history") }()
	select {
	case err := <-done:
		t.Fatalf("daily write bypassed project mutation lock: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("daily write did not resume after project mutation")
	}
}

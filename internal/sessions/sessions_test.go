package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionProjectAssociation(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Create session without project
	s1, err := store.Create("Global Chat")
	if err != nil {
		t.Fatalf("Create s1: %v", err)
	}
	if s1.Project != "" {
		t.Errorf("s1.Project = %q, want empty", s1.Project)
	}

	// Create session with project
	s2, err := store.Create("Project Alpha Chat", "alpha")
	if err != nil {
		t.Fatalf("Create s2: %v", err)
	}
	if s2.Project != "alpha" {
		t.Errorf("s2.Project = %q, want alpha", s2.Project)
	}

	s3, err := store.Create("Project Beta Chat", "beta")
	if err != nil {
		t.Fatalf("Create s3: %v", err)
	}
	if s3.Project != "beta" {
		t.Errorf("s3.Project = %q, want beta", s3.Project)
	}

	_, err = store.Create("Another Alpha Chat", "alpha")
	if err != nil {
		t.Fatalf("Create s4: %v", err)
	}

	// List all
	all, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("List() len = %d, want 4", len(all))
	}

	// ListByProject "alpha"
	alphaList, err := store.ListByProject("alpha")
	if err != nil {
		t.Fatalf("ListByProject alpha: %v", err)
	}
	if len(alphaList) != 2 {
		t.Errorf("ListByProject(alpha) len = %d, want 2", len(alphaList))
	}

	// ListByProject unassigned ("")
	unassigned, err := store.ListByProject("")
	if err != nil {
		t.Fatalf("ListByProject empty: %v", err)
	}
	if len(unassigned) != 1 || unassigned[0].ID != s1.ID {
		t.Errorf("ListByProject() unassigned = %+v, want s1", unassigned)
	}

	// SetProject: move s1 to alpha
	if err := store.SetProject(s1.ID, "alpha"); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	s1Loaded, err := store.Get(s1.ID)
	if err != nil {
		t.Fatalf("Get s1: %v", err)
	}
	if s1Loaded.Project != "alpha" {
		t.Errorf("s1Loaded.Project = %q, want alpha", s1Loaded.Project)
	}

	// SetProject: disassociate s2
	if err := store.SetProject(s2.ID, ""); err != nil {
		t.Fatalf("SetProject empty: %v", err)
	}
	s2Loaded, err := store.Get(s2.ID)
	if err != nil {
		t.Fatalf("Get s2: %v", err)
	}
	if s2Loaded.Project != "" {
		t.Errorf("s2Loaded.Project = %q, want empty", s2Loaded.Project)
	}
}

func TestLegacySessionWithoutProject(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Write a legacy session JSON without project field
	legacyJSON := `{
  "id": "legacy1234567890",
  "title": "Legacy Session",
  "created_at": "2026-08-01T12:00:00Z",
  "updated_at": "2026-08-01T12:00:00Z",
  "turns": [
    {
      "role": "user",
      "text": "hello"
    }
  ]
}`
	if err := os.WriteFile(filepath.Join(dir, "legacy1234567890.json"), []byte(legacyJSON), 0o644); err != nil {
		t.Fatalf("Write legacy file: %v", err)
	}

	sess, err := store.Get("legacy1234567890")
	if err != nil {
		t.Fatalf("Get legacy session: %v", err)
	}
	if sess.Project != "" {
		t.Errorf("sess.Project = %q, want empty string for legacy file", sess.Project)
	}
	if sess.Title != "Legacy Session" {
		t.Errorf("sess.Title = %q, want Legacy Session", sess.Title)
	}

	// Make sure ListByProject("") includes it
	unassigned, err := store.ListByProject("")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(unassigned) != 1 || unassigned[0].ID != "legacy1234567890" {
		t.Fatalf("unassigned = %+v, want legacy session", unassigned)
	}

	// Now associate it with a project
	if err := store.SetProject("legacy1234567890", "migrated-project"); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	migrated, err := store.Get("legacy1234567890")
	if err != nil {
		t.Fatalf("Get migrated: %v", err)
	}
	if migrated.Project != "migrated-project" {
		t.Errorf("migrated.Project = %q, want migrated-project", migrated.Project)
	}
}

func TestRenameProject(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	s1, err := store.Create("Session 1", "old-p")
	if err != nil {
		t.Fatalf("Create s1: %v", err)
	}
	s2, err := store.Create("Session 2", "old-p")
	if err != nil {
		t.Fatalf("Create s2: %v", err)
	}
	s3, err := store.Create("Session 3", "other-p")
	if err != nil {
		t.Fatalf("Create s3: %v", err)
	}

	n, err := store.RenameProject("old-p", "new-p")
	if err != nil {
		t.Fatalf("RenameProject: %v", err)
	}
	if n != 2 {
		t.Errorf("RenameProject count = %d, want 2", n)
	}

	newList, err := store.ListByProject("new-p")
	if err != nil || len(newList) != 2 {
		t.Errorf("ListByProject(new-p) = %d, want 2", len(newList))
	}

	s1Loaded, err := store.Get(s1.ID)
	if err != nil || s1Loaded.Project != "new-p" {
		t.Errorf("s1.Project = %q, want new-p", s1Loaded.Project)
	}
	s2Loaded, err := store.Get(s2.ID)
	if err != nil || s2Loaded.Project != "new-p" {
		t.Errorf("s2.Project = %q, want new-p", s2Loaded.Project)
	}

	oldList, err := store.ListByProject("old-p")
	if err != nil || len(oldList) != 0 {
		t.Errorf("ListByProject(old-p) = %d, want 0", len(oldList))
	}

	s3Loaded, err := store.Get(s3.ID)
	if err != nil || s3Loaded.Project != "other-p" {
		t.Errorf("s3.Project = %q, want other-p", s3Loaded.Project)
	}
}

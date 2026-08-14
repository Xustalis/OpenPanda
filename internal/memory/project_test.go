package memory

import "testing"

func TestValidateName(t *testing.T) {
	valid := []string{"panda", "117club", "my-project", "a.b"}
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

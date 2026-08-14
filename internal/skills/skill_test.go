package skills

import (
	"path/filepath"
	"strings"
	"testing"
)

func sampleSkill() *Skill {
	return &Skill{
		Name:        "deploy-panda",
		Description: "Deploy PANDA binary to Orange Pi and wire systemd",
		Scope:       ScopeProject,
		Project:     "panda",
		Status:      StatusActive,
		UseCount:    3,
		Body:        "## Steps\n1. scp the binary\n2. restart systemd",
	}
}

func TestParseSkillBytesRoundTrip(t *testing.T) {
	sk := sampleSkill()
	data, err := sk.Bytes()
	if err != nil {
		t.Fatalf("bytes: %v", err)
	}
	got, err := ParseSkill(data)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, data)
	}
	if got.Name != sk.Name || got.Description != sk.Description ||
		got.Scope != sk.Scope || got.Project != sk.Project ||
		got.Status != sk.Status || got.UseCount != sk.UseCount {
		t.Errorf("frontmatter mismatch: %+v", got)
	}
	if got.Body != sk.Body {
		t.Errorf("body mismatch: %q", got.Body)
	}
}

func TestParseSkillValidation(t *testing.T) {
	if _, err := ParseSkill([]byte("no frontmatter here")); err == nil {
		t.Errorf("missing delimiter should error")
	}
	if _, err := ParseSkill([]byte("---\nname: x\n---\nbody")); err == nil {
		t.Errorf("missing description should error")
	}
}

func TestScopePaths(t *testing.T) {
	store := NewStore(t.TempDir())
	cases := []struct {
		sk   *Skill
		want string
	}{
		{&Skill{Name: "g", Scope: ScopeGlobal}, "global/g/SKILL.md"},
		{&Skill{Name: "p", Scope: ScopeProject, Project: "x"}, "project/x/p/SKILL.md"},
		{&Skill{Name: "d", Scope: ScopeDevice, Device: "pi"}, "device/pi/d/SKILL.md"},
	}
	for _, c := range cases {
		path, err := store.Path(c.sk)
		if err != nil {
			t.Fatalf("path: %v", err)
		}
		if !strings.HasSuffix(filepath.ToSlash(path), c.want) {
			t.Errorf("path = %q, want suffix %q", path, c.want)
		}
	}
}

func TestStoreSaveLoad(t *testing.T) {
	store := NewStore(t.TempDir())
	sk := sampleSkill()
	if err := store.Save(sk); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := store.Load(ScopeProject, "panda", "deploy-panda")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil || got.Name != "deploy-panda" || got.Body != sk.Body {
		t.Fatalf("load mismatch: %+v", got)
	}
	// A missing skill is nil, not an error.
	missing, err := store.Load(ScopeProject, "panda", "nope")
	if err != nil || missing != nil {
		t.Errorf("missing skill: got %v err=%v, want nil nil", missing, err)
	}
}

func TestStoreIndexAndMatch(t *testing.T) {
	store := NewStore(t.TempDir())
	global := &Skill{Name: "git-workflow", Description: "Commit and PR workflow", Scope: ScopeGlobal, Status: StatusActive}
	proj := &Skill{Name: "deploy-panda", Description: "Deploy to Orange Pi", Scope: ScopeProject, Project: "panda", Status: StatusActive}
	other := &Skill{Name: "deploy-other", Description: "Deploy to other host", Scope: ScopeProject, Project: "other", Status: StatusActive}

	for _, sk := range []*Skill{global, proj, other} {
		if err := store.Save(sk); err != nil {
			t.Fatalf("save %s: %v", sk.Name, err)
		}
	}

	index, err := store.Index()
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if len(index) != 3 {
		t.Fatalf("index has %d entries, want 3", len(index))
	}

	// Project-scoped matching must not leak other projects' skills.
	matched := Match(index, ScopeProject, "panda", "deploy")
	if len(matched) != 1 || matched[0].Name != "deploy-panda" {
		t.Errorf("project match = %v, want only deploy-panda", names(matched))
	}
	// Global skills match regardless of key.
	globalMatched := Match(index, ScopeGlobal, "", "workflow")
	if len(globalMatched) != 1 || globalMatched[0].Name != "git-workflow" {
		t.Errorf("global match = %v", names(globalMatched))
	}
}

func TestMatchWordBoundary(t *testing.T) {
	index := []IndexEntry{{Name: "deploy", Description: "Deploy workflow", Scope: ScopeGlobal, Status: StatusActive}}
	// "dep" is a substring of "deploy" but not a word — must not match.
	if got := Match(index, ScopeGlobal, "", "dep"); len(got) != 0 {
		t.Errorf("substring should not match: %v", names(got))
	}
	if got := Match(index, ScopeGlobal, "", "deploy"); len(got) != 1 {
		t.Errorf("whole word should match: %v", names(got))
	}
}

func TestScopeKeyRejectsTraversal(t *testing.T) {
	store := NewStore(t.TempDir())
	sk := &Skill{Name: "x", Description: "d", Scope: ScopeProject, Project: "../evil"}
	if err := store.Save(sk); err == nil {
		t.Errorf("traversal scope key should error on save")
	}
	if _, err := store.Load(ScopeProject, "../evil", "x"); err == nil {
		t.Errorf("traversal scope key should error on load")
	}
}

func names(entries []IndexEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}

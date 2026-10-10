// SPDX-License-Identifier: AGPL-3.0-or-later

package defense

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNewScopeParsing(t *testing.T) {
	tests := []struct {
		name  string
		spec  string
		empty bool
	}{
		{"blank", "", true},
		{"whitespace", "   ", true},
		{"whole tree dot", ".", true},
		{"whole tree slash", "/", true},
		{"single file", "src/components/Navbar.vue", false},
		{"single dir", "src/components", false},
		{"multi comma", "src/components, src/styles", false},
		{"multi semicolon", "a; b; c", false},
		{"multi newline", "a\nb\nc", false},
		{"leading dot slash", "./src/app", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewScope(tt.spec)
			if s.Empty() != tt.empty {
				t.Fatalf("Empty() = %v, want %v", s.Empty(), tt.empty)
			}
		})
	}
}

func TestScopeContains(t *testing.T) {
	s := NewScope("src/components/Navbar.vue, src/styles")
	tests := []struct {
		path string
		want bool
	}{
		{"src/components/Navbar.vue", true},      // exact file
		{"src/styles/theme.css", true},           // descendant of dir root
		{"src/components/Navbar.vue.bak", false}, // sibling, not descendant
		{"src/components/App.vue", false},        // other file in same dir
		{"src/styles", true},                     // the dir root itself
		{"README.md", false},                     // unrelated
		{"src/other/x", false},                   // near but out
	}
	for _, tt := range tests {
		if got := s.Contains(tt.path); got != tt.want {
			t.Errorf("Contains(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestScopeDrift(t *testing.T) {
	s := NewScope("src/components")
	changed := []string{
		"src/components/Navbar.vue", // in scope
		"src/components/App.vue",    // in scope
		"App.vue",                   // out
		"src/styles/theme.css",      // out
	}
	want := []string{"App.vue", "src/styles/theme.css"}
	if got := s.Drift(changed); !reflect.DeepEqual(got, want) {
		t.Errorf("Drift() = %v, want %v", got, want)
	}
}

func TestScopeEmptyNeverDrifts(t *testing.T) {
	s := NewScope("")
	if got := s.Drift([]string{"anything", "at/all"}); got != nil {
		t.Errorf("empty scope Drift() = %v, want nil", got)
	}
	if !s.Contains("anything") {
		t.Errorf("empty scope should contain everything")
	}
}

// TestScopeDriftUnderReanchorsParentRoot covers the anchor mismatch the entry
// model produces: scope written from the project root ("proj/sub" while
// workDir is ".../proj") names the same tree, and must not drift every change
// inside it.
func TestScopeDriftUnderReanchorsParentRoot(t *testing.T) {
	work := filepath.Join(t.TempDir(), "test_of_OpenPanda")
	if err := os.MkdirAll(filepath.Join(work, "openpanda-intro"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s := NewScope("test_of_OpenPanda/openpanda-intro")
	changed := []string{"openpanda-intro/server.log", "openpanda-intro/start_server.sh"}
	if got := s.DriftUnder(work, changed); len(got) != 0 {
		t.Fatalf("DriftUnder() = %v, want no drift for parent-anchored scope", got)
	}
	got := s.DriftUnder(work, []string{"openpanda-intro/ok.txt", "elsewhere.txt"})
	if !reflect.DeepEqual(got, []string{"elsewhere.txt"}) {
		t.Fatalf("DriftUnder() = %v, want [elsewhere.txt]", got)
	}
}

// TestScopeDriftUnderKeepsCorrectRootStrict pins the re-anchor gate: a suffix
// is only considered when the dropped prefix ends in the workDir's own name,
// so "x/y" never loosens to a coincidentally existing "y".
func TestScopeDriftUnderKeepsCorrectRootStrict(t *testing.T) {
	work := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(filepath.Join(work, "y"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s := NewScope("x/y")
	got := s.DriftUnder(work, []string{"y/file.txt"})
	if !reflect.DeepEqual(got, []string{"y/file.txt"}) {
		t.Fatalf("DriftUnder() = %v, want drift (root x/y must not widen to y)", got)
	}
}

// TestScopeDriftUnderUnresolvableRootStaysStrict: a root that resolves
// nowhere keeps intercepting rather than silently widening.
func TestScopeDriftUnderUnresolvableRootStaysStrict(t *testing.T) {
	work := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s := NewScope("nope/deep")
	got := s.DriftUnder(work, []string{"other/file.txt"})
	if !reflect.DeepEqual(got, []string{"other/file.txt"}) {
		t.Fatalf("DriftUnder() = %v, want drift for unresolvable root", got)
	}
}

// TestScopeDriftUnderDeletedTree: a tree the agent deleted no longer stats,
// but the changed paths still resolve the suffix — an in-scope deletion must
// not be flagged as drift.
func TestScopeDriftUnderDeletedTree(t *testing.T) {
	work := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s := NewScope("proj/sub")
	if got := s.DriftUnder(work, []string{"sub/gone.txt"}); len(got) != 0 {
		t.Fatalf("DriftUnder() = %v, want no drift (deleted in-scope tree)", got)
	}
}

func TestScopeCleanup(t *testing.T) {
	// Roots are cleaned to slash form and trailing slashes dropped, so a
	// directory scope like "src/components/" matches its children.
	s := NewScope("src/components/")
	if !s.Contains("src/components/Navbar.vue") {
		t.Errorf("trailing-slash dir root should match children")
	}
}

// TestScopeProseTolerant covers the natural-language scopes entry models
// emit despite the path-list instruction: the named file must be in scope and
// the prose filler must not become a phantom root that flags every change.
func TestScopeProseTolerant(t *testing.T) {
	tests := []struct {
		name      string
		spec      string
		path      string
		want      bool
		wantEmpty bool
	}{
		{"cjk wrapper around file", "工作目录下的 haiku.txt", "haiku.txt", true, false},
		{"cjk wrapper around dir", "src/ 下的所有文件", "src/api/main.go", true, false},
		{"cjk mixed files", "haiku.txt 和 note.md", "note.md", true, false},
		{"english prose", "the file haiku.txt in the work directory", "haiku.txt", true, false},
		{"english prose drops filler", "the file haiku.txt in the work directory", "file", false, false},
		{"quoted file", `"haiku.txt"`, "haiku.txt", true, false},
		{"pure prose no roots", "所有文件", "anything.txt", true, true},
		{"empty when unsure", "", "anything.txt", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewScope(tt.spec)
			if s.Empty() != tt.wantEmpty {
				t.Fatalf("Empty() = %v, want %v (spec %q)", s.Empty(), tt.wantEmpty, tt.spec)
			}
			if got := s.Contains(tt.path); got != tt.want {
				t.Fatalf("Contains(%q) = %v, want %v (spec %q)", tt.path, got, tt.want, tt.spec)
			}
		})
	}
}

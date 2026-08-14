package defense

import (
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

func TestScopeCleanup(t *testing.T) {
	// Roots are cleaned to slash form and trailing slashes dropped, so a
	// directory scope like "src/components/" matches its children.
	s := NewScope("src/components/")
	if !s.Contains("src/components/Navbar.vue") {
		t.Errorf("trailing-slash dir root should match children")
	}
}

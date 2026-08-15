package defense

import (
	"path/filepath"
	"strings"
)

// Scope is the set of paths a task is allowed to touch (design doc §15.3 check
// ① and §14.2 signal A). Roots are declared relative to the task's working
// directory and are separated by commas, semicolons, or newlines in the
// entry-model spec (e.g. "src/components/Navbar.vue" or "src/components, src/styles").
// An empty scope declares no restriction.
type Scope struct {
	roots []string // cleaned, slash-separated, relative roots
}

// NewScope parses a scope declaration string into allowed roots. Blank input
// yields an empty (unrestricted) scope. A root of "." or "/" is treated as
// "the whole tree" and dropped, since it would not restrict anything.
func NewScope(spec string) *Scope {
	var roots []string
	for _, part := range strings.FieldsFunc(spec, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n'
	}) {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		p = filepath.ToSlash(filepath.Clean(p))
		// "." and "/" declare the whole tree (no restriction); ".." and "../…"
		// escape the working directory, so none of them restrict anything and
		// all are dropped rather than silently widening the scope outward.
		if p == "." || p == "/" || p == ".." || strings.HasPrefix(p, "../") {
			continue
		}
		roots = append(roots, strings.TrimSuffix(p, "/"))
	}
	return &Scope{roots: roots}
}

// Empty reports whether the scope declares no restriction.
func (s *Scope) Empty() bool { return s == nil || len(s.roots) == 0 }

// Contains reports whether relPath (a slash-separated path relative to the
// working directory) is within any declared root. A root matches either the
// exact path or, when it names a directory, every descendant.
func (s *Scope) Contains(relPath string) bool {
	if s.Empty() {
		return true
	}
	p := filepath.ToSlash(filepath.Clean(relPath))
	for _, r := range s.roots {
		if p == r || strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}

// Drift returns the subset of relPaths that fall outside the declared scope.
// An empty scope never drifts.
func (s *Scope) Drift(relPaths []string) []string {
	if s.Empty() {
		return nil
	}
	var out []string
	for _, p := range relPaths {
		if !s.Contains(p) {
			out = append(out, p)
		}
	}
	return out
}

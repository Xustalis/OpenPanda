// SPDX-License-Identifier: AGPL-3.0-or-later

package defense

import (
	"os"
	"path/filepath"
	"regexp"
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
//
// The documented format is comma/semicolon/newline-separated relative paths,
// but entry models routinely wrap the paths in prose ("工作目录下的 haiku.txt",
// "src/ 下的所有文件"). A prose part whose raw text would never match a real
// path must not become a root — that turns every legitimate change into drift
// (a false-positive intercept). So each part is reduced to its path-like
// tokens: non-CJK words that name a path (contain a separator or a file
// extension) or stand alone as a plain root. Parts that yield no path-like
// token contribute nothing.
func NewScope(spec string) *Scope {
	var roots []string
	for _, part := range strings.FieldsFunc(spec, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n'
	}) {
		for _, p := range scopeTokens(part) {
			p = filepath.ToSlash(filepath.Clean(p))
			// "." and "/" declare the whole tree (no restriction); ".." and "../…"
			// escape the working directory, so none of them restrict anything and
			// all are dropped rather than silently widening the scope outward.
			if p == "." || p == "/" || p == ".." || strings.HasPrefix(p, "../") {
				continue
			}
			roots = append(roots, strings.TrimSuffix(p, "/"))
		}
	}
	return &Scope{roots: roots}
}

// scopeTokens reduces one scope part to path-like tokens. A part that is a
// single plain word ("src") is a root as-is. A part with whitespace or CJK
// text is prose: only its tokens that look like paths (contain '/' or carry a
// file extension) survive, so filler words ("the", "file", "工作目录下的")
// cannot become phantom roots.
func scopeTokens(part string) []string {
	p := strings.TrimSpace(part)
	p = strings.Trim(p, `"'`+"`")
	if p == "" {
		return nil
	}
	fields := strings.Fields(p)
	if len(fields) == 1 && !hasCJK(fields[0]) {
		return []string{fields[0]}
	}
	var out []string
	for _, f := range fields {
		f = strings.Trim(f, `"'`+"`")
		if f == "" || hasCJK(f) {
			continue
		}
		if strings.ContainsAny(f, "/\\") || fileExtRE.MatchString(f) {
			out = append(out, f)
		}
	}
	return out
}

// fileExtRE matches a trailing .ext of 1–8 letters/digits, the usual shape of
// a file name ("haiku.txt", "App.tsx", "go.mod").
var fileExtRE = regexp.MustCompile(`\.[A-Za-z0-9]{1,8}$`)

// hasCJK reports whether s contains a CJK rune — a cheap "this is prose, not
// a path" marker for the scopes entry models emit.
func hasCJK(s string) bool {
	for _, r := range s {
		if (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF) ||
			(r >= 0xF900 && r <= 0xFAFF) || (r >= 0x3000 && r <= 0x303F) ||
			(r >= 0xFF00 && r <= 0xFFEF) {
			return true
		}
	}
	return false
}

// Empty reports whether the scope declares no restriction.
func (s *Scope) Empty() bool { return s == nil || len(s.roots) == 0 }

// Roots returns the declared scope roots — the path set peer negotiation
// (whitepaper §5.1) locks against.
func (s *Scope) Roots() []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s.roots))
	copy(out, s.roots)
	return out
}

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

// DriftUnder is Drift with the roots re-anchored to workDir first. The entry
// model declares scope as the user phrases it, which is not always relative to
// the task's working directory: a spec written from the project root can say
// "proj/sub" while workDir is ".../proj", naming the same tree from one level
// up. Compared verbatim, every changed path under that tree misses the root
// and a fully in-scope task drifts — a guaranteed false intercept. Each root
// is rewritten to its longest suffix that resolves under workDir; roots that
// resolve nowhere stay verbatim, so an unresolvable root keeps intercepting
// rather than silently widening.
func (s *Scope) DriftUnder(workDir string, relPaths []string) []string {
	if s.Empty() {
		return nil
	}
	roots := make([]string, 0, len(s.roots))
	for _, r := range s.roots {
		roots = append(roots, reanchorRoot(workDir, r, relPaths))
	}
	var out []string
	for _, p := range relPaths {
		rel := filepath.ToSlash(filepath.Clean(p))
		matched := false
		for _, r := range roots {
			if rel == r || strings.HasPrefix(rel, r+"/") {
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, p)
		}
	}
	return out
}

// reanchorRoot picks the longest suffix of root that resolves under workDir.
// A suffix resolves when it exists on disk, or — for a tree the agent deleted
// or has not created yet — when it names a path-boundary prefix of an
// actually-changed path. The full root wins whenever it resolves, so a
// correctly anchored root is never loosened.
//
// A shortened suffix is only considered when the dropped prefix ends in the
// workDir's own directory name — that is what makes the suffix the same tree
// seen from above ("proj/sub" under workDir ".../proj" is "sub"). Without the
// gate, any suffix that happened to resolve ("x/y" → "y") would silently
// widen the scope.
func reanchorRoot(workDir, root string, relPaths []string) string {
	if workDir == "" {
		return root
	}
	base := filepath.Base(filepath.Clean(workDir))
	segs := strings.Split(root, "/")
	for i := range segs {
		if i > 0 && segs[i-1] != base {
			continue
		}
		cand := strings.Join(segs[i:], "/")
		if cand == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(workDir, filepath.FromSlash(cand))); err == nil {
			return cand
		}
		for _, p := range relPaths {
			rel := filepath.ToSlash(filepath.Clean(p))
			if rel == cand || strings.HasPrefix(rel, cand+"/") {
				return cand
			}
		}
	}
	return root
}

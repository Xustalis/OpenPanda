package skills

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// IndexEntry is a lightweight skill reference for progressive loading (Hermes's
// skill index): it holds only what matching needs, not the full body, so the
// index stays tiny and the full SKILL.md is read only when matched.
type IndexEntry struct {
	Name        string
	Description string
	Scope       Scope
	Key         string // project/device name for scoped skills, else ""
	Status      Status
	UseCount    int
}

// Index scans the skills root and returns lightweight entries, dropping skills
// whose frontmatter fails to parse (a corrupt skill is skipped, not fatal to
// the whole index). It excludes expired skills from matching.
func (s *Store) Index() ([]IndexEntry, error) {
	var out []IndexEntry
	err := filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sk, err := ParseSkill(data)
		if err != nil {
			return nil // skip malformed skill, keep indexing the rest
		}
		out = append(out, IndexEntry{
			Name:        sk.Name,
			Description: sk.Description,
			Scope:       sk.Scope,
			Key:         sk.keyForScope(),
			Status:      sk.Status,
			UseCount:    sk.UseCount,
		})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("skills: index: %w", err)
	}
	return out, nil
}

// Match returns index entries whose name/description contain any query token,
// restricted to the given scope (and key for project/device scopes). Expired
// skills never match. An empty query matches every skill in scope — callers
// pass a real query or filter the result themselves.
func Match(index []IndexEntry, scope Scope, key, query string) []IndexEntry {
	tokens := strings.Fields(strings.ToLower(query))
	var out []IndexEntry
	for _, e := range index {
		if e.Scope != scope {
			continue
		}
		if (scope == ScopeProject || scope == ScopeDevice) && e.Key != key {
			continue
		}
		if e.Status == StatusExpired || e.Status == StatusPending {
			continue // expired is pruned; pending awaits user approval before use
		}
		if len(tokens) == 0 || containsAny(e.Name+" "+e.Description, tokens) {
			out = append(out, e)
		}
	}
	return out
}

// keyForScope returns the scoping key (project/device name) relevant to the
// skill's scope, used by IndexEntry.
func (s *Skill) keyForScope() string {
	switch s.Scope {
	case ScopeProject:
		return s.Project
	case ScopeDevice:
		return s.Device
	default:
		return ""
	}
}

// containsAny reports whether text contains any of the lowercase tokens as a
// whole word — word-boundary match, not substring, so "go" must not match
// "cargo".
func containsAny(text string, tokens []string) bool {
	words := wordSet(text)
	for _, t := range tokens {
		if _, ok := words[t]; ok {
			return true
		}
	}
	return false
}

// wordSet splits text into lowercase word tokens for boundary-accurate
// matching.
func wordSet(s string) map[string]struct{} {
	out := make(map[string]struct{})
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out[strings.ToLower(cur.String())] = struct{}{}
			cur.Reset()
		}
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

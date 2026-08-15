package skills

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/xenith/panda/internal/util"
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

// Match returns index entries whose name/description overlap the query, ranked
// by relevance and restricted to the given scope (and key for project/device
// scopes). Expired and pending skills never match. An empty query matches every
// skill in scope in index order. Scoring is lexical token overlap — whole-word
// for Latin, single ideograph for CJK — enough to rank related skills without a
// semantic index.
func Match(index []IndexEntry, scope Scope, key, query string) []IndexEntry {
	var candidates []IndexEntry
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
		candidates = append(candidates, e)
	}
	if query == "" {
		return candidates
	}
	q := util.Tokenize(query)
	type scored struct {
		entry IndexEntry
		score int
	}
	ranked := make([]scored, 0, len(candidates))
	for _, e := range candidates {
		if s := overlapTokens(q, util.Tokenize(e.Name+" "+e.Description)); s > 0 {
			ranked = append(ranked, scored{e, s})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	out := make([]IndexEntry, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, r.entry)
	}
	return out
}

// overlapTokens counts the query tokens present in the text tokens, skipping
// function words (Latin and CJK) so a match on a common word like "the" or "的"
// alone never drives ranking.
func overlapTokens(query, text map[string]struct{}) int {
	n := 0
	for t := range text {
		if util.IsStopword(t) {
			continue
		}
		if _, ok := query[t]; ok {
			n++
		}
	}
	return n
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

// SPDX-License-Identifier: AGPL-3.0-or-later

package skills

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/Xustalis/OpenPanda/internal/util"
)

// MaxSkillMatches bounds how many skills one agent prompt may inject. Without
// a cap, a long task title can pull in most of the library and starve the
// mandatory intent of its context budget even though the assembly degrades
// individual bodies to index lines.
const MaxSkillMatches = 4

// alwaysBaseScore keeps an always-on procedure ahead of incidental keyword
// matches without making it immune to the prompt budget in withSkills.
const alwaysBaseScore = 1000

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
	Builtin     bool
	Triggers    []string
	Always      bool
	Score       int // filled in by Match/MatchScopes; zero straight from Index
}

// Cache parsed metadata only. Every lookup still stats the directory tree,
// so additions, deletions and ordinary external edits are observed promptly.
// Bodies are loaded separately on demand, and returned slices never alias cache.
type cachedIndexEntry struct {
	size     int64
	modified time.Time
	entry    IndexEntry
}

func cloneIndexEntry(e IndexEntry) IndexEntry {
	e.Triggers = append([]string(nil), e.Triggers...)
	return e
}

// Index scans the skills root and returns lightweight entries, dropping skills
// whose frontmatter fails to parse (a corrupt skill is skipped, not fatal to
// the whole index). It excludes expired skills from matching.
func (s *Store) Index() ([]IndexEntry, error) {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	next := make(map[string]cachedIndexEntry)
	var out []IndexEntry
	err := filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		cached, ok := s.indexCache[path]
		if ok && cached.size == info.Size() && cached.modified.Equal(info.ModTime()) {
			out = append(out, cloneIndexEntry(cached.entry))
			next[path] = cached
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sk, err := ParseSkill(data)
		if err != nil {
			return nil
		}
		triggers, always := sk.Triggers, sk.Always
		builtin := sk.Builtin || IsBuiltinSkill(sk.Name)
		if builtin && len(triggers) == 0 {
			triggers, always = BuiltinTriggers(sk.Name)
		}
		e := IndexEntry{Name: sk.Name, Description: sk.Description, Scope: sk.Scope,
			Key: sk.keyForScope(), Status: sk.Status, UseCount: sk.UseCount, Builtin: builtin,
			Triggers: triggers, Always: always}
		next[path] = cachedIndexEntry{size: info.Size(), modified: info.ModTime(), entry: cloneIndexEntry(e)}
		out = append(out, e)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("skills: index: %w", err)
	}
	s.indexCache = next
	return out, nil
}

// Match returns index entries in one scope whose name/description/triggers
// overlap query, ranked by relevance. Expired and pending skills never match;
// dormant ones are deprioritized. An empty query matches every skill in scope
// in index order.
func Match(index []IndexEntry, scope Scope, key, query string) []IndexEntry {
	return matchRanked(index, func(e IndexEntry) bool {
		if e.Scope != scope {
			return false
		}
		if (scope == ScopeProject || scope == ScopeDevice) && !strings.EqualFold(e.Key, key) {
			return false
		}
		return true
	}, query, "")
}

// MatchScopes matches global, project and device skills in ONE ranked pass.
// Matching each scope separately and concatenating the results (the old
// withSkills behaviour) made every global skill outrank every project or
// device skill regardless of relevance, and left device skills out entirely.
// deviceKeys accepts several names for this node because `panda skill import
// --device` takes a user-chosen label while the core knows only its derived
// runtime node ID; either spelling must reach the same procedure.
func MatchScopes(index []IndexEntry, project string, deviceKeys []string, primary, secondary string) []IndexEntry {
	return matchRanked(index, func(e IndexEntry) bool {
		switch e.Scope {
		case ScopeGlobal:
			return true
		case ScopeProject:
			return project != "" && strings.EqualFold(e.Key, project)
		case ScopeDevice:
			return MatchKey(e.Key, deviceKeys)
		default:
			return false
		}
	}, primary, secondary)
}

// MatchKey reports whether a scoped skill's key is one of the accepted names
// for the current node/project. Empty keys never match: a device skill saved
// without a label must not become visible everywhere. Shared by the execution
// prompt and the ask-engine tools so what is listed is what can be loaded.
func MatchKey(key string, keys []string) bool {
	if key == "" {
		return false
	}
	for _, k := range keys {
		if k != "" && strings.EqualFold(key, k) {
			return true
		}
	}
	return false
}

// matchRanked scores the in-scope, usable entries. The primary query (the
// task title) is weighted twice the secondary one (a bounded slice of the
// intent), so a short specific title still dominates a long generic body.
func matchRanked(index []IndexEntry, allowed func(IndexEntry) bool, primary, secondary string) []IndexEntry {
	var candidates []IndexEntry
	for _, e := range index {
		if e.Status == StatusExpired || e.Status == StatusPending {
			continue // expired is pruned; pending awaits user approval before use
		}
		if !allowed(e) {
			continue
		}
		candidates = append(candidates, e)
	}
	pTokens := util.Tokenize(primary)
	sTokens := util.Tokenize(secondary)
	if len(pTokens) == 0 && len(sTokens) == 0 {
		return candidates // no signal: keep index order, do not invent a ranking
	}
	pRaw := strings.ToLower(primary)
	sRaw := strings.ToLower(secondary)
	type scored struct {
		entry IndexEntry
		score int
		order int
	}
	ranked := make([]scored, 0, len(candidates))
	for i, e := range candidates {
		score := scoreEntry(e, pTokens, pRaw, 2) + scoreEntry(e, sTokens, sRaw, 1)
		if e.Always {
			score += alwaysBaseScore
		}
		if e.Status == StatusDormant && !e.Always && score > 1 {
			score /= 2 // lifecycle §8.5: dormant stays visible but deprioritized
		}
		if score <= 0 {
			continue
		}
		e.Score = score
		ranked = append(ranked, scored{e, score, i})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		if ranked[i].entry.UseCount != ranked[j].entry.UseCount {
			return ranked[i].entry.UseCount > ranked[j].entry.UseCount
		}
		return ranked[i].order < ranked[j].order
	})
	out := make([]IndexEntry, 0, len(ranked))
	for i, r := range ranked {
		if i >= MaxSkillMatches {
			break
		}
		out = append(out, r.entry)
	}
	return out
}

// scoreEntry counts lexical overlap with the query. Explicit triggers are
// worth much more than an incidental word in a description: they are the
// curated, bilingual statement of when a procedure applies.
func scoreEntry(e IndexEntry, qTokens map[string]struct{}, qRaw string, weight int) int {
	if weight <= 0 || len(qTokens) == 0 {
		return 0
	}
	nameTokens := util.Tokenize(e.Name)
	descTokens := util.Tokenize(e.Description)
	score := 0
	for t := range qTokens {
		if util.IsStopword(t) {
			continue
		}
		if _, ok := nameTokens[t]; ok {
			score += 5
		}
		if _, ok := descTokens[t]; ok {
			score++
		}
	}
	score += triggerHits(e.Triggers, qTokens, qRaw)
	return score * weight
}

// triggerHits scores explicit triggers. Latin triggers are matched as whole
// words (so "api" cannot fire on "capital"); Han triggers are matched as
// substrings of the raw query (so "发版" fires on "打开发版清单"). Multi-word
// Latin triggers require all of their words to be present.
func triggerHits(triggers []string, qTokens map[string]struct{}, qRaw string) int {
	hits := 0
	for _, tr := range triggers {
		lt := strings.ToLower(strings.TrimSpace(tr))
		if lt == "" {
			continue
		}
		if containsHan(lt) {
			if strings.Contains(qRaw, lt) {
				hits++
			}
			continue
		}
		trTokens := util.Tokenize(lt)
		if len(trTokens) == 0 {
			continue
		}
		all := true
		for tt := range trTokens {
			if _, ok := qTokens[tt]; !ok {
				all = false
				break
			}
		}
		if all {
			hits++
		}
	}
	return hits * 6
}

// containsHan reports whether s has any CJK ideograph. Shared by the skill
// index and the Hub catalog scorer.
func containsHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
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

// SPDX-License-Identifier: AGPL-3.0-or-later

package artifact

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ignoreRule is one parsed .gitignore line. Only the pack root's .gitignore is
// honored — nested files are a deliberate non-goal: the rules that decide what
// a checkout ships live at its root, and honoring deeper ones would let a
// subdirectory quietly re-include what the root meant to withhold.
type ignoreRule struct {
	neg      bool     // leading '!' — re-include
	dirOnly  bool     // trailing '/' — matches directories only
	anchored bool     // any '/' before the trailing one — relative to root
	segs     []string // pattern segments (anchored) or basename pattern
}

// loadIgnoreRules reads root/.gitignore when present. A missing file is not an
// error — trees without git metadata pack exactly as before.
func loadIgnoreRules(root string) []ignoreRule {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return nil
	}
	return parseIgnoreRules(string(data))
}

func parseIgnoreRules(text string) []ignoreRule {
	var rules []ignoreRule
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, " \r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r := ignoreRule{}
		if strings.HasPrefix(line, "!") {
			r.neg = true
			line = line[1:]
		}
		if strings.HasPrefix(line, `\!`) || strings.HasPrefix(line, `\#`) {
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			r.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		if strings.HasPrefix(line, "/") {
			r.anchored = true
			line = line[1:]
		} else if strings.Contains(line, "/") {
			r.anchored = true // interior slash anchors to the .gitignore's dir
		}
		if line == "" {
			continue
		}
		r.segs = strings.Split(line, "/")
		rules = append(rules, r)
	}
	return rules
}

// ignored reports whether rel (slash-separated, root-relative) is excluded by
// the rules. The last matching rule wins, mirroring git; a '!' re-inclusion
// can only take effect when the entry is actually reached — callers prune
// ignored directories wholesale, so a path inside an ignored dir is never
// visited, same as git.
func ignoredBy(rules []ignoreRule, rel string, isDir bool) bool {
	if len(rules) == 0 {
		return false
	}
	ignored := false
	for _, r := range rules {
		if r.dirOnly && !isDir {
			continue
		}
		if matchIgnoreRule(r, rel) {
			ignored = !r.neg
		}
	}
	return ignored
}

func matchIgnoreRule(r ignoreRule, rel string) bool {
	segs := strings.Split(rel, "/")
	if r.anchored {
		return matchSegs(r.segs, segs)
	}
	// Unanchored: a bare pattern matches the basename at any depth.
	ok, err := path.Match(r.segs[0], segs[len(segs)-1])
	return err == nil && ok
}

// matchSegs matches pattern segments against path segments; "**" as a whole
// segment consumes zero or more path segments.
func matchSegs(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			pat = pat[1:]
			for i := 0; i <= len(segs); i++ {
				if matchSegs(pat, segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], segs[0])
		if err != nil || !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

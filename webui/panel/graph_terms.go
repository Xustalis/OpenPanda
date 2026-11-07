// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Xustalis/OpenPanda/internal/util"
)

// The knowledge layer of the memory graph (graph.go) is a deterministic term
// extraction over the same text surfaces the provenance layer scans — no LLM,
// no embeddings, and the same honesty rule as the rest of the endpoint: a term
// node exists because the literal string recurs in the memory files, and the
// view can never claim a term the files do not contain.
//
// Three literal sources:
//
//   - CJK closed n-grams: repeated Chinese phrases (2..12 runes, >=3
//     occurrences). "Closed" means no longer phrase carries the same count —
//     every occurrence of the short fragment sits inside the longer one —
//     which is what kills word-boundary shrapnel ("护链" inside "巡护链路").
//   - Latin words: 3+ letters, >=2 occurrences, function words excluded.
//   - Quoted names: 「…」『…』“…” (2..12 runes) — the agent explicitly named
//     the thing, so a single occurrence is enough.
const (
	termMinCount  = 3
	termMaxRunes  = 12
	latinMinCount = 2
	nameMaxRunes  = 12
)

// genericTerms are common Chinese phrases that recur in almost any memory text
// and carry no entity signal on their own; they would otherwise crowd out the
// real terms in the capped node set.
var genericTerms = map[string]bool{
	"任务": true, "项目": true, "内容": true, "问题": true, "时间": true,
	"今天": true, "明天": true, "昨天": true, "需要": true, "可以": true,
	"已经": true, "如果": true, "因为": true, "所以": true, "但是": true,
	"而且": true, "或者": true, "然后": true, "现在": true, "一下": true,
	"一个": true, "这个": true, "那个": true, "什么": true, "怎么": true,
	"为什么": true, "进行": true, "完成": true, "使用": true, "支持": true,
	"提供": true, "相关": true, "部分": true, "方式": true, "情况": true,
	"结果": true, "用户": true, "系统": true, "文件": true, "记忆": true,
}

// termHit is one extracted term with its evidence: total occurrences and the
// per-surface breakdown the mention edges are built from.
type termHit struct {
	Term  string
	Count int
	Docs  map[string]int // surface id -> occurrences
	// Samples are a few literal source lines the term appears in — the "why
	// is this node here" answer the detail card shows.
	Samples []string
}

// extractTerms pulls the entity terms out of the memory surfaces. exclude
// carries the lowercased names of things that are already graph nodes (surface
// labels, topic/project/skill names) so the knowledge layer never duplicates
// the provenance layer. Output is deterministic: sorted by count desc, term
// asc, capped at cap.
func extractTerms(docs []graphText, exclude map[string]bool, cap int) []termHit {
	return extractTermsWithCancel(docs, exclude, cap, nil)
}

// extractTermsWithCancel is extractTerms with a cooperative cancellation
// hook. `cancelled` is checked once per document, so a client that walked
// away stops the O(n²) n-gram scan instead of paying for the whole payload.
// A nil result means the caller abandoned the request; it is not an empty
// memory store.
func extractTermsWithCancel(docs []graphText, exclude map[string]bool, cap int, cancelled func() bool) []termHit {
	counts := map[string]int{}
	perDoc := map[string]map[string]int{}
	prevs := map[string]map[rune]bool{}
	nexts := map[string]map[rune]bool{}
	quoted := map[string]bool{}

	note := func(term, docID string) {
		counts[term]++
		m := perDoc[term]
		if m == nil {
			m = map[string]int{}
			perDoc[term] = m
		}
		m[docID]++
	}
	addChar := func(m map[string]map[rune]bool, term string, r rune) {
		set := m[term]
		if set == nil {
			set = map[rune]bool{}
			m[term] = set
		}
		set[r] = true
	}

	for _, d := range docs {
		if cancelled != nil && cancelled() {
			return nil
		}
		for _, run := range hanRuns(d.content) {
			rs := []rune(run)
			for i := 0; i < len(rs); i++ {
				for l := 2; l <= termMaxRunes && i+l <= len(rs); l++ {
					if l == 2 && util.IsCJKFunctionWord(string(rs[i])) && util.IsCJKFunctionWord(string(rs[i+1])) {
						continue // both function characters — no phrase signal
					}
					g := string(rs[i : i+l])
					note(g, d.id)
					// Boundary tracking feeds closedTerm's extension check —
					// a max-length gram can never be extended, so its
					// neighbors are dead bookkeeping.
					if l == termMaxRunes {
						continue
					}
					if i > 0 {
						addChar(prevs, g, rs[i-1])
					}
					if i+l < len(rs) {
						addChar(nexts, g, rs[i+l])
					}
				}
			}
		}
		for _, w := range latinWords(d.content) {
			if len(w) >= 3 && !util.IsStopword(w) {
				note(w, d.id)
			}
		}
		for _, name := range quotedNames(d.content) {
			quoted[name] = true
			note(name, d.id)
		}
	}

	out := make([]termHit, 0, len(counts))
	for term, n := range counts {
		lower := strings.ToLower(term)
		if exclude[lower] || genericTerms[term] {
			continue
		}
		switch {
		case quoted[term]:
			// Explicitly named by the agent — one occurrence is enough.
		case isLatinTerm(term):
			if n < latinMinCount {
				continue
			}
		default:
			if n < termMinCount || !closedTerm(term, n, counts, prevs, nexts) {
				continue
			}
		}
		out = append(out, termHit{Term: term, Count: n, Docs: perDoc[term]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Term < out[j].Term
	})
	if cap > 0 && len(out) > cap {
		out = out[:cap]
	}
	return out
}

// closedTerm reports whether every occurrence of a CJK n-gram sits inside a
// longer n-gram with the same count — a superstring extension with an equal
// count means all occurrences are followed (or preceded) by that character, so
// the shorter fragment is a strict part of a repeated phrase.
func closedTerm(term string, n int, counts map[string]int, prevs, nexts map[string]map[rune]bool) bool {
	for c := range nexts[term] {
		if counts[term+string(c)] == n {
			return false
		}
	}
	for c := range prevs[term] {
		if counts[string(c)+term] == n {
			return false
		}
	}
	return true
}

// hanRuns splits text into maximal runs of Han ideographs.
func hanRuns(s string) []string {
	var runs []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			runs = append(runs, string(cur))
			cur = cur[:0]
		}
	}
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			cur = append(cur, r)
			continue
		}
		flush()
	}
	flush()
	return runs
}

// latinWords splits text into lowercase runs of Latin letters, digits, '_' and
// '-' that start with a letter ("work-report", "utf8", "dog").
func latinWords(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			if unicode.IsLetter(cur[0]) {
				out = append(out, strings.ToLower(string(cur)))
			}
			cur = cur[:0]
		}
	}
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			flush()
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			cur = append(cur, r)
			continue
		}
		flush()
	}
	flush()
	return out
}

// isLatinTerm reports whether a term is a Latin word (as opposed to CJK).
func isLatinTerm(term string) bool {
	first, _ := utf8.DecodeRuneInString(term)
	return first != utf8.RuneError && unicode.IsLetter(first) && !unicode.Is(unicode.Han, first)
}

// quotedRe captures the three quote styles the agent uses in memory text:
// 「…」 『…』 and “…”/"…" (single line, 2..24 runes so an over-long quote is
// rejected before the rune-count check).
var quotedRe = regexp.MustCompile(`[「『“"]([^」』”"\n]{2,24})[」』”"]`)

// quotedNames returns the inner text of quoted names worth a node.
func quotedNames(s string) []string {
	var out []string
	for _, m := range quotedRe.FindAllStringSubmatch(s, -1) {
		name := strings.TrimSpace(m[1])
		if n := len([]rune(name)); n >= 2 && n <= nameMaxRunes {
			out = append(out, name)
		}
	}
	return out
}

// Evidence caps: a node carries at most a few short sample lines — enough to
// answer "where did this term come from" without shipping whole entries.
const (
	evidenceCap      = 3
	evidenceMaxRunes = 96
)

// collectEvidence fills each kept term's Samples with up to evidenceCap
// distinct source lines containing it, scanned oldest-dated first (the
// "daily:YYYY-MM-DD" ids sort chronologically, so early lines are the term's
// first sightings). Runs only over docs the term actually occurs in.
func collectEvidence(terms []termHit, docs []graphText) {
	if len(terms) == 0 {
		return
	}
	byID := make(map[string]string, len(docs))
	for _, d := range docs {
		byID[d.id] = d.content
	}
	for i := range terms {
		t := &terms[i]
		ids := make([]string, 0, len(t.Docs))
		for id := range t.Docs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		tl := strings.ToLower(t.Term)
		seen := map[string]bool{}
		for _, id := range ids {
			for line := range strings.Lines(byID[id]) {
				s := strings.TrimSpace(line)
				if s == "" || seen[s] || !strings.Contains(strings.ToLower(s), tl) {
					continue
				}
				seen[s] = true
				t.Samples = append(t.Samples, trimRunes(s, evidenceMaxRunes))
				if len(t.Samples) >= evidenceCap {
					break
				}
			}
			if len(t.Samples) >= evidenceCap {
				break
			}
		}
	}
}

// trimRunes cuts a sample line to max runes with an ellipsis tail.
func trimRunes(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max-1]) + "…"
}

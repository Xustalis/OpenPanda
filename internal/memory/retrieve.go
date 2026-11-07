// SPDX-License-Identifier: AGPL-3.0-or-later

package memory

import (
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Xustalis/OpenPanda/internal/util"
)

// conversationMemoryK is the maximum number of world-note (MEMORY.md) entries
// injected into the entry model's prompt for a given query. The user profile
// (USER.md) is always injected in full — it is small and always relevant.
const conversationMemoryK = 5

// BM25 tuning constants (Robertson & Zaragoza defaults). Entries carry binary
// term frequencies (tokenize yields a set), so k1 only shapes the length
// normalization term; b controls how much an entry's length dilutes a match.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// Retriever ranks memory entries against a query in memory. Memory is tiny (a
// few KB), so no external index or vector store is needed: split Han runs into
// adjacent character pairs and Latin into words, then score with BM25-lite.
// A single-Han-character query also considers individual characters; longer
// Chinese phrases do not match on accidental single-character coincidences.
// Shared tokens are weighted by inverse document frequency across
// the pool (a token present in every entry carries almost no signal) and
// normalized by entry length (a long entry is not automatically the best one).
type Retriever struct{}

// Rank returns up to k entries most relevant to query, highest first. Entries
// with no content-token overlap are dropped. An empty query returns the first
// k entries unchanged (no relevance signal).
func (Retriever) Rank(query string, entries []string, k int) []string {
	if k <= 0 || len(entries) == 0 {
		return nil
	}
	if query == "" {
		if len(entries) <= k {
			return append([]string(nil), entries...)
		}
		return append([]string(nil), entries[:k]...)
	}
	singleHan := utf8.RuneCountInString(strings.TrimSpace(query)) == 1
	q := retrievalTokens(query, singleHan)
	n := len(entries)

	// One pass over the pool: per-entry token sets plus the document frequency
	// of every content token. df is corpus statistics, so stopwords stay out.
	toks := make([]map[string]struct{}, n)
	df := make(map[string]int)
	totalLen := 0
	for i, e := range entries {
		toks[i] = retrievalTokens(e, singleHan)
		totalLen += len(toks[i])
		for tok := range toks[i] {
			if util.IsStopword(tok) {
				continue
			}
			df[tok]++
		}
	}
	avgLen := 1.0
	if totalLen > 0 {
		avgLen = float64(totalLen) / float64(n)
	}

	type scored struct {
		i     int
		score float64
	}
	ranked := make([]scored, n)
	for i := range entries {
		// tf = 1 for every token (set semantics), so the BM25 term reduces to
		// idf × length norm; k1 folds into the denominator constant.
		dl := float64(len(toks[i]))
		norm := (bm25K1 + 1) / (1 + bm25K1*(1-bm25B+bm25B*dl/avgLen))
		var s float64
		for tok := range toks[i] {
			if util.IsStopword(tok) {
				continue
			}
			if _, ok := q[tok]; !ok {
				continue
			}
			idf := math.Log(1 + (float64(n)-float64(df[tok])+0.5)/(float64(df[tok])+0.5))
			s += idf * norm
		}
		ranked[i] = scored{i, s}
	}
	sort.SliceStable(ranked, func(a, b int) bool { return ranked[a].score > ranked[b].score })
	out := make([]string, 0, k)
	for _, r := range ranked {
		if r.score == 0 {
			break
		}
		out = append(out, entries[r.i])
		if len(out) == k {
			break
		}
	}
	return out
}

// retrievalTokens keeps Han pairs scoped to a contiguous run: it never makes
// a fake phrase by joining characters separated by punctuation or Latin text.
// The single-character fallback applies only when the query itself is short.
func retrievalTokens(s string, includeSingles bool) map[string]struct{} {
	out := make(map[string]struct{})
	var latin strings.Builder
	var han []rune
	flushLatin := func() {
		if latin.Len() > 0 {
			out[strings.ToLower(latin.String())] = struct{}{}
			latin.Reset()
		}
	}
	flushHan := func() {
		if len(han) == 1 {
			out[string(han[0])] = struct{}{}
		} else {
			for i := 1; i < len(han); i++ {
				out[string(han[i-1:i+1])] = struct{}{}
			}
			if includeSingles {
				for _, r := range han {
					out[string(r)] = struct{}{}
				}
			}
		}
		han = han[:0]
	}
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			flushLatin()
			han = append(han, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushHan()
			latin.WriteRune(r)
		default:
			flushHan()
			flushLatin()
		}
	}
	flushHan()
	flushLatin()
	return out
}

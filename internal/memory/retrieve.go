package memory

import (
	"sort"

	"github.com/Xustalis/OpenPanda/internal/util"
)

// conversationMemoryK is the maximum number of world-note (MEMORY.md) entries
// injected into the entry model's prompt for a given query. The user profile
// (USER.md) is always injected in full — it is small and always relevant.
const conversationMemoryK = 5

// Retriever ranks memory entries against a query in memory. Memory is tiny (a
// few KB), so no external index or vector store is needed: tokenize query and
// entry (reusing the package's existing tokenize) and score by token overlap, a
// cheap TF-style relevance measure.
type Retriever struct{}

// Rank returns up to k entries most relevant to query, highest first. Entries
// with no token overlap are dropped. An empty query returns the first k entries
// unchanged (no relevance signal).
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
	type scored struct {
		entry string
		score int
	}
	ranked := make([]scored, len(entries))
	for i, e := range entries {
		ranked[i] = scored{e, overlap(query, e)}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	out := make([]string, 0, k)
	for _, s := range ranked {
		if s.score == 0 {
			break
		}
		out = append(out, s.entry)
		if len(out) == k {
			break
		}
	}
	return out
}

// overlap counts the tokens shared between query and entry (tokenize is defined
// in signals.go for the Dreaming engine's novelty signal), skipping function
// words (Latin and CJK) so a common word alone does not create a false match.
func overlap(query, entry string) int {
	q := tokenize(query)
	n := 0
	for t := range tokenize(entry) {
		if util.IsStopword(t) {
			continue
		}
		if _, ok := q[t]; ok {
			n++
		}
	}
	return n
}

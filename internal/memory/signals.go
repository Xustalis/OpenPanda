package memory

import (
	"strings"
	"time"
	"unicode"

	"github.com/xenith/openpanda/internal/util"
)

// Deep-ranking weights, matching OpenClaw's six signals (design §17.3). The
// order is fixed: novelty, frequency, query diversity, recency,
// consolidation, conceptual richness; the slice sums to 1.0.
var dreamWeights = [6]float64{0.30, 0.24, 0.15, 0.15, 0.10, 0.06}

// recencyWindowDays is how many days back recencySignal decays to zero over.
const recencyWindowDays = 30

// scoreCandidate returns the weighted Deep score in [0,1] from the six raw
// signals, each already normalized to [0,1].
func scoreCandidate(novelty, frequency, queryDiversity, recency, consolidation, conceptual float64) float64 {
	sig := [6]float64{novelty, frequency, queryDiversity, recency, consolidation, conceptual}
	var total float64
	for i := range sig {
		total += dreamWeights[i] * sig[i]
	}
	return total
}

// frequencySignal maps total occurrence count to [0,1]; three or more mentions
// saturate, matching the minRecall gate (a candidate already needs >=3 to be
// eligible, so any eligible candidate scores a full frequency signal).
func frequencySignal(count int) float64 {
	return clamp01(float64(count) / 3)
}

// queryDiversitySignal maps the number of distinct days to [0,1]; three or more
// distinct days saturate, matching the minQueries gate.
func queryDiversitySignal(days int) float64 {
	return clamp01(float64(days) / 3)
}

// recencySignal decays with the days since the candidate was last seen, zero
// after recencyWindowDays.
func recencySignal(lastSeen, now time.Time) float64 {
	age := now.Sub(lastSeen)
	if age < 0 {
		return 1
	}
	return clamp01(1 - float64(age)/float64(recencyWindowDays*24*time.Hour))
}

// consolidationSignal maps the span between first and last sighting to [0,1]; a
// one-week span saturates — a memory that keeps resurfacing over a week is well
// consolidated (OpenClaw's "multi-day recurrence strength").
func consolidationSignal(firstSeen, lastSeen time.Time) float64 {
	span := lastSeen.Sub(firstSeen)
	if span < 0 {
		return 0
	}
	return clamp01(float64(span) / float64(7*24*time.Hour))
}

// noveltySignal measures how much a candidate says that memory does not already
// contain: the fraction of the candidate's words absent from the existing
// MEMORY entries. A brand-new fact scores high (it adds information); a fact
// that merely restates what is already memorized scores low. A cold start
// (empty memory) is neutral — there is nothing to compare against, so novelty
// is maximal rather than zero, otherwise the very first memory could never be
// promoted.
func noveltySignal(candidate string, memory []string) float64 {
	if len(memory) == 0 {
		return 1
	}
	cand := tokenize(candidate)
	if len(cand) == 0 {
		return 0
	}
	mem := tokenize(strings.Join(memory, " "))
	var overlap int
	for w := range cand {
		if _, ok := mem[w]; ok {
			overlap++
		}
	}
	return 1 - float64(overlap)/float64(len(cand))
}

// conceptualSignal maps the density of "concept" tokens — words carrying a
// digit or an uppercase letter (acronyms, identifiers, proper nouns) — to [0,1]
// (OpenClaw's concept-tag density).
func conceptualSignal(candidate string) float64 {
	words := conceptTokens(candidate)
	if len(words) == 0 {
		return 0
	}
	var concepts int
	for _, w := range words {
		if isConceptToken(w) {
			concepts++
		}
	}
	return float64(concepts) / float64(len(words))
}

// conceptTokens splits text into case-preserving word tokens (Latin runs and
// single CJK ideographs), so isConceptToken can detect capitals and digits —
// unlike tokenize, which lowercases for comparison.
func conceptTokens(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if unicode.Is(unicode.Han, r) {
				flush()
				out = append(out, string(r))
			} else {
				cur.WriteRune(r)
			}
		default:
			flush()
		}
	}
	flush()
	return out
}

// isConceptToken reports whether a token looks like a technical concept rather
// than common prose: it contains a digit or an uppercase letter, or it is a CJK
// content ideograph (an ideograph that is not a function word). The function
// word list lives in util so the retrievers share it (util.IsCJKFunctionWord).
func isConceptToken(w string) bool {
	for _, r := range w {
		if unicode.IsDigit(r) || unicode.IsUpper(r) {
			return true
		}
	}
	for _, r := range w {
		if unicode.Is(unicode.Han, r) {
			return !util.IsCJKFunctionWord(w)
		}
	}
	return false
}

// tokenize splits text into lowercase word tokens. Latin runs of letters/digits
// become one token; each CJK ideograph stands alone (so Chinese text compares
// ideograph-by-ideograph). It is a lexical heuristic for the novelty and
// conceptual signals, not a linguistic tokenizer.
func tokenize(s string) map[string]struct{} {
	return util.Tokenize(s)
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

package main

// Typo recovery. A mistyped command is the most common way a CLI session goes
// wrong, and "unknown subcommand" alone leaves the user to re-read `--help` and
// spot the difference themselves. `panda statsu` and `/tsaks` both know what was
// meant; naming it turns a dead end into one keystroke of correction.

import "strings"

// suggest returns the candidate closest to input, or "" when nothing is close
// enough to print. Closeness is edit distance with a length-scaled budget: a
// four-letter word tolerates one typo, a longer one two — beyond that a
// "did you mean" is a guess, and a wrong guess is worse than none.
func suggest(input string, candidates []string) string {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return ""
	}
	budget := 1
	if len(input) > 5 {
		budget = 2
	}
	best, bestDist := "", budget+1
	for _, c := range candidates {
		lc := strings.ToLower(c)
		// A prefix of a real command is a truncation, not a typo ("stat" →
		// "status"): treat it as the strongest possible match so it wins over
		// any edit-distance neighbour.
		if lc != input && strings.HasPrefix(lc, input) {
			if bestDist > 0 || len(c) < len(best) {
				best, bestDist = c, 0
			}
			continue
		}
		if d := editDistance(input, lc); d < bestDist {
			best, bestDist = c, d
		}
	}
	if bestDist > budget {
		return ""
	}
	return best
}

// editDistance is Damerau-Levenshtein restricted to adjacent transpositions —
// "tsaks"/"tasks" is one swap, and a plain Levenshtein would score it 2 and
// lose it. Two rolling rows plus the row before them keep it O(min(n,m)) space.
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) < len(br) { // keep the rows as short as the shorter string
		ar, br = br, ar
	}
	if len(br) == 0 {
		return len(ar)
	}
	prev2 := make([]int, len(br)+1)
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && ar[i-1] == br[j-2] && ar[i-2] == br[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(br)]
}

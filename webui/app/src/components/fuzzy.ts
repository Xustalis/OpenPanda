// Subsequence scoring for the ⌘K palette.
//
// The palette is searched by people who already know the name of where they
// are going, so the ranking only has to get two things right: an exact prefix
// wins, and "mem" must find "Memory" without finding "Reminders" first. That
// is a scored subsequence match — small enough to keep honest, and the reason
// it lives in its own module is that it is the one part of the palette worth
// testing without a DOM.

/** Score how well query matches text. Higher is better; 0 means no match.
 *  Matching is case-insensitive and order-preserving but not contiguous, so
 *  "qu" matches "Queue" and "rmd" matches "Reminders". */
export function score(text: string, query: string): number {
  if (query === '') return 1
  const hay = text.toLowerCase()
  const needle = query.toLowerCase()
  let total = 0
  let at = 0
  let prev = -2
  for (const ch of needle) {
    const hit = hay.indexOf(ch, at)
    if (hit < 0) return 0
    // Contiguous characters are worth far more than scattered ones, and a
    // match at a word start is worth more than one mid-word: both are how a
    // human reads "the thing I typed the beginning of".
    if (hit === prev + 1) total += 8
    else if (hit === 0) total += 6
    else if (isBoundary(hay[hit - 1])) total += 4
    else total += 1
    prev = hit
    at = hit + 1
  }
  // Shorter labels win ties: with "sys" typed, "System" should outrank a
  // hypothetical "System diagnostics report".
  return total * 100 - text.length
}

function isBoundary(ch: string | undefined): boolean {
  return ch === undefined || ch === ' ' || ch === '-' || ch === '/' || ch === ':' || ch === '.'
}

/** Rank items by their best-matching haystack, dropping non-matches. Ties keep
 *  the caller's order, which is the sidebar's order — a blank query therefore
 *  shows the menu exactly as the sidebar lists it. */
export function rank<T>(items: T[], query: string, haystack: (item: T) => string[]): T[] {
  const scored = items.map((item, i) => {
    let best = 0
    for (const hay of haystack(item)) {
      const s = score(hay, query)
      if (s > best) best = s
    }
    return { item, best, i }
  })
  return scored
    .filter((s) => s.best > 0)
    .sort((a, b) => b.best - a.best || a.i - b.i)
    .map((s) => s.item)
}

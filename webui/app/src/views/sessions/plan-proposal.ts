// SPDX-License-Identifier: AGPL-3.0-or-later

// The plan dock's one line, kept apart from the component so the markdown
// flattening is testable without a DOM: `npm test` runs node's runner over
// plain .ts modules, and a .tsx file's JSX cannot be stripped by node.

/** Inline marks flattened, block prefixes dropped: enough to read the opening
 *  line of a proposal as a sentence instead of as source. */
function plainLine(raw: string): string {
  return raw
    .replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/^\s{0,3}>\s?/, '')
    .replace(/^\s*(?:[-*+]|\d+[.)])\s+/, '')
    .replace(/[*_~`]/g, '')
    .replace(/\s+/g, ' ')
    .trim()
}

/** The dock's one line. The dialog is where the proposal is read in full; the
 *  dock only has to say what the plan is about, so headings (they would just
 *  repeat the dock's own title), code, tables and rules are dropped and the
 *  first paragraph that survives is flattened and cut at a word boundary. */
export function proposalSummary(text: string, limit = 140): string {
  const source = text.replace(/\r\n?/g, '\n').replace(/```[\s\S]*?```/g, '\n\n')
  let summary = ''
  for (const block of source.split(/\n{2,}/)) {
    const lines: string[] = []
    for (const raw of block.split('\n')) {
      if (/^\s{0,3}#{1,6}\s/.test(raw)) continue
      if (/^\s*\|/.test(raw)) continue
      if (/^\s{0,3}(?:[-*_])(?:\s*[-*_]){2,}\s*$/.test(raw)) continue
      const line = plainLine(raw)
      if (line) lines.push(line)
    }
    if (lines.length > 0) {
      summary = lines.join(' ')
      break
    }
  }
  summary = summary.replace(/\s+/g, ' ').trim()
  if (summary.length <= limit) return summary
  const cut = summary.slice(0, limit)
  const space = cut.lastIndexOf(' ')
  const head = space > limit * 0.6 ? cut.slice(0, space) : cut
  return head.replace(/[\s,;:，；：、-]+$/, '') + '…'
}

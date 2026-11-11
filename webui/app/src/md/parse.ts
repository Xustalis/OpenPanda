// SPDX-License-Identifier: AGPL-3.0-or-later

// Markdown block parser for the chat transcript.
//
// Why hand-rolled: the console ships one runtime dependency (preact) on
// purpose, and the Go side deliberately carries no CGO and a hand-written
// TUI. A parser that emits a typed block tree also lets the renderer build
// vnodes directly — no `innerHTML`, so there is no sanitizer to get wrong
// and no XSS surface at all.
//
// Scope matches internal/mdtext (the terminal renderer): headings, fenced
// code, lists (nested, ordered and unordered), tables, block quotes,
// horizontal rules, paragraphs, and inline emphasis/code/links. Anything
// unrecognized survives as paragraph text — the rule is that nothing may
// read worse than it would as raw text.

/** One inline span inside a block's text. */
export type Inline =
  | { t: 'text'; s: string }
  | { t: 'code'; s: string }
  | { t: 'bold'; kids: Inline[] }
  | { t: 'italic'; kids: Inline[] }
  | { t: 'strike'; kids: Inline[] }
  | { t: 'link'; href: string; kids: Inline[] }

/** One block-level node. */
export type Block =
  | { t: 'heading'; level: number; kids: Inline[] }
  | { t: 'para'; kids: Inline[] }
  | { t: 'code'; lang: string; text: string }
  | { t: 'list'; ordered: boolean; start: number; items: ListItem[] }
  | { t: 'table'; head: Inline[][]; rows: Inline[][][]; align: Align[] }
  | { t: 'quote'; kids: Block[] }
  | { t: 'rule' }

export type Align = 'left' | 'center' | 'right'

/** A list item holds inline content plus any nested blocks (sub-lists). */
export interface ListItem {
  kids: Inline[]
  children: Block[]
}

const RE_HEADING = /^(#{1,6})\s+(.*)$/
const RE_FENCE = /^\s*(```+|~~~+)\s*([\w+#.-]*)\s*$/
const RE_RULE = /^\s*(?:-{3,}|\*{3,}|_{3,})\s*$/
const RE_UL = /^(\s*)[-*+]\s+(.*)$/
const RE_OL = /^(\s*)(\d{1,9})[.)]\s+(.*)$/
const RE_QUOTE = /^\s*>\s?(.*)$/
const RE_TABLE_ROW = /^\s*\|(.*)\|\s*$/
const RE_TABLE_SEP = /^\s*\|?[\s:|-]*-[\s:|-]*$/

// Bounds-checked accessors. Every loop below already guards its index, but
// `noUncheckedIndexedAccess` is on for a reason — reading past the end as an
// empty string keeps the parser assertion-free, and '' terminates every rule.
const line = (lines: string[], i: number): string => lines[i] ?? ''
const char = (s: string, i: number): string => s[i] ?? ''
const group = (m: RegExpExecArray, n: number): string => m[n] ?? ''

/** Parsed documents, keyed by their source text.
 *
 *  The console re-renders a whole transcript on every change frame while any
 *  task on the node is moving, and every finished reply re-parsed its
 *  Markdown from scratch each time. Parsing is pure, so a result is safe to
 *  hand back; the cache is bounded by both entry count and document size so a
 *  long thread cannot grow it without limit.
 *
 *  Streaming text is not cached: it changes with every delta, so its entries
 *  could never hit and would only evict the history that does. */
const PARSED_MAX_ENTRIES = 128
const PARSED_MAX_CHARS = 32 * 1024
const parsedBlocks = new Map<string, Block[]>()

/** Parse a Markdown document into a block list. `cache: false` parses without
 *  keeping the result — for text that is still changing. */
export function parseBlocks(src: string, cache = true): Block[] {
  if (!cache) return parseLines(src.replace(/\r\n?/g, '\n').split('\n'), 0)
  const hit = parsedBlocks.get(src)
  if (hit) {
    // Refresh recency, so what stays on screen survives a scroll.
    parsedBlocks.delete(src)
    parsedBlocks.set(src, hit)
    return hit
  }
  const blocks = parseLines(src.replace(/\r\n?/g, '\n').split('\n'), 0)
  if (src.length <= PARSED_MAX_CHARS) {
    if (parsedBlocks.size >= PARSED_MAX_ENTRIES) {
      const oldest = parsedBlocks.keys().next().value
      if (oldest !== undefined) parsedBlocks.delete(oldest)
    }
    parsedBlocks.set(src, blocks)
  }
  return blocks
}

/** Deepest block nesting this parser will build.
 *
 *  Both nesting constructs recurse: a block quote re-parses its body one `>`
 *  shorter, and a list re-parses a more-indented run, so depth is bounded only
 *  by the input — a line of ten thousand `>` markers, or one bullet indented a
 *  thousand columns. Either is reachable from a model reply or a pasted file,
 *  and blowing the JS stack there throws a RangeError that takes the whole
 *  message view down instead of rendering one ugly paragraph. Twelve is far
 *  past anything a person writes (a quote inside a list inside a quote is 3). */
const MAX_DEPTH = 12

function parseLines(lines: string[], depth: number): Block[] {
  const out: Block[] = []
  let i = 0

  while (i < lines.length) {
    const cur = line(lines, i)

    // Blank lines only separate blocks.
    if (cur.trim() === '') {
      i++
      continue
    }

    // Fenced code: capture verbatim until the closing fence (or EOF, so a
    // still-streaming block renders as code the moment it opens).
    const fence = RE_FENCE.exec(cur)
    if (fence) {
      const marker = char(group(fence, 1), 0)
      const width = group(fence, 1).length
      const body: string[] = []
      i++
      while (i < lines.length) {
        const close = RE_FENCE.exec(line(lines, i))
        if (
          close &&
          char(group(close, 1), 0) === marker &&
          group(close, 1).length >= width &&
          group(close, 2) === ''
        ) {
          break
        }
        body.push(line(lines, i))
        i++
      }
      if (i < lines.length) i++ // consume the closing fence
      out.push({ t: 'code', lang: group(fence, 2), text: body.join('\n') })
      continue
    }

    // A rule check must precede the list check: `- - -` and `***` would
    // otherwise parse as bullets.
    if (RE_RULE.test(cur)) {
      out.push({ t: 'rule' })
      i++
      continue
    }

    const heading = RE_HEADING.exec(cur)
    if (heading) {
      out.push({
        t: 'heading',
        level: group(heading, 1).length,
        kids: parseInline(group(heading, 2).trim()),
      })
      i++
      continue
    }

    if (RE_QUOTE.test(cur)) {
      const body: string[] = []
      while (i < lines.length) {
        const m = RE_QUOTE.exec(line(lines, i))
        if (!m) {
          // A non-blank, non-marker line continues the quote (lazy
          // continuation, per CommonMark); a blank line ends it.
          if (line(lines, i).trim() === '') break
          body.push(line(lines, i))
          i++
          continue
        }
        body.push(group(m, 1))
        i++
      }
      out.push({ t: 'quote', kids: parseNested(body, depth) })
      continue
    }

    if (RE_UL.test(cur) || RE_OL.test(cur)) {
      const [list, next] = parseList(lines, i, depth)
      out.push(list)
      i = next
      continue
    }

    // A table needs a header row plus a delimiter row; without the
    // delimiter the pipes are just text.
    if (isTableStart(lines, i)) {
      const [table, next] = parseTable(lines, i)
      out.push(table)
      i = next
      continue
    }

    // Paragraph: consume until a blank line or the start of another block.
    // The raw lines are kept as well as the trimmed ones: a reply that turns
    // out to be JSON has to reach the code surface with its indentation
    // intact, and trimming is what would flatten it.
    const para: string[] = []
    const raw: string[] = []
    while (i < lines.length && line(lines, i).trim() !== '' && !startsBlock(lines, i)) {
      raw.push(line(lines, i))
      para.push(line(lines, i).trim())
      i++
    }
    if (para.length === 0) {
      // startsBlock matched on the very first line but no branch above
      // claimed it — keep it as text rather than looping forever.
      raw.push(line(lines, i))
      para.push(line(lines, i).trim())
      i++
    }
    const json = asJSONBlock(raw)
    if (json) {
      out.push(json)
      continue
    }
    out.push({ t: 'para', kids: parseInline(para.join('\n')) })
  }

  return out
}

/** A paragraph that is nothing but a JSON object or array is code, not prose.
 *
 *  Models that cannot reach a native tool_use sometimes answer with the call
 *  envelope as text — {"action":…, "tool":{…}} — and the console used to set
 *  it as a paragraph: quotes in a proportional face, every indent trimmed
 *  away, Chinese prose folded into the middle of a brace. It is still a
 *  failed call, but it is JSON, and a fenced block is the one surface that
 *  tells the truth about it. Narrow on purpose: the paragraph must open with
 *  { or [ and the whole of it must parse — anything else stays prose. */
function asJSONBlock(raw: string[]): Block | null {
  const text = raw.join('\n').trimEnd()
  if (text.length < 2) return null
  const first = text[0]
  const last = text[text.length - 1]
  if (first !== '{' && first !== '[') return null
  if ((first === '{' && last === '}') || (first === '[' && last === ']')) {
    try {
      JSON.parse(text)
    } catch {
      return null
    }
    return { t: 'code', lang: 'json', text }
  }
  // Still arriving. A block whose first line is a bare brace, and that has not
  // closed yet, is the envelope mid-flight — take it as code from the first
  // chunk, or the bubble sets it as a paragraph and re-flows the moment the
  // closing brace lands. A closer anywhere inside means the JSON ended and
  // something followed it; that stays prose, whole.
  const head = raw[0]?.trim() ?? ''
  if (head !== '{' && head !== '[') return null
  if (raw.some((l) => l.trim() === '}' || l.trim() === ']')) return null
  return { t: 'code', lang: 'json', text }
}

function isTableStart(lines: string[], i: number): boolean {
  return (
    RE_TABLE_ROW.test(line(lines, i)) &&
    i + 1 < lines.length &&
    RE_TABLE_SEP.test(line(lines, i + 1))
  )
}

/** Parse a nested body, or — at the nesting ceiling — keep its text as one
 *  paragraph. Flattening beats recursing: the content stays readable and the
 *  stack stays bounded, which is the whole point of the cap. */
function parseNested(lines: string[], depth: number): Block[] {
  if (depth >= MAX_DEPTH) {
    const text = lines.join('\n').trim()
    if (text === '') return []
    return [{ t: 'para', kids: parseInline(text) }]
  }
  return parseLines(lines, depth + 1)
}

/** Whether the line at i opens a new block (ends an open paragraph). */
function startsBlock(lines: string[], i: number): boolean {
  const cur = line(lines, i)
  if (RE_FENCE.test(cur) || RE_RULE.test(cur) || RE_HEADING.test(cur)) return true
  if (RE_QUOTE.test(cur) || RE_UL.test(cur) || RE_OL.test(cur)) return true
  return isTableStart(lines, i)
}

/** Parse one list starting at lines[start]; returns it and the next index.
 *  Deeper indentation becomes a nested list on the enclosing item, so
 *  multi-level plans render as real nested lists. */
function parseList(lines: string[], start: number, depth: number): [Block, number] {
  const first = RE_OL.exec(line(lines, start))
  const ordered = first !== null
  const baseIndent = itemIndent(line(lines, start)) ?? 0
  const items: ListItem[] = []
  let i = start

  while (i < lines.length) {
    const cur = line(lines, i)
    if (cur.trim() === '') {
      // A blank line inside a list is only a separator when a further item
      // follows at this level; otherwise the list is over.
      let j = i + 1
      while (j < lines.length && line(lines, j).trim() === '') j++
      if (j >= lines.length || itemIndent(line(lines, j)) !== baseIndent) break
      i = j
      continue
    }
    const indent = itemIndent(cur)

    if (indent !== null && indent > baseIndent) {
      // Deeper item: nest it under the item we just added — unless we are
      // already at the nesting ceiling, where the text is kept and the extra
      // structure dropped (see MAX_DEPTH).
      if (depth >= MAX_DEPTH) {
        const raw = cur.trim()
        const owner = items[items.length - 1]
        if (owner) owner.kids.push({ t: 'text', s: ' ' + raw })
        else items.push({ kids: [{ t: 'text', s: raw }], children: [] })
        i++
        continue
      }
      const [nested, next] = parseList(lines, i, depth + 1)
      const owner = items[items.length - 1]
      if (owner) owner.children.push(nested)
      else items.push({ kids: [], children: [nested] })
      i = next
      continue
    }
    if (indent === null || indent < baseIndent) break

    const ol = RE_OL.exec(cur)
    const ul = ol ? null : RE_UL.exec(cur)
    const text = ol ? group(ol, 3) : ul ? group(ul, 2) : cur.trim()
    const cont: string[] = [text]
    i++
    // Continuation lines: indented past the marker but not a new item — the
    // rest of a wrapped bullet.
    while (i < lines.length && line(lines, i).trim() !== '' && itemIndent(line(lines, i)) === null) {
      const raw = line(lines, i)
      const lead = raw.length - raw.trimStart().length
      if (lead <= baseIndent && baseIndent > 0) break
      cont.push(raw.trim())
      i++
    }
    items.push({ kids: parseInline(cont.join('\n')), children: [] })
  }

  const startNum = first ? parseInt(group(first, 2), 10) || 1 : 1
  return [{ t: 'list', ordered, start: startNum, items }, i]
}

/** The indent width of a list marker on this line, or null if it is not an
 *  item line. */
function itemIndent(text: string): number | null {
  const ol = RE_OL.exec(text)
  if (ol) return group(ol, 1).length
  const ul = RE_UL.exec(text)
  if (ul) return group(ul, 1).length
  return null
}

function parseTable(lines: string[], start: number): [Block, number] {
  const head = splitRow(line(lines, start)).map(parseInline)
  const align = splitRow(line(lines, start + 1)).map<Align>((cell) => {
    const c = cell.trim()
    if (c.startsWith(':') && c.endsWith(':')) return 'center'
    if (c.endsWith(':')) return 'right'
    return 'left'
  })
  const rows: Inline[][][] = []
  let i = start + 2
  while (i < lines.length && RE_TABLE_ROW.test(line(lines, i))) {
    rows.push(splitRow(line(lines, i)).map(parseInline))
    i++
  }
  return [{ t: 'table', head, rows, align }, i]
}

/** Split a table row into raw cell strings, honouring `\|` escapes. */
function splitRow(text: string): string[] {
  const m = RE_TABLE_ROW.exec(text)
  const inner = m ? group(m, 1) : text
  const cells: string[] = []
  let cur = ''
  for (let i = 0; i < inner.length; i++) {
    if (char(inner, i) === '\\' && char(inner, i + 1) === '|') {
      cur += '|'
      i++
      continue
    }
    if (char(inner, i) === '|') {
      cells.push(cur.trim())
      cur = ''
      continue
    }
    cur += char(inner, i)
  }
  cells.push(cur.trim())
  return cells
}

/** Parse inline spans. Code spans bind tightest (their content is never
 *  re-scanned for emphasis), then links, then the emphasis pairs. */
export function parseInline(src: string): Inline[] {
  const out: Inline[] = []
  let text = ''
  const flush = () => {
    if (text !== '') {
      out.push({ t: 'text', s: text })
      text = ''
    }
  }

  let i = 0
  while (i < src.length) {
    const c = char(src, i)

    // Backslash escape: the next character is literal.
    if (c === '\\' && i + 1 < src.length && /[\\`*_~[\]()#|]/.test(char(src, i + 1))) {
      text += char(src, i + 1)
      i += 2
      continue
    }

    // Code span: match the opening run of backticks with an equal run.
    if (c === '`') {
      let run = 0
      while (char(src, i + run) === '`') run++
      const close = src.indexOf('`'.repeat(run), i + run)
      if (close !== -1 && char(src, close + run) !== '`') {
        flush()
        out.push({ t: 'code', s: src.slice(i + run, close).trim() })
        i = close + run
        continue
      }
    }

    // Link: [label](href). The label may itself hold emphasis.
    if (c === '[') {
      const close = matchBracket(src, i)
      if (close !== -1 && char(src, close + 1) === '(') {
        const end = src.indexOf(')', close + 2)
        if (end !== -1) {
          const href = (src.slice(close + 2, end).trim().split(/\s+/)[0] ?? '').trim()
          if (safeHref(href)) {
            flush()
            out.push({ t: 'link', href, kids: parseInline(src.slice(i + 1, close)) })
            i = end + 1
            continue
          }
        }
      }
    }

    // Emphasis. Longest marker first so ** is never read as two *.
    const marker = emphasisAt(src, i)
    if (marker) {
      const close = src.indexOf(marker.mark, i + marker.mark.length)
      if (close > i + marker.mark.length) {
        flush()
        out.push({ t: marker.t, kids: parseInline(src.slice(i + marker.mark.length, close)) })
        i = close + marker.mark.length
        continue
      }
    }

    text += c
    i++
  }
  flush()
  return out
}

/** The emphasis marker opening at src[i], if any. `_` only opens at a word
 *  boundary so snake_case identifiers stay intact. */
function emphasisAt(
  src: string,
  i: number,
): { mark: string; t: 'bold' | 'italic' | 'strike' } | null {
  if (src.startsWith('~~', i)) return { mark: '~~', t: 'strike' }
  if (src.startsWith('**', i)) return { mark: '**', t: 'bold' }
  if (src.startsWith('__', i) && wordBoundary(src, i)) return { mark: '__', t: 'bold' }
  if (char(src, i) === '*') return { mark: '*', t: 'italic' }
  if (char(src, i) === '_' && wordBoundary(src, i)) return { mark: '_', t: 'italic' }
  return null
}

function wordBoundary(src: string, i: number): boolean {
  return i === 0 || !/\w/.test(char(src, i - 1))
}

/** Find the `]` matching the `[` at open, allowing nested brackets. */
function matchBracket(src: string, open: number): number {
  let depth = 0
  for (let i = open; i < src.length; i++) {
    if (char(src, i) === '\\') {
      i++
      continue
    }
    if (char(src, i) === '[') depth++
    else if (char(src, i) === ']') {
      depth--
      if (depth === 0) return i
    }
  }
  return -1
}

/** Only http(s), mailto and in-app hash/root links become anchors. A model
 *  can emit `javascript:` or `data:`; those render as plain text instead. */
function safeHref(href: string): boolean {
  return /^(https?:\/\/|mailto:|#|\/)/i.test(href)
}

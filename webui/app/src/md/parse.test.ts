// Parser tests for the chat Markdown renderer.
//
// Run with `npm test` (node's built-in test runner strips the types; no test
// framework is added to a console that ships one runtime dependency).
// The renderer itself needs no test of its own: it is a total switch over the
// block union, so `tsc` proves every case is handled, and the parser is where
// all the decisions live.

import assert from 'node:assert/strict'
import { test } from 'node:test'
import { parseBlocks, parseInline, type Block, type Inline } from './parse.ts'

/** Flatten a block's inline tree back to plain text, for terse assertions. */
function text(kids: Inline[]): string {
  return kids
    .map((k) => {
      switch (k.t) {
        case 'text':
        case 'code':
          return k.s
        default:
          return text(k.kids)
      }
    })
    .join('')
}

function kinds(blocks: Block[]): string[] {
  return blocks.map((b) => b.t)
}

test('a bare paragraph stays one block', () => {
  const bs = parseBlocks('hello there')
  assert.deepEqual(kinds(bs), ['para'])
  assert.equal(text((bs[0] as Extract<Block, { t: 'para' }>).kids), 'hello there')
})

test('headings carry their level and drop the marker', () => {
  const bs = parseBlocks('# One\n\n### Three')
  assert.deepEqual(kinds(bs), ['heading', 'heading'])
  const [h1, h3] = bs as Extract<Block, { t: 'heading' }>[]
  assert.equal(h1!.level, 1)
  assert.equal(text(h1!.kids), 'One')
  assert.equal(h3!.level, 3)
})

test('a fence keeps its body verbatim and records the language', () => {
  const bs = parseBlocks('before\n\n```go\nfunc main() {\n\t// **not** bold\n}\n```\n\nafter')
  assert.deepEqual(kinds(bs), ['para', 'code', 'para'])
  const code = bs[1] as Extract<Block, { t: 'code' }>
  assert.equal(code.lang, 'go')
  assert.equal(code.text, 'func main() {\n\t// **not** bold\n}')
})

test('an unclosed fence still renders as code (mid-stream)', () => {
  const bs = parseBlocks('```sh\nmake gate')
  assert.deepEqual(kinds(bs), ['code'])
  assert.equal((bs[0] as Extract<Block, { t: 'code' }>).text, 'make gate')
})

test('a longer fence may contain a shorter one', () => {
  const bs = parseBlocks('````md\n```\ninner\n```\n````')
  assert.deepEqual(kinds(bs), ['code'])
  assert.equal((bs[0] as Extract<Block, { t: 'code' }>).text, '```\ninner\n```')
})

test('a paragraph ends where the next block begins, with no blank line', () => {
  assert.deepEqual(kinds(parseBlocks('text\n- item')), ['para', 'list'])
  assert.deepEqual(kinds(parseBlocks('text\n## head')), ['para', 'heading'])
  assert.deepEqual(kinds(parseBlocks('text\n```\nx\n```')), ['para', 'code'])
})

test('rules win over bullets', () => {
  assert.deepEqual(kinds(parseBlocks('---')), ['rule'])
  assert.deepEqual(kinds(parseBlocks('***')), ['rule'])
  assert.deepEqual(kinds(parseBlocks('- item')), ['list'])
})

test('lists nest by indentation and keep their ordering', () => {
  const bs = parseBlocks('1. first\n2. second\n   - nested\n   - also\n3. third')
  assert.deepEqual(kinds(bs), ['list'])
  const list = bs[0] as Extract<Block, { t: 'list' }>
  assert.equal(list.ordered, true)
  assert.equal(list.start, 1)
  assert.equal(list.items.length, 3)
  assert.equal(text(list.items[0]!.kids), 'first')
  const nested = list.items[1]!.children[0] as Extract<Block, { t: 'list' }>
  assert.equal(nested.t, 'list')
  assert.equal(nested.ordered, false)
  assert.equal(nested.items.length, 2)
  assert.equal(text(nested.items[1]!.kids), 'also')
})

test('an ordered list honours its start number', () => {
  const list = parseBlocks('7. seven\n8. eight')[0] as Extract<Block, { t: 'list' }>
  assert.equal(list.start, 7)
})

test('a wrapped bullet keeps its continuation line', () => {
  const list = parseBlocks('- one line\n  and its wrap\n- two')[0] as Extract<Block, { t: 'list' }>
  assert.equal(list.items.length, 2)
  assert.equal(text(list.items[0]!.kids), 'one line\nand its wrap')
})

test('tables need a delimiter row — otherwise pipes are text', () => {
  const bs = parseBlocks('| a | b |\n| - |:-:|\n| 1 | 2 |')
  assert.deepEqual(kinds(bs), ['table'])
  const tbl = bs[0] as Extract<Block, { t: 'table' }>
  assert.deepEqual(tbl.align, ['left', 'center'])
  assert.deepEqual(tbl.head.map(text), ['a', 'b'])
  assert.deepEqual(tbl.rows.map((r) => r.map(text)), [['1', '2']])

  assert.deepEqual(kinds(parseBlocks('| a | b |\n| 1 | 2 |')), ['para'])
})

test('an escaped pipe stays inside its cell', () => {
  const tbl = parseBlocks('| a | b |\n| - | - |\n| x \\| y | z |')[0] as Extract<Block, { t: 'table' }>
  assert.deepEqual(tbl.rows[0]!.map(text), ['x | y', 'z'])
})

test('quotes strip the marker and parse their contents as blocks', () => {
  const bs = parseBlocks('> ## inside\n> - a\n> - b')
  assert.deepEqual(kinds(bs), ['quote'])
  const q = bs[0] as Extract<Block, { t: 'quote' }>
  assert.deepEqual(kinds(q.kids), ['heading', 'list'])
})

test('inline emphasis nests, and code spans are never re-scanned', () => {
  assert.deepEqual(parseInline('a `**b**` c'), [
    { t: 'text', s: 'a ' },
    { t: 'code', s: '**b**' },
    { t: 'text', s: ' c' },
  ])
  const bold = parseInline('**bold _and_ italic**')[0] as Extract<Inline, { t: 'bold' }>
  assert.equal(bold.t, 'bold')
  assert.equal(text(bold.kids), 'bold and italic')
  assert.equal((parseInline('~~gone~~')[0] as Extract<Inline, { t: 'strike' }>).t, 'strike')
})

test('an underscore inside a word is not emphasis', () => {
  assert.deepEqual(parseInline('snake_case_name'), [{ t: 'text', s: 'snake_case_name' }])
})

test('an unmatched marker stays literal', () => {
  assert.deepEqual(parseInline('2 * 3 * 4'), [
    { t: 'text', s: '2 ' },
    { t: 'italic', kids: [{ t: 'text', s: ' 3 ' }] },
    { t: 'text', s: ' 4' },
  ])
  assert.deepEqual(parseInline('**unclosed'), [{ t: 'text', s: '**unclosed' }])
  assert.deepEqual(parseInline('a ` b'), [{ t: 'text', s: 'a ` b' }])
})

test('a backslash escapes the next marker', () => {
  assert.deepEqual(parseInline('\\*not italic\\*'), [{ t: 'text', s: '*not italic*' }])
  assert.deepEqual(parseInline('\\`not code\\`'), [{ t: 'text', s: '`not code`' }])
  assert.deepEqual(parseInline('\\[not a link](x)'), [{ t: 'text', s: '[not a link](x)' }])
  // A backslash before an ordinary character is not an escape.
  assert.deepEqual(parseInline('C:\\temp'), [{ t: 'text', s: 'C:\\temp' }])
})

test('only safe schemes become links', () => {
  const ok = parseInline('[docs](https://example.com/x)')[0] as Extract<Inline, { t: 'link' }>
  assert.equal(ok.t, 'link')
  assert.equal(ok.href, 'https://example.com/x')
  assert.equal(text(ok.kids), 'docs')

  // A model can emit these; they must degrade to text, never to an anchor.
  for (const bad of ['javascript:alert(1)', 'data:text/html,<script>', 'vbscript:x']) {
    const spans = parseInline(`[click](${bad})`)
    assert.ok(
      spans.every((s) => s.t !== 'link'),
      `${bad} must not become a link`,
    )
  }
})

test('a link label may hold nested brackets and emphasis', () => {
  const link = parseInline('[a **[b]** c](#/queue)')[0] as Extract<Inline, { t: 'link' }>
  assert.equal(link.t, 'link')
  assert.equal(link.href, '#/queue')
  assert.equal(text(link.kids), 'a [b] c')
})

test('CRLF input parses the same as LF', () => {
  assert.deepEqual(kinds(parseBlocks('# h\r\n\r\ntext\r\n')), kinds(parseBlocks('# h\n\ntext\n')))
})

test('an empty document yields no blocks', () => {
  assert.deepEqual(parseBlocks(''), [])
  assert.deepEqual(parseBlocks('\n\n  \n'), [])
})

test('a realistic reply parses into the expected block sequence', () => {
  const reply = [
    '## What I found',
    '',
    'Two problems in `internal/core`:',
    '',
    '1. The lease is renewed **after** the deadline check.',
    '2. Retries share one backoff.',
    '',
    '| file | line |',
    '| --- | ---: |',
    '| core.go | 210 |',
    '',
    '```go',
    'lease.Renew()',
    '```',
    '',
    '> Both are covered by the new test.',
  ].join('\n')
  assert.deepEqual(kinds(parseBlocks(reply)), [
    'heading',
    'para',
    'list',
    'table',
    'code',
    'quote',
  ])
})

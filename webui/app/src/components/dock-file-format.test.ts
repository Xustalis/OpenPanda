// SPDX-License-Identifier: AGPL-3.0-or-later

// The viewer's pure formatting layer: document link rewriting, outline
// extraction and the line colouriser. Run with `node --test`.

import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
  baseName,
  docHeadings,
  extOf,
  highlightLines,
  isDocFile,
  prepareDocText,
  resolveDocPath,
  slugOf,
  sourceLangOf,
  type Token,
} from './dock-file-format.ts'

function textOf(rows: Token[][]): string[] {
  return rows.map((row) => row.map((tok) => tok.text).join(''))
}

function kinds(rows: Token[][]): string[] {
  return rows.flatMap((row) => row.filter((t) => t.kind !== 'plain').map((t) => t.kind))
}

test('extension helpers understand both path flavours', () => {
  assert.equal(extOf('docs/README.MD'), 'md')
  assert.equal(baseName('D:\\work\\notes.md'), 'notes.md')
  assert.equal(baseName('src/lib/parse.ts'), 'parse.ts')
  assert.equal(extOf('Makefile'), '')
  assert.equal(isDocFile('docs/guide.markdown'), true)
  assert.equal(isDocFile('docs/guide.mdx'), false)
})

test('language classification is conservative about unknown text', () => {
  assert.equal(sourceLangOf('a.go').kind, 'code')
  assert.equal(sourceLangOf('a.html').kind, 'markup')
  assert.equal(sourceLangOf('a.css').kind, 'style')
  assert.equal(sourceLangOf('a.json').kind, 'data')
  assert.equal(sourceLangOf('a.md').kind, 'markdown')
  assert.equal(sourceLangOf('a.log').kind, 'plain')
  assert.equal(sourceLangOf('a.py').hashComments, true)
  assert.equal(sourceLangOf('a.css').hashComments, false)
})

test('outline skips headings inside fenced code and strips inline markup', () => {
  const src = ['# Title', '', '```md', '# not a heading', '```', '', '## **Install** [here](docs/install.md)'].join('\n')
  assert.deepEqual(docHeadings(src), [
    { level: 1, text: 'Title' },
    { level: 2, text: 'Install here' },
  ])
  assert.equal(slugOf('Getting Started!'), 'getting-started')
})

test('relative links and images become viewer anchors', () => {
  const src = [
    'See [the guide](docs/guide.md#usage) and ![a chart](assets/chart.png).',
    'External [site](https://example.com), anchor [top](#title), `code [x](y.md)`.',
    '```',
    '[not a link](inside/fence.md)',
    '```',
  ].join('\n')
  const out = prepareDocText(src).split('\n')
  assert.equal(out[0], 'See [the guide](#file:docs%2Fguide.md%23usage) and [a chart](#image:assets%2Fchart.png).')
  assert.match(out[1] ?? '', /\[site\]\(https:\/\/example\.com\)/)
  assert.match(out[1] ?? '', /\[top\]\(#title\)/)
  assert.match(out[1] ?? '', /`code \[x\]\(y\.md\)`/)
  assert.equal(out[3], '[not a link](inside/fence.md)')
})

test('links resolve against the document, with anchors and :line targets', () => {
  const base = 'D:/work/repo/docs/readme.md'
  assert.deepEqual(resolveDocPath(base, 'guide.md'), { path: 'D:/work/repo/docs/guide.md', line: 0 })
  assert.deepEqual(resolveDocPath(base, '../src/main.go:42'), { path: 'D:/work/repo/src/main.go', line: 42 })
  assert.deepEqual(resolveDocPath(base, 'guide.md#usage'), { path: 'D:/work/repo/docs/guide.md', line: 0 })
  assert.deepEqual(resolveDocPath('README.md', 'docs/./a.md'), { path: 'docs/a.md', line: 0 })
  assert.equal(resolveDocPath(base, 'https://example.com/a.md'), null)
  assert.equal(resolveDocPath(base, '#local'), null)
})

test('colouriser keeps every byte of the source', () => {
  const lines = ['package main // hi', 'var s = "return 12/*x*/"', 'func f() {}']
  const rows = highlightLines(lines, sourceLangOf('a.go'))
  assert.deepEqual(textOf(rows), lines)
  assert.ok(kinds(rows).includes('comment'))
  assert.ok(kinds(rows).includes('string'))
  assert.ok(kinds(rows).includes('keyword'))
  // A keyword inside a string is not a keyword.
  assert.equal(rows[1]?.find((t) => t.text.includes('return 12'))?.kind, 'string')
})

test('block comments carry across lines; fences do too', () => {
  const rows = highlightLines(['/* open', 'still open */ var x = 1', 'closed'], sourceLangOf('a.js'))
  assert.deepEqual(textOf(rows), ['/* open', 'still open */ var x = 1', 'closed'])
  assert.equal(rows[0]?.[0]?.kind, 'comment')
  assert.equal(rows[1]?.[0]?.kind, 'comment')
  assert.ok(rows[1]?.some((t) => t.kind === 'keyword'))

  const md = highlightLines(['# Head', '```go', 'func main() {}', '```', '- item'], sourceLangOf('a.md'))
  assert.equal(md[0]?.[0]?.kind, 'marker')
  assert.equal(md[1]?.[0]?.kind, 'marker')
  assert.equal(md[2]?.[0]?.kind, 'string')
  assert.equal(md[3]?.[0]?.kind, 'marker')
  assert.equal(md[4]?.[0]?.kind, 'marker')
})

test('markup tags, attributes and data keys get their own colour', () => {
  const html = highlightLines(['<div class="box" data-id=12>'], sourceLangOf('a.html'))
  assert.ok(html[0]?.some((t) => t.kind === 'tag' && t.text === 'div'))
  assert.ok(html[0]?.some((t) => t.kind === 'attr' && t.text === 'class'))
  const json = highlightLines(['  "name": "panda", "count": 3'], sourceLangOf('a.json'))
  assert.ok(json[0]?.some((t) => t.kind === 'key' && t.text === '"name"'))
  assert.ok(json[0]?.some((t) => t.kind === 'number'))
})

// SPDX-License-Identifier: AGPL-3.0-or-later

import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  atQuery,
  fenceLang,
  fileLabel,
  expandFileRefs,
  exportMarkdown,
  exportFilename,
  isBinaryRef,
  isImageFile,
  splitFileRef,
  withAttachments,
} from './attach.ts'
import { setLocale } from '../i18n/index.ts'

test('atQuery finds the @token under the caret', () => {
  assert.deepEqual(atQuery('explain @main.go', 16), { start: 8, token: 'main.go' })
  assert.deepEqual(atQuery('@src/', 5), { start: 0, token: 'src/' })
  assert.deepEqual(atQuery('a @b c', 4), { start: 2, token: 'b' })
})

test('atQuery returns null outside an @token', () => {
  assert.equal(atQuery('no at here', 10), null)
  assert.equal(atQuery('email a@b.com x', 12), null) // caret past the token
  assert.equal(atQuery('', 0), null)
  assert.equal(atQuery('double @@x', 9), null) // @@ has @ inside token class
})

test('fenceLang maps extensions, unknown → empty', () => {
  assert.equal(fenceLang('a/b/main.go'), 'go')
  assert.equal(fenceLang('x.TSX'), 'typescript')
  assert.equal(fenceLang('notes.md'), 'markdown')
  assert.equal(fenceLang('noext'), '')
})

const reader =
  (files: Record<string, string>) =>
  async (path: string) => {
    if (!(path in files)) throw new Error('nope')
    return { content: files[path] }
  }

test('expandFileRefs appends fenced blocks and reports attachments', async () => {
  const r = reader({ 'a.go': 'package a\n' })
  const { prompt, attached } = await expandFileRefs('check @a.go please', r)
  assert.deepEqual(attached, ['a.go'])
  assert.match(prompt, /^check @a\.go please\n\n`a\.go`:\n```go\npackage a\n```$/)
})

test('expandFileRefs leaves unresolvable tokens untouched', async () => {
  const { prompt, attached } = await expandFileRefs('mail me@x.com about @missing', reader({}))
  assert.equal(prompt, 'mail me@x.com about @missing')
  assert.deepEqual(attached, [])
})

test('expandFileRefs dedupes repeated mentions and strips trailing punctuation', async () => {
  const seen: string[] = []
  const r = async (p: string) => {
    seen.push(p)
    return { content: 'x' }
  }
  const { attached } = await expandFileRefs('@a.txt, then @a.txt.', r)
  assert.deepEqual(attached, ['a.txt'])
  assert.deepEqual(seen, ['a.txt'])
})

test('expandFileRefs marks truncated reads', async () => {
  const r = async () => ({ content: 'body', truncated: true })
  const { prompt } = await expandFileRefs('@big.txt', r)
  assert.match(prompt, /body\n… \(truncated\)\n```/)
})

test('isImageFile trusts the declared type, then the extension', () => {
  assert.equal(isImageFile({ type: 'image/png', name: 'shot' }), true)
  assert.equal(isImageFile({ type: 'IMAGE/PNG', name: 'shot' }), true)
  assert.equal(isImageFile({ type: '', name: 'shot.PNG' }), true)
  assert.equal(isImageFile({ name: 'diagram.jpeg' }), true)
  assert.equal(isImageFile({ type: 'text/plain', name: 'shot.png' }), false)
  assert.equal(isImageFile({ type: '', name: 'main.go' }), false)
  assert.equal(isImageFile({}), false)
})

test('isBinaryRef covers images, archives and media, not source', () => {
  assert.equal(isBinaryRef('.openpanda/uploads/1-shot.png'), true)
  assert.equal(isBinaryRef('dist/app.zip'), true)
  assert.equal(isBinaryRef('clip.mov'), true)
  assert.equal(isBinaryRef('fonts/ui.woff2'), true)
  assert.equal(isBinaryRef('src/main.go'), false)
  assert.equal(isBinaryRef('notes.md'), false)
  assert.equal(isBinaryRef('noext'), false)
  // SVG previews as an image in the tray but is markup underneath, so it is
  // inlined like any other text file — the source is more use than a path.
  assert.equal(isBinaryRef('assets/logo.SVG'), false)
})

test('expandFileRefs leaves a binary ref as a path instead of inlining bytes', async () => {
  // /api/fs/read hands back raw bytes as a string, so a fenced block around a
  // PNG would paste mojibake into the model's context. It still counts as an
  // attachment, so the bubble can show it.
  const r = async () => ({ content: '\u0089PNG\r\n\u001a\n' })
  const { prompt, attached } = await expandFileRefs('look at @shot.png', r)
  assert.deepEqual(attached, ['shot.png'])
  assert.equal(prompt, 'look at @shot.png')
})

test('withAttachments appends a readable block and dedupes', () => {
  assert.equal(withAttachments('hi', []), 'hi')
  assert.equal(withAttachments('hi', ['', '']), 'hi')
  const out = withAttachments('hi', ['a.png', 'a.png', 'b.go'])
  assert.match(out, /^hi\n\nAttached files \(read them from the workspace\):\n- @a\.png\n- @b\.go$/)
  // Tray-only send: no words typed, so the block is the whole prompt.
  assert.match(withAttachments('  ', ['a.png']), /^Attached files/)
})

test('withAttachments output is recognised by expandFileRefs', async () => {
  // The two must agree, or an attached file silently never reaches the model.
  const prompt = withAttachments('summarise this', ['up/1-notes.md'])
  const seen: string[] = []
  const { attached } = await expandFileRefs(prompt, async (p) => {
    seen.push(p)
    return { content: 'x' }
  })
  assert.deepEqual(seen, ['up/1-notes.md'])
  assert.deepEqual(attached, ['up/1-notes.md'])
})

test('exportMarkdown renders roles and thought details', async () => {
  // Role names come from the i18n dictionary — pin English so the export
  // does not depend on the machine locale the test happens to run under.
  await setLocale('en')
  const md = exportMarkdown(
    [
      { role: 'user', text: 'hi' },
      { role: 'assistant', text: 'hello', thought: 'hmm' },
    ],
    { project: 'demo', title: 't' },
  )
  assert.match(md, /# OpenPanda conversation/)
  assert.match(md, /project demo/)
  assert.match(md, /## You\n\nhi/)
  assert.match(md, /## Panda\n\nhello/)
  assert.match(md, /<details><summary>thought<\/summary>\n\nhmm\n<\/details>/)
})

test('exportMarkdown renders a report head instead of the wire prefix', async () => {
  await setLocale('en')
  const md = exportMarkdown(
    [
      { role: 'user', text: 'test the harness' },
      { role: 'assistant', text: '[failed] @codex · 连通性自检\n\nunexpected argument' },
      { role: 'assistant', text: '[done] 没写代理的收尾' },
    ],
    {},
  )
  // The brackets are protocol; a reader gets a bold line naming the state.
  assert.match(md, /\*\*failed\*\* · @codex · 连通性自检\n\nunexpected argument/)
  assert.doesNotMatch(md, /\[failed\]/)
  // No agent leg, only a title: still one readable line.
  assert.match(md, /\*\*done\*\* · 没写代理的收尾/)
  assert.doesNotMatch(md, /\[done\]/)
})

test('exportMarkdown leaves ordinary prose alone', async () => {
  await setLocale('en')
  const md = exportMarkdown([{ role: 'assistant', text: '[注意] 这不是状态\n第二行' }], {})
  assert.match(md, /\[注意\] 这不是状态\n第二行/)
})

test('exportFilename sanitizes and falls back', () => {
  assert.equal(exportFilename('My Chat!'), 'My-Chat.md')
  assert.equal(exportFilename(''), 'chat.md')
  assert.equal(exportFilename(null), 'chat.md')
})

test('splitFileRef keeps the line anchor on the label, not on the @path', () => {
  // The files dock inserts "src/app.tsx:42" when a line is focused. Sending
  // that whole string would make expandFileRefs read a file that cannot exist.
  assert.deepEqual(splitFileRef('src/app.tsx:42'), { path: 'src/app.tsx', label: 'app.tsx:42' })
  assert.deepEqual(splitFileRef('pelican-bike.html'), {
    path: 'pelican-bike.html',
    label: 'pelican-bike.html',
  })
  assert.deepEqual(splitFileRef('a/b/README'), { path: 'a/b/README', label: 'README' })
  // A Windows drive letter is a colon too, but only a trailing :digits is an
  // anchor — the rest has to stay in the path.
  assert.deepEqual(splitFileRef('C:\\work\\main.go:7'), {
    path: 'C:\\work\\main.go',
    label: 'main.go:7',
  })
})

test('exportMarkdown keeps the delivery report rather than only its replay digest', async () => {
  await setLocale('en')
  const delivery = '## Changes\nFixed it.\n\n## Verification\nTests passed.\n\n## Remaining\nDeploy manually.'
  const md = exportMarkdown([{ role: 'assistant', text: '[done] @codex · Repair\nDeploy manually.', delivery }], {})
  assert.match(md, /## Changes/)
  assert.match(md, /Tests passed/)
  assert.equal(md.split('Deploy manually.').length - 1, 1)
  assert.ok(!md.includes('[done]'))
})

test('fileLabel drops the upload stamp and the directory', () => {
  // What the panel stamps on disk: <unix-millis>-<original name>.
  assert.equal(
    fileLabel('.openpanda/uploads/01a1d335/1794544881290-shot.png'),
    'shot.png',
  )
  assert.equal(fileLabel('C:\\data\\uploads\\1794544881290-notes.md'), 'notes.md')
  // A path that was never stamped, and a name that only looks stamped.
  assert.equal(fileLabel('src/main.go'), 'main.go')
  assert.equal(fileLabel('1234-keep.png'), '1234-keep.png')
  assert.equal(fileLabel(''), '')
})

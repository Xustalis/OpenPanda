import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  atQuery,
  fenceLang,
  expandFileRefs,
  exportMarkdown,
  exportFilename,
} from './attach.ts'

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

test('exportMarkdown renders roles and thought details', () => {
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

test('exportFilename sanitizes and falls back', () => {
  assert.equal(exportFilename('My Chat!'), 'My-Chat.md')
  assert.equal(exportFilename(''), 'chat.md')
  assert.equal(exportFilename(null), 'chat.md')
})

// State helpers for the chat composer, tested apart from the component.
//
// Run with `npm test` (node's built-in runner). The bugs these guard against
// are both streaming-target races: an in-flight reply's deltas must land in
// the streaming assistant bubble — never in whatever message happens to sit
// last after a transcript refetch — and the composer's `/` completion must
// only open on a real slash prefix.

import assert from 'node:assert/strict'
import { test } from 'node:test'
import { patchStreaming, slashQuery } from './chatstate.ts'

interface Msg {
  role: string
  text: string
  streaming?: boolean
}

test('patchStreaming targets the streaming assistant bubble, not the last message', () => {
  const msgs: Msg[] = [
    { role: 'user', text: 'hi' },
    { role: 'assistant', text: '', streaming: true },
  ]
  const got = patchStreaming(msgs, (m) => ({ ...m, text: m.text + 'delta' }))
  assert.equal(got[1]!.text, 'delta')
  assert.equal(got[0]!.text, 'hi')
})

test('patchStreaming survives a trailing non-streaming message', () => {
  // The refetch race: a server refresh can append/keep an older message at
  // the tail while the stream still writes to an earlier bubble.
  const msgs: Msg[] = [
    { role: 'user', text: 'q' },
    { role: 'assistant', text: 'partial', streaming: true },
    { role: 'user', text: 'stray' },
  ]
  const got = patchStreaming(msgs, (m) => ({ ...m, text: m.text + '!' }))
  assert.equal(got[1]!.text, 'partial!')
  assert.equal(got[2]!.text, 'stray')
})

test('patchStreaming falls back to the last message when nothing streams', () => {
  const msgs: Msg[] = [
    { role: 'user', text: 'q' },
    { role: 'assistant', text: '' },
  ]
  const got = patchStreaming(msgs, (m) => ({ ...m, text: 'done' }))
  assert.equal(got[1]!.text, 'done')
})

test('patchStreaming on an empty transcript is a no-op', () => {
  assert.deepEqual(patchStreaming([], (m) => m), [])
})

test('slashQuery opens only on a slash-prefixed first token', () => {
  assert.equal(slashQuery('/'), '')
  assert.equal(slashQuery('/he'), 'he')
  assert.equal(slashQuery('hello'), null)
  assert.equal(slashQuery(''), null)
  // Text after the command token is ignored: completion is for the command
  // name, not free text after it.
  assert.equal(slashQuery('/go x'), 'go')
  assert.equal(slashQuery(' /x'), null)
})

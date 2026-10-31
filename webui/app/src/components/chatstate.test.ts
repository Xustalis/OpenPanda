// SPDX-License-Identifier: AGPL-3.0-or-later

// State helpers for the chat composer, tested apart from the component.
//
// Run with `npm test` (node's built-in runner). The bugs these guard against
// are both streaming-target races: an in-flight reply's deltas must land in
// the streaming assistant bubble — never in whatever message happens to sit
// last after a transcript refetch — and the composer's `/` completion must
// only open on a real slash prefix.

import assert from 'node:assert/strict'
import { test } from 'node:test'
import { mergeTailTurn, patchStreaming, slashQuery } from './chatstate.ts'

interface Msg {
  role: 'user' | 'assistant'
  text: string
  kind?: string
  ref?: string
  streaming?: boolean
  /** Display-only fields the transcript never keeps (see mergeTailTurn). */
  result?: unknown
  thought?: string
  k?: string
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

// --- mergeTailTurn: the empty-bubble-after-a-task-dispatch bug --------------

test('mergeTailTurn fills an empty task bubble from its persisted turn', () => {
  // The wire sent kind=task + task_id but no prose; the server wrote the
  // dispatch line and the ref. Without the merge the bubble stays blank and
  // no task card renders.
  const msgs = [
    { role: 'user' as const, text: '写个网页' },
    { role: 'assistant' as const, text: '', kind: undefined, ref: undefined },
  ]
  const turns = [
    { role: 'user' as const, text: '写个网页' },
    { role: 'assistant' as const, text: '已派发任务：写网页（t1）', kind: 'task', ref: 't1' },
  ]
  const got = mergeTailTurn(msgs, turns)
  assert.equal(got[1]!.text, '已派发任务：写网页（t1）')
  assert.equal(got[1]!.kind, 'task')
  assert.equal(got[1]!.ref, 't1')
})

test('mergeTailTurn keeps display-only fields the transcript never stores', () => {
  // Chain-of-thought and the streamed result live only in the pane; a merge
  // must not throw them away the way a wholesale transcript replace would.
  const msgs = [
    { role: 'user' as const, text: 'q' },
    { role: 'assistant' as const, text: 'partial', thought: 'why', result: { ok: true } },
  ]
  const turns = [{ role: 'user' as const, text: 'q' }, { role: 'assistant' as const, text: 'partial' }]
  const got = mergeTailTurn(msgs, turns)
  assert.equal(got[1]!.thought, 'why')
  assert.deepEqual(got[1]!.result, { ok: true })
})

test('mergeTailTurn leaves the pane alone when the turn never persisted', () => {
  // Fewer server turns than bubbles means the bookkeeping write failed: the
  // wire copy is all there is, and merging would paste an older reply in.
  const msgs = [
    { role: 'user' as const, text: 'q' },
    { role: 'assistant' as const, text: 'streamed only' },
  ]
  const turns = [{ role: 'user' as const, text: 'q' }]
  assert.deepEqual(mergeTailTurn(msgs, turns), msgs)
})

test('mergeTailTurn ignores a transcript whose tail is not an answer', () => {
  const msgs = [
    { role: 'user' as const, text: 'q' },
    { role: 'assistant' as const, text: '' },
  ]
  assert.deepEqual(mergeTailTurn(msgs, [{ role: 'user' as const, text: 'q' }]), msgs)
  assert.deepEqual(mergeTailTurn([], []), [])
})

test('mergeTailTurn inserts the extra turns a fanned-out round persisted', () => {
  // The wire sends one result frame for a round that dispatched three tasks;
  // the panel files one dispatch turn per task. Without adopting the surplus
  // the thread shows one card while the task board shows three.
  const msgs = [
    { role: 'user' as const, text: '测一下这几个 agent' },
    { role: 'assistant' as const, text: '', kind: 'task', ref: 't3', result: { ok: true } },
  ]
  const turns = [
    { role: 'user' as const, text: '测一下这几个 agent' },
    { role: 'assistant' as const, text: '已派发任务：a（t1）', kind: 'task', ref: 't1' },
    { role: 'assistant' as const, text: '已派发任务：b（t2）', kind: 'task', ref: 't2' },
    { role: 'assistant' as const, text: '已派发任务：c（t3）', kind: 'task', ref: 't3' },
  ]
  let n = 0
  const got = mergeTailTurn<Msg>(msgs, turns, (turn) => ({ ...turn, k: `extra-${n++}` }) as Msg)
  assert.equal(got.length, 4)
  assert.deepEqual(
    got.slice(1).map((m) => m.ref),
    ['t1', 't2', 't3'],
  )
  // The local tail keeps the streamed result the transcript never stores.
  assert.deepEqual(got[3]!.result, { ok: true })
})

test('mergeTailTurn reconciles only the tail when no adopter is given', () => {
  // Callers that cannot build pane-shaped rows keep the old single-merge
  // behaviour rather than pasting raw transcript rows in.
  const msgs = [
    { role: 'user' as const, text: 'q' },
    { role: 'assistant' as const, text: '', kind: 'task', ref: 't2' },
  ]
  const turns = [
    { role: 'user' as const, text: 'q' },
    { role: 'assistant' as const, text: '已派发任务：a（t1）', kind: 'task', ref: 't1' },
    { role: 'assistant' as const, text: '已派发任务：b（t2）', kind: 'task', ref: 't2' },
  ]
  const got = mergeTailTurn(msgs, turns)
  assert.equal(got.length, 2)
  assert.equal(got[1]!.ref, 't2')
})

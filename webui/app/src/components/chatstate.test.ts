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
import { composerIntent, reconcileTranscript, runningOperation, threadBusy, mergeTailTurn, patchStreaming, slashArgStarted, slashQuery, slashWords, planArgument } from './chatstate.ts'
import { rankWords } from './fuzzy.ts'

interface Msg {
  role: 'user' | 'assistant'
  text: string
  kind?: string
  ref?: string
  streaming?: boolean
  /** Display-only fields the transcript never keeps (see mergeTailTurn). */
  result?: unknown
  thought?: string
  operation_id?: string
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

test('patchStreaming ignores an absent streaming target', () => {
  const msgs: Msg[] = [
    { role: 'user', text: 'q' },
    { role: 'assistant', text: '' },
  ]
  const got = patchStreaming(msgs, (m) => ({ ...m, text: 'done' }))
  assert.equal(got[1]!.text, '')
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

test('planArgument recognizes plan drafts without consuming navigation or ordinary text', () => {
  assert.equal(planArgument('/plan'), '')
  assert.equal(planArgument(' /PLAN  '), '')
  assert.equal(planArgument('/plan build the page\nthen review it'), 'build the page\nthen review it')
  for (const text of ['/plans', '/planet', '/pl', 'explain /plan', '']) {
    assert.equal(planArgument(text), null, text)
  }
})

// --- the `/` menu: a bare command keeps its list ---------------------------

test('the slash menu closes on the argument, never on the command itself', () => {
  // The regression: `/plan` and `/goal` matched their own argument parsers
  // with an empty string, so the list vanished on the last keystroke and the
  // command could not be picked. Only whitespace after the name is prose.
  for (const bare of ['/', '/p', '/plan', '/goal']) {
    assert.equal(slashArgStarted(bare), false, bare)
  }
  for (const typed of ['/plan ', '/plan build the page', '/goal ship it', '/read a.go', '/plan\nbuild']) {
    assert.equal(slashArgStarted(typed), true, typed)
  }
  assert.equal(slashArgStarted('plain text'), false)
})

test('slashWords offers the label, the id verb, and the English keywords', () => {
  const words = slashWords({ id: 'go:workspace', label: '工作空间', alias: 'workspace map topology' })
  assert.ok(words.includes('工作空间'))
  assert.ok(words.includes('workspace'))
  assert.ok(!words.includes('go:workspace'), 'the prefixed id must not be a word')
  // A board's id keeps its colons: one word, so `/pla` cannot reach it.
  assert.deepEqual(slashWords({ id: 'go:control:plans', label: '计划', alias: '' }), ['计划', 'control:plans'])
})

test('a slash query ranks verbs, not destinations that merely contain the letters', () => {
  const items = [
    { id: 'cmd:goal', label: '/goal', alias: '' },
    { id: 'cmd:plan', label: '/plan', alias: '' },
    { id: 'cmd:plans', label: '/plans', alias: '' },
    { id: 'go:control:plans', label: '计划', alias: 'control plans pipeline stages 计划' },
    { id: 'go:workspace', label: '工作空间', alias: 'workspace map topology devices graph network fleet wiring' },
    { id: 'settings:mcp', label: 'MCP 服务器', alias: 'mcp tools servers integrations' },
    { id: 'settings:team', label: '启用任务与协作', alias: 'experimental team tasks collaboration enable disable' },
  ]
  const rank = (q: string) => rankWords(items, q, slashWords).map((c) => c.id)
  // 计划 keeps its row because "plans" is one of its English keywords — the
  // same mechanism that lets `/queue` reach 队列 in any locale. It sorts after
  // the two verbs, which is what the eye reads as the answer.
  assert.deepEqual(rank('pla'), ['cmd:plan', 'cmd:plans', 'go:control:plans'])
  assert.deepEqual(rank('goal'), ['cmd:goal'])
  // The English keywords still work for the destinations worth typing.
  assert.deepEqual(rank('workspace'), ['go:workspace'])
  assert.deepEqual(rank('mcp'), ['settings:mcp'])
  // Nothing that only spells p…l…a inside a keyword sentence.
  assert.deepEqual(rank('plans'), ['cmd:plans', 'go:control:plans'])
})

// --- composerIntent: a picked mode owns the draft --------------------------

test('an armed chip turns the whole draft into that mode, prefixes included', () => {
  // The command token is gone from the editor once the chip is armed, so what
  // is left is the payload — even when it looks like a command of its own.
  assert.deepEqual(composerIntent('goal', 'verify the login flow'), { goalText: 'verify the login flow', planText: null })
  assert.deepEqual(composerIntent('goal', 'pause'), { goalText: 'pause', planText: null })
  assert.deepEqual(composerIntent('goal', '/plan the release'), { goalText: '/plan the release', planText: null })
  assert.deepEqual(composerIntent('plan', 'build the page'), { goalText: null, planText: 'build the page' })
  assert.deepEqual(composerIntent('plan', '/goal x'), { goalText: null, planText: '/goal x' })
})

test('without a chip the hand-typed prefixes still parse, and nothing else does', () => {
  assert.deepEqual(composerIntent(null, '/goal ship it'), { goalText: 'ship it', planText: null })
  assert.deepEqual(composerIntent(null, '/plan ship it'), { goalText: null, planText: 'ship it' })
  assert.deepEqual(composerIntent(null, 'plain question'), { goalText: null, planText: null })
  assert.deepEqual(composerIntent(null, '/plans'), { goalText: null, planText: null })
})

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

test('late frames never overwrite a user message or a different operation', () => {
  const msgs: Msg[] = [
    { role: 'assistant', text: 'other operation', streaming: true, k: 'other' },
    { role: 'user', text: 'original question', k: 'user' },
  ]
  assert.deepEqual(patchStreaming(msgs, m => ({ ...m, text: 'wrong' }), 'lost'), msgs)
  assert.deepEqual(patchStreaming(msgs, m => ({ ...m, text: 'wrong' }), 'user'), msgs)
  assert.deepEqual(patchStreaming([msgs[1]!], m => ({ ...m, text: 'wrong' })), [msgs[1]!])
})

test('identified assistant remains patchable after its closing frame', () => {
  const msgs: Msg[] = [{ role: 'assistant', text: 'partial', streaming: false, k: 'reply' }]
  assert.equal(patchStreaming(msgs, m => ({ ...m, text: 'complete' }), 'reply')[0]!.text, 'complete')
})

test('a finalizer report is not pasted into the dispatch or user row', () => {
  const local = [
    { role: 'user' as const, text: 'question', k: 'q' },
    { role: 'assistant' as const, text: '', kind: 'task', ref: 't1', operation_id: 'r1', thought: 'summary', k: 'reply' },
  ]
  const stored = [
    { role: 'user' as const, text: 'question', k: 'srv0' },
    { role: 'assistant' as const, text: 'dispatched', kind: 'task', ref: 't1', operation_id: 'r1', k: 'srv1' },
    { role: 'assistant' as const, text: '[done] report', kind: 'answer', k: 'srv2' },
  ]
  const got = reconcileTranscript(local, stored)
  assert.equal(got[0]!.text, 'question')
  assert.equal(got[1]!.k, 'reply')
  assert.equal((got[1] as Msg).thought, 'summary')
  assert.equal(got[2]!.text, '[done] report')
  assert.equal(got[2]!.k, 'srv2')
})

test('refresh and thread switch resolve the exact running operation', () => {
  const s = { id: 's1', operation: { id: 'op1', status: 'running' } }
  assert.equal(runningOperation('s1', s), 'op1')
  assert.equal(runningOperation('s2', s), null)
  assert.equal(runningOperation('s1', { ...s, operation: { id: 'op1', status: 'done' } }), null)
  assert.equal(runningOperation('s1', null), null)
})

test('a dispatched job that is still moving keeps the composer running', () => {
  // The turn settles the moment the handoff is made; the employee it
  // dispatched keeps working. Every one of the three inputs has to count, or
  // the composer swaps Stop for Send while there is still something to stop.
  const done = { id: 's1', operation: { id: 'op1', status: 'done' } }
  assert.equal(threadBusy(false, 's1', done, 0), false)
  assert.equal(threadBusy(false, 's1', done, 1), true)
  assert.equal(threadBusy(true, 's1', done, 0), true)
  assert.equal(threadBusy(false, 's1', { id: 's1', operation: { id: 'op2', status: 'running' } }, 0), true)
  // Another thread's live job is not this composer's business.
  assert.equal(threadBusy(false, 's1', { id: 's2', operation: { id: 'op3', status: 'running' } }, 0), false)
})

test('failed persistence keeps the current streamed reply, not unrelated rows', () => {
  const local = [
    { role: 'assistant' as const, text: 'only wire copy', k: 'new', operation_id: 'op' },
    { role: 'assistant' as const, text: 'stale', k: 'old', operation_id: 'old' },
  ]
  assert.deepEqual(reconcileTranscript(local, [], 'op').map(m => m.text), ['only wire copy'])
  assert.deepEqual(reconcileTranscript(local, []).map(m => m.text), [])
})

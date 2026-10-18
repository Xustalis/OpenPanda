// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for the thread rail's pure helpers (see session-list.ts): pinned-first
// ordering, transcript diffing for the SSE sync path, and selection pruning
// against refreshed listings.

import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { Session, SessionTurn } from '../api/client.ts'
import {
  pruneSelection,
  sortSessions,
  threadMark,
  threadMarkKey,
  threadOutcome,
  turnsChanged,
} from './session-list.ts'

function sess(id: string, updated_at: string, pinned = false): Session {
  return { id, title: id, created_at: updated_at, updated_at, turns: [], pinned }
}

test('pinned sessions lead, then newest activity first', () => {
  const list = [
    sess('a', '2024-01-03T00:00:00Z'),
    sess('b', '2024-01-01T00:00:00Z', true),
    sess('c', '2024-01-02T00:00:00Z'),
  ]
  assert.deepEqual(
    sortSessions(list).map((s) => s.id),
    ['b', 'a', 'c'],
  )
})

test('sortSessions does not mutate the input order', () => {
  const list = [sess('a', '2024-01-01T00:00:00Z'), sess('b', '2024-01-02T00:00:00Z')]
  sortSessions(list)
  assert.equal(list[0]?.id, 'a')
})

test('pinned pair still orders by recency', () => {
  const list = [
    sess('a', '2024-01-01T00:00:00Z', true),
    sess('b', '2024-01-03T00:00:00Z', true),
    sess('c', '2024-01-02T00:00:00Z'),
  ]
  assert.deepEqual(
    sortSessions(list).map((s) => s.id),
    ['b', 'a', 'c'],
  )
})

test('turnsChanged stays quiet on title/pin-only moves', () => {
  const turns: SessionTurn[] = [{ role: 'user', text: 'hi' }]
  assert.equal(turnsChanged(turns, [...turns]), false)
  assert.equal(turnsChanged(undefined, undefined), false)
})

test('turnsChanged catches appends and tail edits', () => {
  const base: SessionTurn[] = [{ role: 'user', text: 'hi' }]
  const longer: SessionTurn[] = [...base, { role: 'assistant', text: 'there' }]
  assert.equal(turnsChanged(base, longer), true)
  assert.equal(turnsChanged(longer, base), true)
  assert.equal(turnsChanged(longer, [{ role: 'user', text: 'hi' }, { role: 'assistant', text: 'edit' }]), true)
})

test('pruneSelection drops ids that left the listing', () => {
  const sel = new Set(['a', 'gone', 'b'])
  const next = pruneSelection(sel, [sess('a', '2024-01-01T00:00:00Z'), sess('b', '2024-01-02T00:00:00Z')])
  assert.deepEqual([...next].sort(), ['a', 'b'])
})

test('pruneSelection returns the same set when nothing left', () => {
  const sel = new Set(['a', 'b'])
  const next = pruneSelection(sel, [sess('a', '2024-01-01T00:00:00Z'), sess('b', '2024-01-02T00:00:00Z')])
  assert.equal(next, sel)
})

// ---- threadOutcome / threadMark: the dot on a history row ----------------

function withTurns(id: string, turns: SessionTurn[]): Session {
  return { ...sess(id, '2024-01-01T00:00:00Z'), turns }
}

const failedThread = withTurns('a', [
  { role: 'user', text: '帮我做一个html网页' },
  { role: 'assistant', text: '已派发任务：x（t1）', kind: 'task' },
  { role: 'assistant', text: '[failed] lease expired' },
])

test('threadOutcome reads the finalizer digest for a dead task', () => {
  // The exact shape a lease expiry leaves behind (panel/finalize.go).
  assert.equal(threadOutcome(failedThread), 'failed')
})

test('threadOutcome treats an error turn as failed', () => {
  assert.equal(threadOutcome(withTurns('a', [
    { role: 'user', text: 'hi' },
    { role: 'assistant', text: '⚠ 模型配置无效', kind: 'error', code: 'model_config' },
  ])), 'failed')
})

test('threadOutcome reports a clean finish for a done digest and a plain reply', () => {
  assert.equal(threadOutcome(withTurns('a', [
    { role: 'user', text: 'hi' },
    { role: 'assistant', text: '[done] 任务完成' },
  ])), 'done')
  assert.equal(threadOutcome(withTurns('a', [
    { role: 'user', text: 'hi' },
    { role: 'assistant', text: '在的，有什么可以帮你的吗？' },
  ])), 'done')
})

test('threadOutcome stays quiet on a dispatch, a cancellation and an empty thread', () => {
  // Still queued: the run has not produced an outcome to report yet.
  assert.equal(threadOutcome(withTurns('a', [
    { role: 'user', text: 'hi' },
    { role: 'assistant', text: '已派发任务：x（t1）', kind: 'task' },
  ])), '')
  assert.equal(threadOutcome(withTurns('a', [
    { role: 'user', text: 'hi' },
    { role: 'assistant', text: '[cancelled] 任务已被用户取消' },
  ])), '')
  assert.equal(threadOutcome(withTurns('a', [])), '')
})

test('threadOutcome ignores a failure that a later answer superseded', () => {
  assert.equal(threadOutcome(withTurns('a', [
    { role: 'user', text: 'hi' },
    { role: 'assistant', text: '[failed] lease expired' },
    { role: 'user', text: '再试一次' },
    { role: 'assistant', text: '[done] 好了' },
  ])), 'done')
})

test('threadOutcome does not fire on a trailing user turn', () => {
  assert.equal(threadOutcome(withTurns('a', [
    { role: 'assistant', text: '[failed] lease expired' },
    { role: 'user', text: '还没跑完？' },
  ])), '')
})

test('threadMark shows the outcome until that exact run is read', () => {
  assert.equal(threadMark(failedThread, {}), 'failed')

  const seen = { a: threadMarkKey(failedThread) }
  assert.equal(threadMark(failedThread, seen), '')

  // A newer run — a retry that fails again, or one that finally succeeds —
  // must re-arm the mark even though the thread was read before.
  const retried = withTurns('a', [
    ...(failedThread.turns as SessionTurn[]),
    { role: 'user', text: '再试一次' },
    { role: 'assistant', text: '[failed] spawn failed' },
  ])
  assert.equal(threadMark(retried, seen), 'failed')

  const fixed = withTurns('a', [
    ...(failedThread.turns as SessionTurn[]),
    { role: 'user', text: '再试一次' },
    { role: 'assistant', text: '[done] 好了' },
  ])
  assert.equal(threadMark(fixed, seen), 'done')
})

test('threadMarkKey separates runs of the same length', () => {
  const a = withTurns('a', [
    { role: 'user', text: 'hi' },
    { role: 'assistant', text: '[failed] low' },
  ])
  const b = withTurns('a', [
    { role: 'user', text: 'hi' },
    { role: 'assistant', text: '[failed] bad' },
  ])
  assert.notEqual(threadMarkKey(a), threadMarkKey(b))
})

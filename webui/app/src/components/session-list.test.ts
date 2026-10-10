// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for the thread rail's pure helpers (see session-list.ts): pinned-first
// ordering, transcript diffing for the SSE sync path, and selection pruning
// against refreshed listings.

import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { Session, SessionTurn } from '../api/client.ts'
import { pruneSelection, sortSessions, turnsChanged } from './session-list.ts'

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

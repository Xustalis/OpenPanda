// SPDX-License-Identifier: AGPL-3.0-or-later

import assert from 'node:assert/strict'
import { test } from 'node:test'
import { groupDispatches, isBareDispatch } from './chat-batch.ts'

interface Turn {
  k: string
  role: string
  kind?: string
  ref?: string
  streaming?: boolean
  /** Stands in for the live result payload the view attaches to a turn. */
  result?: unknown
}

function dispatch(k: string, extra: Partial<Turn> = {}): Turn {
  return { k, role: 'assistant', kind: 'task', ref: `t-${k}`, ...extra }
}

test('a fan-out is one group, including the tail that carries the receipt', () => {
  // The bug: the last dispatch turn is the one the live stream wrote its
  // result onto, so `!result` used to split it into a second bubble.
  const msgs: Turn[] = [
    { k: 'u', role: 'user' },
    dispatch('a'),
    dispatch('b'),
    dispatch('c'),
    dispatch('d', { result: { task_id: 't-d' } }),
  ]
  const { batchOf, absorbed } = groupDispatches(msgs)
  assert.deepEqual(
    batchOf.get('a')!.map((m) => m.k),
    ['b', 'c', 'd'],
  )
  assert.deepEqual([...absorbed].sort(), ['b', 'c', 'd'])
  assert.equal(batchOf.has('d'), false)
})

test('a streaming turn stays out of the group it will join when it lands', () => {
  const msgs: Turn[] = [dispatch('a'), dispatch('b', { streaming: true })]
  const { batchOf, absorbed } = groupDispatches(msgs)
  assert.equal(batchOf.size, 0)
  assert.equal(absorbed.size, 0)
})

test('a non-dispatch turn ends the run', () => {
  const msgs: Turn[] = [
    dispatch('a'),
    dispatch('b'),
    { k: 'answer', role: 'assistant', kind: 'answer' },
    dispatch('c'),
  ]
  const { batchOf } = groupDispatches(msgs)
  assert.deepEqual(batchOf.get('a')!.map((m) => m.k), ['b'])
  assert.equal(batchOf.has('c'), false)
})

test('a single dispatch is its own bubble', () => {
  const { batchOf, absorbed } = groupDispatches([{ k: 'u', role: 'user' }, dispatch('a')])
  assert.equal(batchOf.size, 0)
  assert.equal(absorbed.size, 0)
})

test('a plan turn is not a dispatch turn', () => {
  assert.equal(isBareDispatch({ k: 'p', role: 'assistant', kind: 'plan', ref: 'x' }), false)
})

test('a dispatch without a ref is not a receipt', () => {
  assert.equal(isBareDispatch({ k: 'x', role: 'assistant', kind: 'task' }), false)
})

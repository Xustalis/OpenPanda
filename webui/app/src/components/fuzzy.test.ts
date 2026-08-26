// Scoring tests for the palette's search.
//
// Run with `npm test` (node's built-in runner). The palette component itself
// needs no test — it is a list and three key handlers — but the ranking is
// where a wrong answer is invisible until someone types "mem" and lands on
// Reminders.

import assert from 'node:assert/strict'
import { test } from 'node:test'
import { rank, score } from './fuzzy.ts'

test('an empty query matches everything, in the given order', () => {
  assert.deepEqual(rank(['Chat', 'Queue', 'Memory'], '', (s) => [s]), ['Chat', 'Queue', 'Memory'])
})

test('a non-subsequence does not match', () => {
  assert.equal(score('Memory', 'zz'), 0)
  assert.equal(score('Memory', 'omem'), 0) // order matters
  assert.deepEqual(rank(['Chat', 'Queue'], 'xyz', (s) => [s]), [])
})

test('matching is case-insensitive and gap-tolerant', () => {
  assert.ok(score('Queue', 'QU') > 0)
  assert.ok(score('Reminders', 'rmd') > 0)
})

test('a prefix outranks a mid-word hit', () => {
  // "Reminders" does contain m…e, so both match; only the ranking separates
  // them — which is the whole point of scoring rather than filtering.
  const got = rank(['Reminders', 'Memory'], 'me', (s) => [s])
  assert.deepEqual(got, ['Memory', 'Reminders'])
})

test('a word-start hit outranks a scattered one', () => {
  assert.ok(score('Devices & agents', 'ag') > score('Manage', 'ag'))
})

test('the shorter label wins a tie', () => {
  assert.deepEqual(rank(['System diagnostics', 'System'], 'system', (s) => [s]), [
    'System',
    'System diagnostics',
  ])
})

test('any haystack can match, and the best one decides the rank', () => {
  const items = [
    { label: 'Nodes', keys: ['Nodes', 'devices machines'] },
    { label: 'Memory', keys: ['Memory', 'notes'] },
  ]
  const got = rank(items, 'devices', (i) => i.keys).map((i) => i.label)
  assert.deepEqual(got, ['Nodes'])
})

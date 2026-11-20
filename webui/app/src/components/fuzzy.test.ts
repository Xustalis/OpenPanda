// SPDX-License-Identifier: AGPL-3.0-or-later

// Scoring tests for the palette's search.
//
// Run with `npm test` (node's built-in runner). The palette component itself
// needs no test — it is a list and three key handlers — but the ranking is
// where a wrong answer is invisible until someone types "mem" and lands on
// Reminders.

import assert from 'node:assert/strict'
import { test } from 'node:test'
import { rank, rankWords, score } from './fuzzy.ts'

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

// --- rankWords: the composer's `/` menu names a verb ----------------------

test('word-start ranking ignores a scattered hit inside a keyword blob', () => {
  // "pla" is a subsequence of "workspace map topology devices …" and of
  // "mcp tools servers integrations" (p…l…a in order) — the palette's search
  // tolerates that, a command menu must not: /pla is being typed, not searched.
  const items = [
    { id: 'cmd:plan', label: '/plan', alias: '' },
    { id: 'cmd:plans', label: '/plans', alias: '' },
    { id: 'go:workspace', label: '工作空间', alias: 'workspace map topology devices graph' },
    { id: 'settings:mcp', label: 'MCP 服务器', alias: 'mcp tools servers integrations' },
  ]
  const got = rankWords(items, 'pla', (c) => [
    c.label.replace(/^\//, ''),
    c.id.replace(/^[^:]+:/, ''),
    ...c.alias.split(/\s+/),
  ]).map((c) => c.id)
  assert.deepEqual(got, ['cmd:plan', 'cmd:plans'])
})

test('an exact word outranks a longer prefix, a shorter prefix the reverse', () => {
  const items = [
    { id: 'cmd:plans', label: '/plans' },
    { id: 'cmd:plan', label: '/plan' },
  ]
  const words = (c: { label: string }) => [c.label.replace(/^\//, '')]
  assert.deepEqual(rankWords(items, 'plan', words).map((c) => c.id), ['cmd:plan', 'cmd:plans'])
  assert.deepEqual(rankWords(items, 'pla', words).map((c) => c.id), ['cmd:plan', 'cmd:plans'])
  // Past the point of no return the longer word is the only hit.
  assert.deepEqual(rankWords(items, 'plans', words).map((c) => c.id), ['cmd:plans'])
})

test('an empty query keeps the menu in its given order', () => {
  const items = ['/new', '/export', '/plan']
  assert.deepEqual(rankWords(items, '', (s) => [s]), items)
})

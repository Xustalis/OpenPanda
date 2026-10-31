// SPDX-License-Identifier: AGPL-3.0-or-later

import assert from 'node:assert/strict'
import { test } from 'node:test'
import { buildWorkspaceEdges, crossesRect, workspaceLinkLine } from './workspace-links.ts'

interface TestDevice {
  id: string
  local: boolean
  neighbors: string[]
  rtt: number
  transport: string
}

function device(id: string, extra: Partial<TestDevice> = {}): TestDevice {
  return { id, local: false, neighbors: [], rtt: 0, transport: '', ...extra }
}

test('neighbor declarations become one edge and unknown or self links are ignored', () => {
  const edges = buildWorkspaceEdges([
    device('a', { local: true, neighbors: ['b', 'b', 'a', 'missing'] }),
    device('b', { neighbors: ['a'] }),
    device('c'),
  ])
  assert.equal(edges.length, 1)
  assert.equal(edges[0]!.kind, 'mesh')
  assert.deepEqual([edges[0]!.a, edges[0]!.b].sort(), ['a', 'b'])
})

test('local RTT adds one-way visibility and enriches an existing mesh edge', () => {
  const edges = buildWorkspaceEdges([
    device('a', { local: true }),
    device('b', { rtt: 18, transport: 'tailscale' }),
    device('c', { neighbors: ['a'], rtt: 24, transport: 'direct' }),
  ])
  assert.equal(edges.length, 2)
  const measured = edges.find((e) => [e.a, e.b].sort().join('/') === 'a/b')
  assert.deepEqual(measured, {
    a: 'a',
    b: 'b',
    kind: 'measured',
    rtt: 18,
    transport: 'tailscale',
  })
  const mesh = edges.find((e) => [e.a, e.b].sort().join('/') === 'a/c')
  assert.equal(mesh?.kind, 'mesh')
  assert.equal(mesh?.rtt, 24)
  assert.equal(mesh?.transport, 'direct')
})

test('a horizontal connection starts and ends outside the card boundaries', () => {
  const line = workspaceLinkLine(
    { x: 0, y: 0, w: 100, h: 80 },
    { x: 218, y: 0, w: 100, h: 80 },
  )
  assert.deepEqual(line, { x1: 102, y1: 40, x2: 216, y2: 40 })
})

test('a vertical connection follows a card drag to its new position', () => {
  const line = workspaceLinkLine(
    { x: 0, y: 0, w: 100, h: 80 },
    { x: 0, y: 198, w: 100, h: 80 },
  )
  assert.deepEqual(line, { x1: 50, y1: 82, x2: 50, y2: 196 })
})

test('overlapping cards do not draw a backwards line through either card', () => {
  const a = { x: 0, y: 0, w: 100, h: 80 }
  assert.equal(workspaceLinkLine(a, { x: 30, y: 20, w: 100, h: 80 }), null)
  assert.equal(workspaceLinkLine(a, a), null)
})

test('cards closer than two gaps get no line at all', () => {
  const a = { x: 0, y: 0, w: 100, h: 80 }
  // The anchor sits LINK_GAP outside its own card, so any gap under 2×LINK_GAP
  // puts each endpoint inside the other card — the line would run backwards
  // through both corners. 1px apart and 1.28px overlapped both hit that.
  assert.equal(workspaceLinkLine(a, { x: 101, y: 0, w: 100, h: 80 }), null)
  assert.equal(workspaceLinkLine(a, { x: 98.72, y: 0, w: 100, h: 80 }), null)
  // A real corridor (the grid keeps 18px) still draws.
  assert.ok(workspaceLinkLine(a, { x: 118, y: 0, w: 100, h: 80 }))
})

test('a third card in the corridor marks the line instead of hiding it', () => {
  const a = { x: 0, y: 0, w: 100, h: 80 }
  const b = { x: 200, y: 0, w: 100, h: 80 }
  const between = { x: 100, y: 0, w: 100, h: 80 }
  const blocked = workspaceLinkLine(a, b, [a, between, b])
  assert.ok(blocked, 'an occluded edge is still a real edge')
  assert.equal(blocked.blockedBy, between)
  // Clear corridor: drawn and unmarked.
  const aside = { x: 100, y: 200, w: 100, h: 80 }
  const clear = workspaceLinkLine(a, b, [a, aside, b])
  assert.ok(clear)
  assert.equal(clear.blockedBy, undefined)
  // Passing the endpoints themselves must not count as an obstacle.
  assert.deepEqual(workspaceLinkLine(a, b, [a, b]), workspaceLinkLine(a, b))
})

test('property: a drawn line never crosses either endpoint card', () => {
  // Deterministic LCG — a failing case must reproduce from the seed alone.
  let seed = 0x2f6e2b1
  const rnd = () => {
    seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0
    return seed / 0x100000000
  }
  const rect = () => ({
    x: rnd() * 400,
    y: rnd() * 300,
    w: 40 + rnd() * 160,
    h: 30 + rnd() * 120,
  })
  let drawn = 0
  for (let i = 0; i < 20000; i++) {
    const a = rect()
    const b = rect()
    const line = workspaceLinkLine(a, b)
    if (!line) continue
    drawn++
    // Shrunk by half a pixel: the anchor sits exactly on the inflated
    // boundary, so the endpoint itself must not count as a crossing.
    for (const card of [a, b]) {
      const inner = { x: card.x + 0.5, y: card.y + 0.5, w: card.w - 1, h: card.h - 1 }
      assert.ok(
        !crossesRect(line.x1, line.y1, line.x2, line.y2, inner, 0),
        `line ${JSON.stringify(line)} crosses ${JSON.stringify(card)}`,
      )
    }
  }
  assert.ok(drawn > 2000, `property test only exercised ${drawn} drawn lines`)
})

test('a corner hit slides along the edge so the dot clears the card radius', () => {
  const line = workspaceLinkLine(
    { x: 0, y: 0, w: 200, h: 120 },
    { x: 260, y: 140, w: 200, h: 120 },
  )
  assert.ok(line)
  const near = (got: number, want: number) => Math.abs(got - want) < 1e-6
  assert.ok(near(line.x1, 202) && near(line.y1, 94), `start ${line.x1},${line.y1}`)
  assert.ok(near(line.x2, 258) && near(line.y2, 166), `end ${line.x2},${line.y2}`)
})

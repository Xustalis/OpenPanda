// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for the memory graph's pure model/layout module. The component is a
// thin SVG shell — the interesting guarantees (determinism, spacing, honest
// filtering) all live here, where node --test can reach them.

import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { MemoryGraph, MemoryGraphNode } from '../api/client.ts'
import {
  availableKinds,
  captionExtent,
  dominantSurface,
  filterGraph,
  fitText,
  isEntity,
  labelLines,
  layoutGraph,
  nodeExtent,
  nodeRadius,
  recencyAlpha,
  searchGraph,
  seenDates,
} from './graph-layout.ts'

const file = (id: string, kind: string, extra: Partial<MemoryGraphNode> = {}): MemoryGraphNode => ({
  id,
  kind,
  layer: 'file',
  label: id,
  ...extra,
})
const entity = (id: string, extra: Partial<MemoryGraphNode> = {}): MemoryGraphNode => ({
  id: `entity:${id}`,
  kind: 'entity',
  layer: 'entity',
  label: id,
  ...extra,
})

const sample: MemoryGraph = {
  nodes: [
    file('core:MEMORY.md', 'core', { weight: 8 }),
    file('topic:agents', 'topic', { weight: 3, first_seen: '2026-01-02', last_seen: '2026-01-05' }),
    file('daily', 'daily', { weight: 12, first_seen: '2026-01-01', last_seen: '2026-01-06' }),
    file('skill:web', 'skill'),
    entity('巡护链路', { weight: 4, first_seen: '2026-01-02', last_seen: '2026-01-03' }),
    entity('dog', { weight: 2, first_seen: '2026-01-04', last_seen: '2026-01-06' }),
  ],
  edges: [
    { from: 'daily', to: 'core:MEMORY.md', kind: 'promotion', weight: 2 },
    { from: 'daily', to: 'entity:巡护链路', kind: 'mention', weight: 4 },
    { from: 'entity:巡护链路', to: 'entity:dog', kind: 'co', weight: 2 },
    { from: 'topic:agents', to: 'entity:dog', kind: 'mention', weight: 1 },
  ],
}

test('isEntity accepts either the layer flag or the entity kind', () => {
  assert.ok(isEntity(entity('x')))
  assert.ok(isEntity(file('e', 'entity')))
  assert.ok(!isEntity(sample.nodes[0]!))
})

test('layer filtering keeps only the requested side and drops orphan edges', () => {
  const filesOnly = filterGraph(sample, { layer: 'files', hiddenKinds: new Set() })
  assert.equal(filesOnly.nodes.length, 4)
  assert.deepEqual(
    filesOnly.edges.map((e) => e.kind),
    ['promotion'],
  )
  const entsOnly = filterGraph(sample, { layer: 'entities', hiddenKinds: new Set() })
  assert.equal(entsOnly.nodes.length, 2)
  assert.deepEqual(
    entsOnly.edges.map((e) => e.kind),
    ['co'],
  )
})

test('kind hiding and the first-seen timeline compose', () => {
  const noDaily = filterGraph(sample, { layer: 'all', hiddenKinds: new Set(['daily']) })
  assert.ok(noDaily.nodes.every((n) => n.kind !== 'daily'))
  const early = filterGraph(sample, { layer: 'all', hiddenKinds: new Set(), until: '2026-01-02' })
  // dog first appears 2026-01-04 — hidden; undated files stay visible.
  assert.ok(early.nodes.every((n) => n.id !== 'entity:dog'))
  assert.ok(early.nodes.some((n) => n.id === 'core:MEMORY.md'))
  assert.ok(early.nodes.some((n) => n.id === 'entity:巡护链路'))
})

test('seenDates returns the distinct first_seen days ascending', () => {
  assert.deepEqual(seenDates(sample), ['2026-01-01', '2026-01-02', '2026-01-04'])
  assert.deepEqual(seenDates({ nodes: [file('a', 'core')], edges: [] }), [])
})

test('nodeRadius grows with evidence and saturates', () => {
  assert.ok(nodeRadius(entity('x', { weight: 9 })) > nodeRadius(entity('y', { weight: 1 })))
  assert.ok(nodeRadius(entity('z', { weight: 10000 })) <= 13)
  assert.ok(nodeRadius(file('c', 'core', { weight: 10000 })) <= 19)
})

test('recencyAlpha fades old surfaces to a floor', () => {
  const now = Date.parse('2026-02-01')
  assert.equal(recencyAlpha(undefined, now), 1)
  assert.equal(recencyAlpha('not-a-date', now), 1)
  assert.equal(recencyAlpha('2026-01-30', now), 1)
  assert.equal(recencyAlpha('2025-01-01', now), 0.6)
  const mid = recencyAlpha('2025-08-01', now)
  assert.ok(mid > 0.6 && mid < 1)
})

test('labelLines wraps long labels into at most two lines', () => {
  assert.deepEqual(labelLines('dog'), ['dog'])
  const lines = labelLines('导航模拟测试代码对照巡护链路全景')
  assert.equal(lines.length, 2)
  assert.ok(lines[1]!.endsWith('…'))
})

test('fitText stays on one line and reserves room for the ellipsis', () => {
  assert.equal(fitText('dog'), 'dog')
  const s = fitText('导航模拟测试代码对照巡护链路全景')
  assert.ok(s.endsWith('…'))
  assert.ok([...s].length <= 11)
})

test('nodeExtent sizes entity pills by their label text', () => {
  const wide = nodeExtent(entity('导航模拟测试代码对照'))
  const narrow = nodeExtent(entity('dog'))
  assert.ok(wide > narrow)
  assert.equal(nodeExtent(file('c', 'core', { weight: 4 })), nodeRadius(file('c', 'core', { weight: 4 })))
})

test('layout is deterministic — same input, same coordinates', () => {
  const a = layoutGraph(sample, 860, 560)
  const b = layoutGraph(sample, 860, 560)
  for (const [id, p] of a.pos) {
    const q = b.pos.get(id)!
    assert.equal(p.x, q.x)
    assert.equal(p.y, q.y)
  }
  assert.deepEqual(a.clusters, b.clusters)
})

test('every node lands inside the canvas and pairs do not overlap', () => {
  const { pos } = layoutGraph(sample, 860, 560)
  const pts = [...pos.values()]
  for (const p of pts) {
    assert.ok(p.x >= 0 && p.x <= 860, `${p.id} x=${p.x}`)
    assert.ok(p.y >= 0 && p.y <= 560, `${p.id} y=${p.y}`)
  }
  for (let i = 0; i < pts.length; i++) {
    for (let j = i + 1; j < pts.length; j++) {
      const a = pts[i]!
      const b = pts[j]!
      const d = Math.hypot(a.x - b.x, a.y - b.y)
      const min = (nodeExtent(a) + nodeExtent(b)) * 0.8
      assert.ok(d >= min, `${a.id} vs ${b.id}: ${d} < ${min}`)
    }
  }
})

test('in the mixed view entities ring the outside of the file disc', () => {
  const cx = 430
  const cy = 280
  const { pos } = layoutGraph(sample, 860, 560)
  const dist = (id: string) => {
    const p = pos.get(id)!
    return Math.hypot(p.x - cx, p.y - cy)
  }
  const entityMin = Math.min(dist('entity:巡护链路'), dist('entity:dog'))
  const fileMax = Math.max(
    dist('core:MEMORY.md'),
    dist('topic:agents'),
    dist('daily'),
    dist('skill:web'),
  )
  assert.ok(entityMin > fileMax - 30, `entityMin=${entityMin} fileMax=${fileMax}`)
})

test('a single-layer entity view spreads across the disc, not one ring', () => {
  const only: MemoryGraph = { nodes: sample.nodes.filter(isEntity), edges: [] }
  const { pos, clusters } = layoutGraph(only, 860, 560)
  assert.equal(clusters.length, 0)
  assert.equal(pos.size, 2)
})

test('seeding is deterministic and keeps converged positions stable', () => {
  const a = layoutGraph(sample, 860, 560)
  const seed = new Map([...a.pos].map(([id, p]) => [id, { x: p.x, y: p.y }]))
  const b = layoutGraph(sample, 860, 560, seed)
  const c = layoutGraph(sample, 860, 560, seed)
  for (const [id, p] of b.pos) {
    const q = c.pos.get(id)!
    assert.equal(p.x, q.x)
    assert.equal(p.y, q.y)
  }
  // A converged layout is a fixed point: re-seeding from it barely moves.
  for (const [id, p] of a.pos) {
    const q = b.pos.get(id)!
    assert.ok(Math.hypot(p.x - q.x, p.y - q.y) < 5, `${id} drifted`)
  }
})

test('a seeded relayout absorbs a new node without displacing the old ones', () => {
  const before = layoutGraph(sample, 860, 560)
  const seed = new Map([...before.pos].map(([id, p]) => [id, { x: p.x, y: p.y }]))
  const grown: MemoryGraph = {
    nodes: [...sample.nodes, entity('新实体', { weight: 2 })],
    edges: sample.edges,
  }
  const after = layoutGraph(grown, 860, 560, seed)
  for (const [id, p] of before.pos) {
    const q = after.pos.get(id)!
    const moved = Math.hypot(p.x - q.x, p.y - q.y)
    assert.ok(moved < 30, `${id} moved ${moved}`)
  }
})

test('captionExtent charges file labels for their width, bounded', () => {
  assert.equal(captionExtent(entity('x')), 0) // the pill already carries its text
  const short = captionExtent(file('a', 'core', { label: '小' }))
  const long = captionExtent(file('b', 'core', { label: '导航模拟测试代码对照巡护链路全景' }))
  assert.ok(long > short)
  assert.ok(long <= 24)
})

test('dominantSurface picks the strongest mention and breaks ties deterministically', () => {
  const g: MemoryGraph = {
    nodes: [],
    edges: [
      { from: 'f1', to: 'entity:x', kind: 'mention', weight: 2 },
      { from: 'f2', to: 'entity:x', kind: 'mention', weight: 9 },
      { from: 'f3', to: 'entity:x', kind: 'co', weight: 99 },
      { from: 'f1', to: 'entity:y', kind: 'mention', weight: 4 },
      { from: 'f0', to: 'entity:y', kind: 'mention', weight: 4 },
    ],
  }
  const owners = dominantSurface(g)
  assert.equal(owners.get('entity:x'), 'f2')
  assert.equal(owners.get('entity:y'), 'f0') // equal weight → lower id
  assert.equal(owners.size, 2)
})

// A synthetic graph at the scale the real endpoint returns: 10 file
// surfaces, 8 skills, N terms with 4-character labels and varied weights.
const pool = '巡检脚本部署日志链路排查节点任务缓存同步索引模型网关凭证面板会话主题技能图谱'
function synthetic(termCount: number): MemoryGraph {
  const kinds = ['core', 'topic', 'project', 'daily']
  const nodes: MemoryGraphNode[] = []
  for (let i = 0; i < 10; i++) {
    nodes.push(file(`f${i}`, kinds[i % 4]!, { label: `文件面${i}`, weight: 5 + (i % 7) }))
  }
  for (let i = 0; i < 8; i++) {
    nodes.push(file(`s${i}`, 'skill', { label: `技能${i}`, weight: 1 }))
  }
  const edges: MemoryGraph['edges'] = []
  for (let i = 0; i < termCount; i++) {
    const a = (i * 7) % pool.length
    const label = pool.slice(a, a + 3) + pool[(a + 11) % pool.length]!
    const id = `entity:e${i}`
    nodes.push(entity(`e${i}`, { label, weight: 3 + (i % 9) }))
    edges.push({ from: `f${i % 10}`, to: id, kind: 'mention', weight: 3 + (i % 9) })
  }
  return { nodes, edges }
}

function pairStats(graph: MemoryGraph) {
  const { pos } = layoutGraph(graph, 860, 560)
  const pts = [...pos.values()]
  let footprint = 0
  let text = 0
  let coincident = 0
  let outside = 0
  for (const p of pts) if (p.x < 0 || p.x > 860 || p.y < 0 || p.y > 560) outside++
  for (let i = 0; i < pts.length; i++) {
    for (let j = i + 1; j < pts.length; j++) {
      const a = pts[i]!
      const b = pts[j]!
      const d = Math.hypot(a.x - b.x, a.y - b.y)
      if (d < nodeExtent(a) + nodeExtent(b)) footprint++
      if (d < nodeExtent(a) + captionExtent(a) + nodeExtent(b) + captionExtent(b)) text++
      if (d < 8) coincident++
    }
  }
  return { footprint, text, coincident, outside, count: pts.length }
}

test('the default file-only view is fully readable at 860x560', () => {
  const stats = pairStats(synthetic(0))
  assert.equal(stats.count, 18)
  assert.equal(stats.footprint, 0)
  assert.equal(stats.text, 0)
  assert.equal(stats.outside, 0)
})

test('the 60-entity view stays drawable; the ring has no coincident nodes', () => {
  // An 860x560 outer ring physically cannot seat 60 labelled pills without
  // overlap. The full view relies on search, focus and label LOD, not on
  // geometry. What must hold: nothing leaves the canvas, nothing stacks
  // exactly, and the text-overlap count does not regress past the
  // 2026-11-02 baseline of 143 pairs (measured at 108 footprint overlaps).
  const stats = pairStats(synthetic(60))
  assert.equal(stats.count, 78)
  assert.equal(stats.outside, 0)
  assert.equal(stats.coincident, 0)
  assert.ok(stats.text < 143, `text overlaps regressed to ${stats.text}`)
})

test('hidden kinds retain controls so they can be restored', () => {
  const hidden = filterGraph(sample, { layer: 'all', hiddenKinds: new Set(['skill']) })
  assert.ok(hidden.nodes.every((n) => n.kind !== 'skill'))
  assert.ok(availableKinds(sample, 'all').has('skill'))
  assert.ok(!availableKinds(sample, 'entities').has('skill'))
})

test('graph search retains evidence neighbors but respects prior layer filters', () => {
  const result = searchGraph(sample, '巡护链路')
  assert.deepEqual(new Set(result.nodes.map((n) => n.id)), new Set(['entity:巡护链路', 'daily', 'entity:dog']))
  const files = filterGraph(sample, { layer: 'files', hiddenKinds: new Set() })
  const narrowed = searchGraph(files, 'daily')
  assert.ok(narrowed.nodes.every((n) => n.layer === 'file'))
  assert.ok(narrowed.nodes.some((n) => n.id === 'core:MEMORY.md'))
})

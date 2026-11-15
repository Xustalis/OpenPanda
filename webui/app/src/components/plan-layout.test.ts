// SPDX-License-Identifier: AGPL-3.0-or-later

import { test } from 'node:test'
import assert from 'node:assert/strict'
import { planLayout } from './plan-layout.ts'
import type { PlanStage } from '../api/client.ts'

function stage(id: string, needs: string[] = []): PlanStage {
  return { stage: id, task_id: id, state: 'submitted', needs, created_at: 0, updated_at: 0 }
}

test('fork/join flow preserves every dependency and independent branch', () => {
  const graph = planLayout([stage('join', ['pi', 'windows']), stage('pi'), stage('windows')])
  assert.equal(graph.incomplete, false)
  assert.equal(graph.edges.length, 2)
  const nodes = new Map(graph.nodes.map((n) => [n.stage.stage, n]))
  assert.equal(nodes.get('pi')!.x, nodes.get('windows')!.x)
  assert.notEqual(nodes.get('pi')!.y, nodes.get('windows')!.y)
  assert.ok(nodes.get('join')!.x > nodes.get('pi')!.x)
  for (const edge of graph.edges) assert.ok(edge.from.x + 232 < edge.to.x)
})

test('missing or cyclic dependencies are flagged without inventing an execution order', () => {
  const graph = planLayout([stage('a', ['b']), stage('b', ['a']), stage('lost', ['missing']), stage('valid')])
  assert.equal(graph.incomplete, true)
  assert.deepEqual(graph.nodes.map((n) => n.stage.stage), ['valid'])
})

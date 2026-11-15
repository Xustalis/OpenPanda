// SPDX-License-Identifier: AGPL-3.0-or-later

import type { PlanStage } from '../api/client.ts'

export interface PlanPosition { stage: PlanStage; x: number; y: number; rank: number }
export interface PlanEdge { from: PlanPosition; to: PlanPosition }

/** Each column is a dependency depth, not an assertion of simultaneous work.
 * Cycles or missing predecessors remain in the list with a visible warning. */
export function planLayout(stages: PlanStage[]) {
  const pending = new Map(stages.map((s) => [s.stage, s]))
  const ranks = new Map<string, number>()
  while (pending.size) {
    const ready = [...pending.values()].filter((s) => (s.needs ?? []).every((n) => ranks.has(n)))
    if (!ready.length) break
    for (const s of ready) {
      ranks.set(s.stage, Math.max(-1, ...(s.needs ?? []).map((n) => ranks.get(n)!)) + 1)
      pending.delete(s.stage)
    }
  }
  const rows = new Map<number, number>()
  const nodes = stages.filter((s) => ranks.has(s.stage)).map((stage) => {
    const rank = ranks.get(stage.stage)!
    const row = rows.get(rank) ?? 0
    rows.set(rank, row + 1)
    return { stage, rank, x: 20 + rank * 276, y: 20 + row * 130 }
  })
  const byId = new Map(nodes.map((n) => [n.stage.stage, n]))
  const edges: PlanEdge[] = []
  for (const to of nodes) for (const need of to.stage.needs ?? []) {
    const from = byId.get(need)
    if (from) edges.push({ from, to })
  }
  return { nodes, edges, incomplete: pending.size > 0, width: Math.max(272, ...nodes.map((n) => n.x + 252)), height: Math.max(150, ...nodes.map((n) => n.y + 130)) }
}

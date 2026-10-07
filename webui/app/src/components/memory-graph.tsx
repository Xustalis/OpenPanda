// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import type { MemoryGraph, MemoryGraphNode } from '../api/client'
import { t } from '../i18n'

/** The memory knowledge graph (v0.0.10): every memory surface and skill as a
 *  node; edges are provenance ([from:…] promotions) and literal name mentions.
 *  Rendering is dependency-free SVG — a small deterministic force relaxation
 *  gives the organic layout a canvas library would, without the bundle cost
 *  of shipping one inside the Go binary. */

interface Placed extends MemoryGraphNode {
  x: number
  y: number
}

/** Kind → lane on the X axis. The graph reads left-to-right the way memory
 *  flows: raw diary on the left, long-term surfaces in the middle, topical
 *  and project files fanned out on the right, skills below the fold. */
const LANE_X: Record<string, number> = {
  daily: 0.1,
  core: 0.34,
  topic: 0.62,
  project: 0.62,
  skill: 0.88,
}
const LANE_JITTER: Record<string, number> = {
  topic: 0.1,
  project: -0.08,
}

function layout(graph: MemoryGraph, w: number, h: number): Map<string, Placed> {
  const nodes = graph.nodes
  const byKind = new Map<string, MemoryGraphNode[]>()
  for (const n of nodes) {
    const list = byKind.get(n.kind) ?? []
    list.push(n)
    byKind.set(n.kind, list)
  }
  const pos = new Map<string, Placed>()
  // Deterministic seed: nodes fan out inside their lane vertically, in label
  // order — the same data always lands in the same picture.
  for (const [kind, list] of byKind) {
    const cx = (LANE_X[kind] ?? 0.5) * w + (LANE_JITTER[kind] ?? 0) * w
    const sorted = [...list].sort((a, b) => a.label.localeCompare(b.label))
    sorted.forEach((n, i) => {
      const frac = (i + 1) / (sorted.length + 1)
      pos.set(n.id, {
        ...n,
        x: cx,
        y: h * (0.08 + 0.84 * frac),
      })
    })
  }
  // Relax: springs along edges + gentle mutual repulsion. ~90 iterations is
  // enough to unfold crossings without jitter on tiny graphs.
  const edges = graph.edges
  const K = Math.min(w, h) / 4
  for (let it = 0; it < 90; it++) {
    const delta = new Map<string, { dx: number; dy: number }>()
    const push = (id: string, dx: number, dy: number) => {
      const d = delta.get(id) ?? { dx: 0, dy: 0 }
      d.dx += dx
      d.dy += dy
      delta.set(id, d)
    }
    for (const e of edges) {
      const a = pos.get(e.from)
      const b = pos.get(e.to)
      if (!a || !b) continue
      const dx = b.x - a.x
      const dy = b.y - a.y
      const dist = Math.max(Math.hypot(dx, dy), 1)
      const pull = (dist - K) * 0.02
      push(a.id, (dx / dist) * pull, (dy / dist) * pull)
      push(b.id, (-dx / dist) * pull, (-dy / dist) * pull)
    }
    const list = nodes.map((n) => pos.get(n.id)!)
    for (let i = 0; i < list.length; i++) {
      for (let j = i + 1; j < list.length; j++) {
        const a = list[i]
        const b = list[j]
        if (!a || !b) continue
        let dx = b.x - a.x
        let dy = b.y - a.y
        const d2 = dx * dx + dy * dy
        if (d2 < 1) {
          dx = 0.5
          dy = 0.5
        }
        const dist = Math.max(Math.sqrt(d2), 12)
        const rep = Math.min((K * K) / (dist * dist), 6) * 0.6
        push(a.id, (-dx / dist) * rep, (-dy / dist) * rep)
        push(b.id, (dx / dist) * rep, (dy / dist) * rep)
      }
    }
    for (const n of nodes) {
      const p = pos.get(n.id)!
      const d = delta.get(n.id)
      if (d) {
        p.x += d.dx
        p.y += d.dy
      }
      // Lane pull keeps the left-to-right story readable instead of letting
      // the sim collapse kinds into a knot.
      const laneX = (LANE_X[n.kind] ?? 0.5) * w + (LANE_JITTER[n.kind] ?? 0) * w
      p.x += (laneX - p.x) * 0.012
      p.x = Math.min(w - 40, Math.max(40, p.x))
      p.y = Math.min(h - 30, Math.max(30, p.y))
    }
  }
  return pos
}

function nodeRadius(n: MemoryGraphNode): number {
  if (n.kind === 'core') return 16
  const scale = Math.sqrt(Math.max(n.chars ?? 0, 1))
  return Math.min(14, Math.max(6, scale * 0.9))
}

const KIND_CLASS: Record<string, string> = {
  core: 'mg-core',
  topic: 'mg-topic',
  project: 'mg-project',
  daily: 'mg-daily',
  skill: 'mg-skill',
}

export function MemoryGraphView({
  graph,
  onOpen,
}: {
  graph: MemoryGraph
  onOpen?(node: MemoryGraphNode): void
}) {
  const wrap = useRef<HTMLDivElement>(null)
  const [size, setSize] = useState({ w: 860, h: 520 })
  const [hover, setHover] = useState<string | null>(null)
  const [selected, setSelected] = useState<string | null>(null)

  useEffect(() => {
    const el = wrap.current
    if (!el) return
    const ro = new ResizeObserver(() => {
      const r = el.getBoundingClientRect()
      if (r.width > 0) setSize({ w: r.width, h: Math.max(420, Math.min(640, r.width * 0.62)) })
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  const pos = useMemo(() => layout(graph, size.w, size.h), [graph, size.w, size.h])
  const neighbors = useMemo(() => {
    const set = new Set<string>()
    if (hover) {
      for (const e of graph.edges) {
        if (e.from === hover) set.add(e.to)
        if (e.to === hover) set.add(e.from)
      }
    }
    return set
  }, [graph, hover])

  const sel = selected ? graph.nodes.find((n) => n.id === selected) : null
  const selEdges = useMemo(
    () => (sel ? graph.edges.filter((e) => e.from === sel.id || e.to === sel.id) : []),
    [graph, sel],
  )
  const labelOf = (id: string) => graph.nodes.find((n) => n.id === id)?.label ?? id

  return (
    <div class="mg-wrap" ref={wrap}>
      <svg
        class="mg-canvas"
        viewBox={`0 0 ${size.w} ${size.h}`}
        role="img"
        aria-label={t('memory.graphTitle')}
      >
        {graph.edges.map((e, i) => {
          const a = pos.get(e.from)
          const b = pos.get(e.to)
          if (!a || !b) return null
          const mx = (a.x + b.x) / 2
          const my = (a.y + b.y) / 2 - 24
          const dim = hover !== null && e.from !== hover && e.to !== hover
          return (
            <path
              key={i}
              class={`mg-edge mg-edge-${e.kind}${dim ? ' dim' : ''}`}
              d={`M ${a.x} ${a.y} Q ${mx} ${my} ${b.x} ${b.y}`}
            />
          )
        })}
        {graph.nodes.map((n) => {
          const p = pos.get(n.id)!
          const dimmed = hover !== null && hover !== n.id && !neighbors.has(n.id)
          return (
            <g
              key={n.id}
              class={`mg-node ${KIND_CLASS[n.kind] ?? 'mg-core'}${dimmed ? ' dim' : ''}${
                selected === n.id ? ' sel' : ''
              }${n.status === 'pending' ? ' pending' : ''}`}
              transform={`translate(${p.x},${p.y})`}
              onMouseEnter={() => setHover(n.id)}
              onMouseLeave={() => setHover((v) => (v === n.id ? null : v))}
              onClick={() => setSelected(n.id === selected ? null : n.id)}
            >
              <circle r={nodeRadius(n)} />
              <text y={nodeRadius(n) + 14} text-anchor="middle">
                {n.label}
              </text>
            </g>
          )
        })}
      </svg>
      <div class="mg-legend">
        {(['core', 'topic', 'project', 'daily', 'skill'] as const).map((k) => (
          <span key={k} class={`mg-legend-item ${KIND_CLASS[k]}`}>
            <i /> {t(`memory.graph.${k}`)}
          </span>
        ))}
        <span class="mg-legend-item mg-legend-edge-promo">
          <svg width="22" height="8"><line x1="0" y1="4" x2="22" y2="4" /></svg>
          {t('memory.graph.promotion')}
        </span>
        <span class="mg-legend-item mg-legend-edge-ref">
          <svg width="22" height="8"><line x1="0" y1="4" x2="22" y2="4" /></svg>
          {t('memory.graph.reference')}
        </span>
      </div>
      {sel && (
        <div class="mg-detail card">
          <div class="mg-detail-head">
            <span class={`badge ${sel.kind === 'core' ? 'green' : sel.kind === 'project' ? 'blue' : sel.kind === 'skill' ? 'yellow' : ''}`}>
              {t(`memory.graph.${sel.kind}`)}
            </span>
            <strong class="mono">{sel.label}</strong>
            {sel.status && <span class="badge yellow">{sel.status}</span>}
            <button class="icon-btn" onClick={() => setSelected(null)} aria-label="×">×</button>
          </div>
          {selEdges.length > 0 ? (
            <ul class="mg-detail-edges">
              {selEdges.map((e, i) => (
                <li key={i}>
                  <span class="dim">{e.from === sel.id ? '→' : '←'}</span>
                  <span class="mono">{labelOf(e.from === sel.id ? e.to : e.from)}</span>
                  <span class="dim">· {t(`memory.graph.${e.kind}`)}</span>
                </li>
              ))}
            </ul>
          ) : (
            <p class="dim">{t('memory.graph.noEdges')}</p>
          )}
          {onOpen && sel.kind !== 'core' && sel.kind !== 'daily' && (
            <button class="btn small" onClick={() => onOpen(sel)}>
              {t('memory.graph.open')}
            </button>
          )}
        </div>
      )}
    </div>
  )
}

// SPDX-License-Identifier: AGPL-3.0-or-later

import type { MemoryGraph, MemoryGraphNode } from '../api/client'

/** Pure geometry/model helpers for the memory graph — no DOM, no randomness,
 *  so the same graph always lands in the same picture and the whole layout is
 *  unit-testable (node --test). The component (memory-graph.tsx) owns the SVG.
 *
 *  Geometry: provenance surfaces live in an inner disc, clustered around one
 *  anchor per kind; knowledge terms ring the outside when both layers show. A
 *  no-overlap relaxation then unfolds the clusters, anchored so the layers
 *  never swap places. Single-layer views get the whole canvas. */

export type LayerMode = 'all' | 'files' | 'entities'

export interface Placed extends MemoryGraphNode {
  x: number
  y: number
}

export interface Cluster {
  key: string
  x: number
  y: number
  /** Soft-halo radius — grows with the cluster's node count. */
  r: number
}

export interface Layout {
  pos: Map<string, Placed>
  clusters: Cluster[]
}

export const ENTITY_KIND = 'entity'

export function isEntity(n: MemoryGraphNode): boolean {
  return n.layer === 'entity' || n.kind === ENTITY_KIND
}

/** Canonical kind ordering — the sequence anchors take when dividing the
 *  circle among the kinds actually present. */
const KIND_ORDER = ['core', 'topic', 'project', 'daily', 'skill']

/** Keep only the nodes the active layer/kind/date filters admit; an edge
 *  survives only when both of its endpoints do. Nodes without a first_seen
 *  date are always visible (the files predate the diary). */
export function filterGraph(
  graph: MemoryGraph,
  opts: { layer: LayerMode; hiddenKinds: ReadonlySet<string>; until?: string },
): MemoryGraph {
  const nodes = graph.nodes.filter((n) => {
    if (opts.layer === 'files' && isEntity(n)) return false
    if (opts.layer === 'entities' && !isEntity(n)) return false
    if (opts.hiddenKinds.has(n.kind)) return false
    if (opts.until && n.first_seen && n.first_seen > opts.until) return false
    return true
  })
  const ids = new Set(nodes.map((n) => n.id))
  const edges = graph.edges.filter((e) => ids.has(e.from) && ids.has(e.to))
  return { nodes, edges }
}

/** Search keeps one hop of context so a matching term still shows which
 *  memory surfaces support it. It never restores a layer/kind already hidden
 *  by filterGraph; the caller applies that first. */
export function searchGraph(graph: MemoryGraph, query: string): MemoryGraph {
  const q = query.trim().toLowerCase()
  if (!q) return graph
  const matches = new Set(graph.nodes.filter((n) => n.label.toLowerCase().includes(q)).map((n) => n.id))
  const visible = new Set(matches)
  for (const e of graph.edges) {
    if (matches.has(e.from)) visible.add(e.to)
    if (matches.has(e.to)) visible.add(e.from)
  }
  return {
    nodes: graph.nodes.filter((n) => visible.has(n.id)),
    edges: graph.edges.filter((e) => visible.has(e.from) && visible.has(e.to)),
  }
}

/** Kind controls reflect the unfiltered graph. Otherwise hiding a kind
 *  immediately removes its own toggle, trapping that category invisible. */
export function availableKinds(graph: MemoryGraph, layer: LayerMode): Set<string> {
  return new Set(graph.nodes.filter((n) =>
    layer === 'all' || (layer === 'entities' ? isEntity(n) : !isEntity(n)),
  ).map((n) => n.kind))
}

/** Distinct first_seen dates, ascending — the timeline's play steps. */
export function seenDates(graph: MemoryGraph): string[] {
  const set = new Set<string>()
  for (const n of graph.nodes) if (n.first_seen) set.add(n.first_seen)
  return [...set].sort()
}

/** Evidence mass → rendered radius: √ keeps a 9× node from dwarfing the rest. */
export function nodeRadius(n: MemoryGraphNode): number {
  const w = Math.max(1, n.weight ?? 1)
  if (isEntity(n)) return Math.min(13, 5.5 + 1.25 * Math.sqrt(w))
  if (n.kind === 'core') return Math.min(19, 9.5 + 1.3 * Math.sqrt(w))
  return Math.min(17, 7.5 + 1.3 * Math.sqrt(w))
}

/** Freshness → node opacity: touched within a week stays fully lit, an old
 *  surface fades toward the floor. */
export function recencyAlpha(lastSeen: string | undefined, now: number): number {
  if (!lastSeen) return 1
  const t = Date.parse(lastSeen)
  if (Number.isNaN(t)) return 1
  const days = Math.max(0, (now - t) / 86400000)
  if (days <= 7) return 1
  if (days >= 240) return 0.6
  return 1 - 0.4 * ((days - 7) / 233)
}

/** Display width in "units": CJK ≈ 1, latin ≈ 0.55 (they render roughly half
 *  as wide at the graph's font size). */
function uwidth(s: string): number {
  return [...s].reduce((n, ch) => n + (ch.charCodeAt(0) < 0x100 ? 0.55 : 1), 0)
}

/** Wrap a label into at most two lines (CJK ≈ 1 unit, latin ≈ 0.55), with an
 *  ellipsis when even two lines do not fit. Latin names wrap at separator
 *  boundaries (- _ space /) when one sits inside the window — "node-" /
 *  "workflow-…" reads far better than a mid-token cut. */
export function labelLines(label: string, maxUnits = 7): string[] {
  if (uwidth(label) <= maxUnits) return [label]
  const chars = [...label]
  const take = (cap: number): string => {
    let cur = ''
    while (chars.length > 0 && uwidth(cur + chars[0]!) <= cap) cur += chars.shift()!
    if (chars.length > 0) {
      const cut = Math.max(
        cur.lastIndexOf('-'),
        cur.lastIndexOf('_'),
        cur.lastIndexOf(' '),
        cur.lastIndexOf('/'),
      )
      if (cut >= 2) {
        chars.unshift(...[...cur.slice(cut + 1)])
        cur = cur.slice(0, cut + 1)
      }
    }
    return cur
  }
  const first = take(maxUnits)
  let second = take(maxUnits)
  if (chars.length > 0) {
    while (second && uwidth(second + '…') > maxUnits) second = second.slice(0, -1)
    second += '…'
  }
  return [first, second]
}

/** Single-line label for inside an entity pill — truncated with an ellipsis
 *  that keeps one unit of room for the '…' itself. */
export function fitText(label: string, maxUnits = 10): string {
  if (uwidth(label) <= maxUnits) return label
  let cur = ''
  for (const ch of [...label]) {
    if (uwidth(cur + ch) + 1 > maxUnits) break
    cur += ch
  }
  return cur + '…'
}

/** Visual footprint radius — what spacing and edge endpoints must respect.
 *  Files are circles (nodeRadius); entities are pills sized by their label,
 *  so a long term's half-width is its extent. */
export function nodeExtent(n: MemoryGraphNode): number {
  if (isEntity(n)) {
    return Math.max(15, (uwidth(fitText(n.label)) * 10.6 + 18) / 2)
  }
  return nodeRadius(n)
}

/** File captions hang outside their dot; the circle-only footprint used to
 *  ignore that width entirely, so two labels could sit on top of a neighbour
 *  even while every dot was "non-overlapping". Bounded: a long caption must
 *  not push the whole inner disc apart. */
export function captionExtent(n: MemoryGraphNode): number {
  if (isEntity(n)) return 0 // the pill's nodeExtent already includes its text
  const widest = Math.max(...labelLines(n.label).map((l) => uwidth(l)))
  return Math.min(24, (widest * 10.6) / 2 + 4)
}

/** The surface an entity belongs to: the strongest `mention` edge wins.
 *  Relations, not just node counts, decide where terms start out — a term
 *  mentioned mostly in one topic lands beside it instead of at a random
 *  angle on the outer ring. Ties break by surface id so the result stays
 *  deterministic. Entities with no mention edge keep the even spread. */
export function dominantSurface(graph: MemoryGraph): Map<string, string> {
  const best = new Map<string, { id: string; weight: number }>()
  for (const e of graph.edges) {
    if (e.kind !== 'mention') continue
    const w = e.weight ?? 1
    const cur = best.get(e.to)
    if (!cur || w > cur.weight || (w === cur.weight && e.from < cur.id)) {
      best.set(e.to, { id: e.from, weight: w })
    }
  }
  const out = new Map<string, string>()
  for (const [id, s] of best) out.set(id, s.id)
  return out
}

/** Distance from the node's centre to its visual rim along the unit vector
 *  (ux, uy). Pills are ellipses (half-width × 11), so an edge approaching
 *  from above lands much closer than one coming from the side — a radial
 *  half-width guess leaves ugly gaps on vertical approaches. */
export function rimDistance(n: MemoryGraphNode, ux: number, uy: number): number {
  if (!isEntity(n)) return nodeRadius(n)
  const rx = nodeExtent(n)
  const ry = 11
  return (rx * ry) / Math.sqrt(ry * ry * ux * ux + rx * rx * uy * uy)
}

/** Deterministic cluster-and-relax layout. `seed` carries positions from a
 *  previous layout of mostly-the-same nodes (timeline ticks, filter toggles):
 *  surviving nodes start where they were, so playback ripples instead of
 *  reshuffling — and the relax loop's early exit makes a seeded pass nearly
 *  free. */
export function layoutGraph(
  graph: MemoryGraph,
  w: number,
  h: number,
  seed?: ReadonlyMap<string, { x: number; y: number }>,
): Layout {
  const cx = w / 2
  const cy = h / 2
  const R = Math.max(120, Math.min(w, h) / 2 - 58)

  const files = graph.nodes.filter((n) => !isEntity(n))
  const entities = graph.nodes.filter(isEntity)
  const mixed = files.length > 0 && entities.length > 0

  const innerR = mixed ? R * 0.52 : R * 0.92
  // The old outer band was only ~80px deep: 60 terms could not fit and the
  // relaxation had nowhere to put them. Widening inward gives the ring real
  // area; repulsion keeps terms clear of the inner file clusters.
  const outerMin = mixed ? R * 0.48 : R * 0.12
  const outerMax = mixed ? R * 0.98 : R * 0.92

  const pos = new Map<string, Placed>()
  const clusters: Cluster[] = []
  const byKind = new Map<string, MemoryGraphNode[]>()
  for (const n of files) {
    const list = byKind.get(n.kind) ?? []
    list.push(n)
    byKind.set(n.kind, list)
  }
  const byWeight = (a: MemoryGraphNode, b: MemoryGraphNode) =>
    (b.weight ?? 1) - (a.weight ?? 1) || a.label.localeCompare(b.label)

  // Seed files: a sunflower fan around each kind anchor (golden angle keeps
  // same-weight nodes from stacking). Anchors divide the circle among the
  // kinds actually present — three kinds get 120° of separation instead of
  // squeezing into neighbouring slots of a fixed pentagon.
  const kindRank = (k: string) => {
    const i = KIND_ORDER.indexOf(k)
    return i < 0 ? KIND_ORDER.length : i
  }
  const presentKinds = [...byKind.keys()].sort((a, b) => kindRank(a) - kindRank(b) || a.localeCompare(b))
  const anchors = new Map<string, { x: number; y: number }>()
  for (const [kind, list] of byKind) {
    const angle = -Math.PI / 2 + (presentKinds.indexOf(kind) / Math.max(1, presentKinds.length)) * Math.PI * 2
    const ax = cx + Math.cos(angle) * innerR * 0.78
    const ay = cy + Math.sin(angle) * innerR * 0.78
    anchors.set(kind, { x: ax, y: ay })
    clusters.push({ key: kind, x: ax, y: ay, r: 46 + 17 * Math.sqrt(list.length) })
    ;[...list].sort(byWeight).forEach((n, i) => {
      const s = seed?.get(n.id)
      const t = i * 2.399963
      const rr = i === 0 ? 0 : 13 + 11 * Math.sqrt(i)
      pos.set(n.id, { ...n, x: s?.x ?? ax + Math.cos(t) * rr, y: s?.y ?? ay + Math.sin(t) * rr })
    })
  }

  // Seed entities: terms sharing a dominant surface start as neighbours on
  // the outer ring, so their mention edges stay short instead of crossing
  // the canvas. Unowned terms come last and still get an even angular
  // spread; the golden-ratio fraction scatters depth into an organic halo.
  const owners = dominantSurface(graph)
  const ownerOf = (n: MemoryGraphNode) => owners.get(n.id) ?? '\uffff'
  ;[...entities]
    .sort((a, b) => ownerOf(a).localeCompare(ownerOf(b)) || byWeight(a, b))
    .forEach((n, i) => {
      const s = seed?.get(n.id)
      const t = -Math.PI / 2 + (i / Math.max(1, entities.length)) * Math.PI * 2
      const frac = (i * 0.618034) % 1
      const rr = outerMin + (outerMax - outerMin) * (0.16 + 0.68 * frac)
      pos.set(n.id, { ...n, x: s?.x ?? cx + Math.cos(t) * rr, y: s?.y ?? cy + Math.sin(t) * rr })
    })

  const list = graph.nodes.map((n) => pos.get(n.id)!)
  // Spacing follows each node's real footprint (pill width / circle radius)
  // plus a per-pair air gap — long terms get room, small circles pack tighter.
  const minDist = (a: Placed, b: Placed) => {
    // Captions hang outside file dots, so their half-width joins the pair's
    // spacing requirement; entity pills already carry their text in
    // nodeExtent. The per-pair air gap stays generous on purpose.
    const gap = isEntity(a) && isEntity(b) ? 11 : isEntity(a) || isEntity(b) ? 20 : 26
    return nodeExtent(a) + captionExtent(a) + nodeExtent(b) + captionExtent(b) + gap
  }
  const clampRadius = (p: Placed, lo: number, hi: number) => {
    const dx = p.x - cx
    const dy = p.y - cy
    const d = Math.max(1, Math.hypot(dx, dy))
    const target = d < lo ? lo : d > hi ? hi : d
    if (target !== d) {
      p.x = cx + (dx / d) * target
      p.y = cy + (dy / d) * target
    }
  }

  // Relax: no-overlap repulsion + a soft pull back to the layer's home band.
  // `moved` tracks the largest displacement per iteration; once nothing moves
  // by more than a fraction of a unit the picture has converged — a seeded
  // re-layout typically exits in single-digit iterations instead of 140.
  for (let it = 0; it < 140; it++) {
    let moved = 0
    for (let i = 0; i < list.length; i++) {
      for (let j = i + 1; j < list.length; j++) {
        const a = list[i]!
        const b = list[j]!
        let dx = b.x - a.x
        let dy = b.y - a.y
        let d2 = dx * dx + dy * dy
        if (d2 < 1e-4) {
          dx = i - j || 0.5
          dy = 0.5
          d2 = dx * dx + dy * dy
        }
        const d = Math.sqrt(d2)
        const min = minDist(a, b)
        if (d < min) {
          const push = ((min - d) / d) * 0.32
          a.x -= dx * push
          a.y -= dy * push
          b.x += dx * push
          b.y += dy * push
          moved = Math.max(moved, (min - d) * 0.32)
        }
      }
    }
    for (const p of list) {
      const px = p.x
      const py = p.y
      if (isEntity(p)) {
        const d = Math.max(1, Math.hypot(p.x - cx, p.y - cy))
        const home = mixed ? (outerMin + outerMax) / 2 : R * 0.5
        const pull = ((home - d) / d) * 0.05
        p.x += (p.x - cx) * pull
        p.y += (p.y - cy) * pull
        // Angular spring: drift toward the bearing of the dominant surface.
        // Tangential only, so the radius band stays intact and the repulsion
        // pass still resolves crowding inside a group. It runs only on the
        // first (unseeded) pass: a seeded re-layout must stay a fixed point,
        // otherwise timeline ticks would keep rotating the ring forever.
        const owner = owners.get(p.id)
        const ownerPos = owner ? pos.get(owner) : undefined
        if (ownerPos && !seed) {
          const target = Math.atan2(ownerPos.y - cy, ownerPos.x - cx)
          const cur = Math.atan2(p.y - cy, p.x - cx)
          const delta = Math.atan2(Math.sin(target - cur), Math.cos(target - cur))
          if (Math.abs(delta) > 1e-3) {
            const step = delta * 0.03
            const rx = p.x - cx
            const ry = p.y - cy
            p.x = cx + rx * Math.cos(step) - ry * Math.sin(step)
            p.y = cy + rx * Math.sin(step) + ry * Math.cos(step)
          }
        }
        clampRadius(p, mixed ? outerMin : 0, outerMax)
      } else {
        const a = anchors.get(p.kind)
        if (a) {
          p.x += (a.x - p.x) * 0.035
          p.y += (a.y - p.y) * 0.035
        }
        clampRadius(p, 0, innerR)
      }
      moved = Math.max(moved, Math.hypot(p.x - px, p.y - py))
    }
    if (moved < 0.05) break
  }

  return { pos, clusters }
}

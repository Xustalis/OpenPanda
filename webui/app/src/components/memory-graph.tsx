// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import type { MemoryGraph, MemoryGraphEdge, MemoryGraphNode } from '../api/client'
import { t } from '../i18n'
import {
  availableKinds,
  captionExtent,
  ENTITY_KIND,
  filterGraph,
  isEntity,
  labelLines,
  layoutGraph,
  nodeExtent,
  nodeRadius,
  recencyAlpha,
  rimDistance,
  searchGraph,
  seenDates,
  type LayerMode,
} from './graph-layout'

/** Long-term facts and the files that retain them. Each fact has a stable
 *  attribute ID and an explicit source edge. Daily logs, task output and skill
 *  descriptions are excluded by the server. Pan, zoom, search and the detail
 *  card work with both current fact graphs and older server responses. */

interface View {
  k: number // zoom scale
  x: number // pan offset, graph units
  y: number
}

const VIEW_HOME: View = { k: 1, x: 0, y: 0 }
const ZOOM_MIN = 0.4
const ZOOM_MAX = 5

const KIND_CLASS: Record<string, string> = {
  core: 'mg-core',
  topic: 'mg-topic',
  project: 'mg-project',
  daily: 'mg-daily',
  skill: 'mg-skill',
  [ENTITY_KIND]: 'mg-entity',
}

const EDGE_CLASS: Record<string, string> = {
  promotion: 'mg-edge-promotion',
  reference: 'mg-edge-reference',
  mention: 'mg-edge-mention',
  co: 'mg-edge-co',
}

const FILE_KIND_ORDER = ['core', 'topic', 'project', 'daily', 'skill'] as const

export function MemoryGraphView({
  graph,
  onOpen,
}: {
  graph: MemoryGraph
  onOpen?(node: MemoryGraphNode): void
}) {
  const wrap = useRef<HTMLDivElement>(null)
  const svgRef = useRef<SVGSVGElement>(null)
  const [size, setSize] = useState({ w: 860, h: 560 })
  const [hover, setHover] = useState<string | null>(null)
  const [selected, setSelected] = useState<string | null>(null)
  const [view, setView] = useState<View>(VIEW_HOME)
  const [panning, setPanning] = useState(false)
  // Show facts together with their sources; keep legacy noisy layers hidden.
  const [layer, setLayer] = useState<LayerMode>('all')
  const [hiddenKinds, setHiddenKinds] = useState<ReadonlySet<string>>(new Set(['skill']))
  const [hiddenEdges, setHiddenEdges] = useState<ReadonlySet<string>>(new Set(['co']))
  const [query, setQuery] = useState('')
  const [neighborsOnly, setNeighborsOnly] = useState(false)
  const [until, setUntil] = useState('')
  const [playing, setPlaying] = useState(false)
  const drag = useRef<{ px: number; py: number; vx: number; vy: number } | null>(null)

  useEffect(() => {
    const el = wrap.current
    if (!el) return
    const ro = new ResizeObserver(() => {
      const r = el.getBoundingClientRect()
      if (r.width > 0) setSize({ w: r.width, h: Math.max(520, Math.min(780, r.width * 0.7)) })
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  const zoomAt = (cx: number, cy: number, factor: number) => {
    setView((v) => {
      const k = Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, v.k * factor))
      const s = k / v.k
      return { k, x: cx - (cx - v.x) * s, y: cy - (cy - v.y) * s }
    })
  }

  // Manual wheel binding: React-style synthetic handlers cannot preventDefault
  // inside a passive listener, so the zoom lives on a real non-passive one.
  useEffect(() => {
    const el = svgRef.current
    if (!el) return
    const onWheel = (e: WheelEvent) => {
      e.preventDefault()
      const r = el.getBoundingClientRect()
      const s = size.w / r.width
      zoomAt((e.clientX - r.left) * s, (e.clientY - r.top) * s, e.deltaY < 0 ? 1.15 : 1 / 1.15)
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [size.w])

  // Timeline: play walks the distinct first-seen dates; nodes without one
  // (files that predate the diary) stay visible throughout.
  const steps = useMemo(() => seenDates(graph), [graph])
  const stepIdx = until ? Math.max(0, steps.indexOf(until)) : steps.length - 1
  const effectiveUntil = until || steps[steps.length - 1] || ''
  useEffect(() => {
    if (!playing) return
    let i = stepIdx
    const id = window.setInterval(() => {
      i += 1
      const step = steps[i]
      if (!step) {
        setPlaying(false)
        return
      }
      setUntil(step)
    }, 460)
    return () => window.clearInterval(id)
    // stepIdx is read once per playback run on purpose: the interval advances
    // from wherever the user left the slider.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [playing, steps])

  // Selection behaves like a persistent hover: a picked node keeps its
  // neighbourhood lit after the pointer leaves.
  const focus = hover ?? selected
  const filtered = useMemo(() => {
    const base = filterGraph(graph, { layer, hiddenKinds, until: effectiveUntil })
    const searched = searchGraph({ nodes: base.nodes, edges: base.edges.filter((e) => !hiddenEdges.has(e.kind)) }, query)
    const edges = searched.edges
    let nodes = searched.nodes
    if (neighborsOnly && selected) {
      const keep = new Set<string>([selected])
      for (const e of edges) {
        if (e.from === selected) keep.add(e.to)
        if (e.to === selected) keep.add(e.from)
      }
      nodes = nodes.filter((n) => keep.has(n.id))
    }
    const ids = new Set(nodes.map((n) => n.id))
    return { nodes, edges: edges.filter((e) => ids.has(e.from) && ids.has(e.to)) }
  }, [graph, layer, hiddenKinds, hiddenEdges, effectiveUntil, query, neighborsOnly, selected])
  // Positions persist across filter/timeline changes: the layout is seeded
  // with the previous one (same canvas size), so playing the timeline makes
  // nodes appear in place rather than reshuffling the whole picture, and the
  // relax loop's early exit makes each tick nearly free.
  const prevLayout = useRef<{ w: number; h: number; pos: Map<string, { x: number; y: number }> } | null>(null)
  const layout = useMemo(() => {
    const prev = prevLayout.current
    const seed = prev && prev.w === size.w && prev.h === size.h ? prev.pos : undefined
    const next = layoutGraph(filtered, size.w, size.h, seed)
    prevLayout.current = {
      w: size.w,
      h: size.h,
      pos: new Map([...next.pos].map(([id, p]) => [id, { x: p.x, y: p.y }])),
    }
    return next
  }, [filtered, size])
  const pos = layout.pos

  const byId = useMemo(() => new Map(filtered.nodes.map((n) => [n.id, n])), [filtered])

  // Parallel edges between the same pair fan out symmetrically around the
  // straight line — a lone edge stays straight (offset 0), pairs spread
  // evenly so a promotion and a reference never draw on top of each other.
  const edgeSpread = useMemo(() => {
    const pairKey = (e: MemoryGraphEdge) => [e.from, e.to].sort().join('|')
    const count = new Map<string, number>()
    for (const e of filtered.edges) {
      const k = pairKey(e)
      count.set(k, (count.get(k) ?? 0) + 1)
    }
    const seen = new Map<string, number>()
    const out = new Map<number, number>()
    filtered.edges.forEach((e, i) => {
      const k = pairKey(e)
      const idx = seen.get(k) ?? 0
      seen.set(k, idx + 1)
      out.set(i, (idx - ((count.get(k) ?? 1) - 1) / 2) * 14)
    })
    return out
  }, [filtered])

  // Neighbours of the hovered or selected node drive the highlight.
  const neighbors = useMemo(() => {
    const set = new Set<string>()
    if (focus) {
      for (const e of filtered.edges) {
        if (e.from === focus) set.add(e.to)
        if (e.to === focus) set.add(e.from)
      }
    }
    return set
  }, [filtered, focus])

  const sel = selected ? byId.get(selected) : undefined
  const selEdges = useMemo(
    () => (sel ? filtered.edges.filter((e) => e.from === sel.id || e.to === sel.id) : []),
    [filtered, sel],
  )
  const labelOf = (id: string) => byId.get(id)?.label ?? id

  const kindsPresent = useMemo(() => availableKinds(graph, layer), [graph, layer])

  const chips = useMemo(() => {
    const order = layer === 'entities' ? [ENTITY_KIND] : layer === 'files' ? [...FILE_KIND_ORDER] : [...FILE_KIND_ORDER, ENTITY_KIND]
    return order.filter((k) => kindsPresent.has(k))
  }, [layer, kindsPresent])

  const hasEntities = useMemo(() => graph.nodes.some(isEntity), [graph])
  const hasFiles = useMemo(() => graph.nodes.some((n) => !isEntity(n)), [graph])
  const edgeLegend = (['promotion', 'reference', 'mention', 'co'] as const)
    .filter((kind) => graph.edges.some((edge) => edge.kind === kind))

  const now = Date.now()
  const viewDirty = Math.abs(view.k - 1) > 0.01 || Math.abs(view.x) > 1 || Math.abs(view.y) > 1

  const toggleKind = (kind: string) => {
    setHiddenKinds((prev) => {
      const next = new Set(prev)
      if (next.has(kind)) next.delete(kind)
      else next.add(kind)
      return next
    })
  }

  const toggleEdge = (kind: string) => {
    setHiddenEdges((prev) => {
      const next = new Set(prev)
      if (next.has(kind)) next.delete(kind)
      else next.add(kind)
      return next
    })
  }

  // Label LOD: a dense or zoomed-out canvas draws dots only. Close range
  // brings every label back, so reading tools stay available without the
  // all-at-once text collision of the old default view.
  const showLabels = view.k >= 1.2 || (view.k >= 0.7 && filtered.nodes.length <= 30)

  const fitView = () => {
    const pts = [...pos.values()]
    if (pts.length === 0) return
    let minX = Infinity
    let minY = Infinity
    let maxX = -Infinity
    let maxY = -Infinity
    for (const p of pts) {
      const e = nodeExtent(p) + captionExtent(p)
      minX = Math.min(minX, p.x - e)
      maxX = Math.max(maxX, p.x + e)
      minY = Math.min(minY, p.y - e)
      maxY = Math.max(maxY, p.y + e)
    }
    const pad = 24
    const bw = maxX - minX + pad * 2
    const bh = maxY - minY + pad * 2
    const k = Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, Math.min(size.w / bw, size.h / bh)))
    setView({
      k,
      x: size.w / 2 - ((minX + maxX) / 2) * k,
      y: size.h / 2 - ((minY + maxY) / 2) * k,
    })
  }

  return (
    <div class={`mg-wrap${panning ? ' panning' : ''}`} ref={wrap}>
      <div class="mg-toolbar">
        <div class="segmented mg-layers" role="group" aria-label={t('memory.graph.layers')}>
          {(['all', 'files', 'entities'] as const).map((m) => {
            const empty = (m === 'files' && !hasFiles) || (m === 'entities' && !hasEntities)
            return (
              <button
                key={m}
                type="button"
                class={`seg${layer === m ? ' on' : ''}`}
                aria-pressed={layer === m}
                disabled={empty}
                onClick={() => {
                  setLayer(m)
                  if (m === 'entities') setHiddenEdges((prev) => {
                    const next = new Set(prev)
                    next.delete('co')
                    return next
                  })
                }}
              >
                {t(`memory.graph.layer.${m}`)}
              </button>
            )
          })}
        </div>
        <input
          class="mg-search"
          type="search"
          value={query}
          placeholder={t('memory.graph.searchPlaceholder')}
          aria-label={t('memory.graph.search')}
          onInput={(e) => setQuery((e.target as HTMLInputElement).value)}
        />
        <button
          type="button"
          class={`btn small${neighborsOnly ? ' on' : ''}`}
          aria-pressed={neighborsOnly}
          disabled={!selected}
          title={t('memory.graph.neighbors')}
          onClick={() => setNeighborsOnly((v) => !v)}
        >
          {t('memory.graph.neighbors')}
        </button>
        <span class="mg-stats dim mono">
          {t('memory.graph.nodesN', { n: filtered.nodes.length })} ·{' '}
          {t('memory.graph.edgesN', { n: filtered.edges.length })}
        </span>
        <div class="mg-toolbar-right">
          <button type="button" class="btn small" aria-label={t('memory.graph.fit')} title={t('memory.graph.fit')} onClick={fitView}>
            {t('memory.graph.fit')}
          </button>
          <button type="button" class="btn small" aria-label={t('memory.graph.zoomOut')} onClick={() => zoomAt(size.w / 2, size.h / 2, 1 / 1.3)}>
            −
          </button>
          <button type="button" class="btn small" aria-label={t('memory.graph.zoomIn')} onClick={() => zoomAt(size.w / 2, size.h / 2, 1.3)}>
            +
          </button>
          {viewDirty && (
            <button type="button" class="btn small" onClick={() => setView(VIEW_HOME)}>
              {t('memory.graph.reset')}
            </button>
          )}
        </div>
      </div>
      {filtered.nodes.length === 0 ? (
        <p class="dim mg-empty">
          {t(graph.nodes.length === 0 ? 'memory.graphEmpty' : 'memory.graph.filteredEmpty')}
        </p>
      ) : (
        <div class="mg-stage">
          <svg
            ref={svgRef}
            class="mg-canvas"
            viewBox={`0 0 ${size.w} ${size.h}`}
            role="group"
            aria-label={t('memory.graphTitle')}
            onPointerDown={(e) => {
              const el = e.target as Element
              if (!el.classList?.contains('mg-bg')) return
              el.setPointerCapture?.(e.pointerId)
              drag.current = { px: e.clientX, py: e.clientY, vx: view.x, vy: view.y }
              setPanning(true)
            }}
            onPointerMove={(e) => {
              const d = drag.current
              if (!d) return
              const r = svgRef.current!.getBoundingClientRect()
              const s = size.w / r.width
              setView((v) => ({
                ...v,
                x: d.vx + (e.clientX - d.px) * s,
                y: d.vy + (e.clientY - d.py) * s,
              }))
            }}
            onPointerUp={(e) => {
              const d = drag.current
              drag.current = null
              setPanning(false)
              // A press that never moved is a click on empty canvas — drop
              // the current selection.
              if (d && Math.hypot(e.clientX - d.px, e.clientY - d.py) < 4) setSelected(null)
            }}
            onPointerCancel={() => {
              drag.current = null
              setPanning(false)
            }}
          >
          <defs>
            {/* Direction survives, but as a small neutral head rather than a
                coloured arrow competing with the nodes. */}
            <marker
              id="mg-arr-ref"
              viewBox="0 0 10 10"
              refX="7.6"
              refY="5"
              markerWidth="7"
              markerHeight="7"
              markerUnits="userSpaceOnUse"
              orient="auto-start-reverse"
            >
              <path d="M 1 1.4 L 8.6 5 L 1 8.6 z" class="mg-arrow-ref" />
            </marker>
            <marker
              id="mg-arr-promo"
              viewBox="0 0 10 10"
              refX="7.6"
              refY="5"
              markerWidth="7"
              markerHeight="7"
              markerUnits="userSpaceOnUse"
              orient="auto-start-reverse"
            >
              <path d="M 1 1.4 L 8.6 5 L 1 8.6 z" class="mg-arrow-promo" />
            </marker>
          </defs>
          <g transform={`translate(${view.x} ${view.y}) scale(${view.k})`}>
            {/* One transparent plane is the pan/zoom target; no dot grid,
                vignette or cluster rings — the drawing is the content. */}
            <rect class="mg-bg" x={-size.w * 4} y={-size.h * 4} width={size.w * 9} height={size.h * 9} />
            {filtered.edges.map((e, i) => {
              const a = pos.get(e.from)
              const b = pos.get(e.to)
              if (!a || !b) return null
              const dx = b.x - a.x
              const dy = b.y - a.y
              const dist = Math.max(Math.hypot(dx, dy), 1)
              const ux = dx / dist
              const uy = dy / dist
              // Endpoints pull back to the actual dot rim; parallel edges
              // shift both ends along the perpendicular so they stay straight
              // and evenly spaced.
              const x1 = a.x + ux * (rimDistance(a, ux, uy) + 3)
              const y1 = a.y + uy * (rimDistance(a, ux, uy) + 3)
              const x2 = b.x - ux * (rimDistance(b, ux, uy) + 6)
              const y2 = b.y - uy * (rimDistance(b, ux, uy) + 6)
              const off = edgeSpread.get(i) ?? 0
              const nx = -uy * off
              const ny = ux * off
              const ax = x1 + nx
              const ay = y1 + ny
              const bx = x2 + nx
              const by = y2 + ny
              const d = `M ${ax} ${ay} L ${bx} ${by}`
              const lit = focus !== null && (e.from === focus || e.to === focus)
              const dim = focus !== null && !lit
              const lx = (ax + bx) / 2
              const ly = (ay + by) / 2
              const bw = 9 + String(e.weight ?? 0).length * 4.6
              const arrow = e.kind === 'promotion' ? 'url(#mg-arr-promo)' : e.kind === 'reference' ? 'url(#mg-arr-ref)' : undefined
              // Opacity is computed as one number so focus dimming cannot be
              // overridden by an inline "mention" opacity. A stray mention is
              // nearly invisible; a repeated one earns contrast.
              const mentionOpacity = Math.min(0.62, 0.22 + Math.sqrt(e.weight ?? 1) * 0.16)
              const opacity = dim
                ? 0.05
                : lit
                  ? 1
                  : e.kind === 'mention'
                    ? mentionOpacity
                    : e.kind === 'co'
                      ? 0.55
                      : 0.92
              return (
                <g
                  key={i}
                  class={`mg-edge-g${dim ? ' dim' : ''}${lit ? ' lit' : ''}`}
                  style={{ animationDelay: `${Math.min(i * 18, 400)}ms` }}
                >
                  <path
                    class={`mg-edge ${EDGE_CLASS[e.kind] ?? 'mg-edge-reference'}`}
                    style={{
                      strokeWidth: edgeWidth(e) + (lit ? 0.45 : 0),
                      opacity,
                    }}
                    d={d}
                    marker-end={arrow}
                  />
                  {/* Fat invisible stroke: a real hit target for the tooltip,
                      the thin visible line alone is hard to hover. */}
                  <path class="mg-edge-hit" d={d}>
                    <title>
                      {t(`memory.graph.${e.kind}`, e.kind)}
                      {(e.weight ?? 0) > 1 ? ` ×${e.weight}` : ''}
                    </title>
                  </path>
                  {(e.weight ?? 0) > 1 && (e.kind === 'promotion' || lit) && (
                    <g class="mg-badge">
                      <rect x={lx - bw / 2} y={ly - 6} width={bw} height={12} rx={6} />
                      <text class="mono" x={lx} y={ly + 3} text-anchor="middle">
                        ×{e.weight}
                      </text>
                    </g>
                  )}
                </g>
              )
            })}
            {filtered.nodes.map((n, i) => {
              const p = pos.get(n.id)!
              const dimmed = focus !== null && focus !== n.id && !neighbors.has(n.id)
              const r = nodeRadius(n)
              const alpha = recencyAlpha(n.last_seen, now)
              const labelVisible = showLabels || hover === n.id || selected === n.id
              const lines = labelLines(n.label)
              return (
                <g
                  key={n.id}
                  class={`mg-node ${KIND_CLASS[n.kind] ?? 'mg-core'}${dimmed ? ' dim' : ''}${
                    selected === n.id ? ' sel' : ''
                  }${n.status && n.status !== 'active' ? ' pending' : ''}`}
                  transform={`translate(${p.x},${p.y})`}
                  onMouseEnter={() => setHover(n.id)}
                  onMouseLeave={() => setHover((v) => (v === n.id ? null : v))}
                  onFocus={() => setHover(n.id)}
                  onBlur={() => setHover((v) => (v === n.id ? null : v))}
                  onClick={() => setSelected(n.id === selected ? null : n.id)}
                  role="button"
                  tabIndex={0}
                  aria-label={`${t(`memory.graph.${n.kind}`, n.kind)} ${n.label}`}
                  aria-pressed={selected === n.id}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault()
                      setSelected(n.id === selected ? null : n.id)
                    } else if (e.key === 'Escape') {
                      setSelected(null)
                    }
                  }}
                >
                  {/* Inner g carries the pop-in — animating transform on the
                      outer one would fight its translate() attribute. */}
                  <g class="mg-pop" style={{ animationDelay: `${Math.min(i * 30, 520)}ms` }}>
                    {selected === n.id && <circle class="mg-halo" r={r + 6} />}
                    <circle class="mg-hit" r={r + 10} />
                    {n.status && n.status !== 'active' && <circle class="mg-ring" r={r + 4.5} />}
                    <circle class="mg-dot" r={r} opacity={alpha} />
                    {/* Every caption sits beneath its dot, centred. One
                        consistent baseline is what makes a dense graph read
                        as a list of names instead of scattered annotation. */}
                    {labelVisible && (
                      <text class="mg-label" y={r + 13} text-anchor="middle">
                        {lines.length === 1 ? (
                          lines[0]
                        ) : (
                          lines.map((ln, li) => (
                            <tspan key={li} x={0} dy={li === 0 ? 0 : 12}>
                              {ln}
                            </tspan>
                          ))
                        )}
                      </text>
                    )}
                  </g>
                </g>
              )
            })}
          </g>
          </svg>
          {/* Pan/zoom hint lives on the canvas, bottom-left: it is a gesture
              legend for the drawing, not a control in the toolbar. */}
          <span class="mg-hint-chip">{t('memory.graph.hint')}</span>
          {sel && (
            <div class={`mg-detail card mg-detail-${sel.kind}`}>
              <div class="mg-detail-head">
                <span class={`mg-kind-chip ${KIND_CLASS[sel.kind] ?? 'mg-core'}`}>
                  <i />
                  {sel.category ? t(`memory.category.${sel.category}`) : t(`memory.graph.${sel.kind}`, sel.kind)}
                </span>
                <strong class="mono">{sel.label}</strong>
                {sel.status && <span class="badge yellow">{sel.status}</span>}
                <button type="button" class="icon-btn" onClick={() => setSelected(null)} aria-label={t('common.close')}>
                  ×
                </button>
              </div>
              {(sel.entries || sel.files || sel.chars || sel.weight) && (
                <div class="mg-detail-stats dim">
                  {sel.entries ? <span>{t('memory.graph.entriesN', { n: sel.entries })}</span> : null}
                  {sel.files ? <span>{t('memory.graph.filesN', { n: sel.files })}</span> : null}
                  {sel.chars ? <span>{t('memory.graph.charsN', { n: sel.chars })}</span> : null}
                  {sel.weight && isEntity(sel) ? <span>{t('memory.graph.mentionsN', { n: sel.weight })}</span> : null}
                </div>
              )}
              {(sel.first_seen || sel.last_seen) && (
                <div class="mg-detail-stats dim">
                  {sel.first_seen ? (
                    <span>
                      {t('memory.graph.firstSeen')} {sel.first_seen}
                    </span>
                  ) : null}
                  {sel.last_seen ? (
                    <span>
                      {t('memory.graph.lastSeen')} {sel.last_seen}
                    </span>
                  ) : null}
                </div>
              )}
              {isEntity(sel) && (sel.evidence?.length ?? 0) > 0 && (
                <div class="mg-evidence">
                  <span class="dim">{t('memory.graph.evidence')}</span>
                  <ul>
                    {sel.evidence!.map((s, i) => (
                      <li key={i}>{s}</li>
                    ))}
                  </ul>
                </div>
              )}
              {selEdges.length > 0 ? (
                <ul class="mg-detail-edges">
                  {selEdges.map((e, i) => {
                    const other = e.from === sel.id ? e.to : e.from
                    return (
                      <li key={i}>
                        <span class="dim">{e.kind === 'co' ? '·' : e.from === sel.id ? '→' : '←'}</span>
                        <button type="button" class="mg-neighbor mono" onClick={() => setSelected(other)}>
                          {labelOf(other)}
                        </button>
                        <span class="dim">
                          · {t(`memory.graph.${e.kind}`, e.kind)}
                          {(e.weight ?? 0) > 1 ? ` ×${e.weight}` : ''}
                        </span>
                      </li>
                    )
                  })}
                </ul>
              ) : (
                <p class="dim">{t('memory.graph.noEdges')}</p>
              )}
              {onOpen && sel.kind !== ENTITY_KIND && (
                <button class="btn small" onClick={() => onOpen(sel)}>
                  {t('memory.graph.open')}
                </button>
              )}
            </div>
          )}
        </div>
      )}
      {steps.length > 1 && (
        <div class="mg-timeline">
          <button
            type="button"
            class="icon-btn mg-play"
            aria-label={playing ? t('memory.graph.pause') : t('memory.graph.play')}
            aria-pressed={playing}
            onClick={() => setPlaying((p) => !p)}
          >
            {playing ? (
              <svg viewBox="0 0 10 12" aria-hidden="true">
                <rect x="1.4" y="1" width="2.7" height="10" rx="1" />
                <rect x="5.9" y="1" width="2.7" height="10" rx="1" />
              </svg>
            ) : (
              <svg viewBox="0 0 10 12" aria-hidden="true">
                <path d="M 1.6 1 L 8.8 6 L 1.6 11 z" />
              </svg>
            )}
          </button>
          <input
            class="mg-range"
            type="range"
            min={0}
            max={steps.length - 1}
            step={1}
            value={stepIdx}
            aria-label={t('memory.graph.timeline')}
            style={{
              background: `linear-gradient(90deg, var(--accent) ${(stepIdx / Math.max(1, steps.length - 1)) * 100}%, var(--bg-inset) ${(stepIdx / Math.max(1, steps.length - 1)) * 100}%)`,
            }}
            onInput={(e) => {
              setPlaying(false)
              const i = Number((e.target as HTMLInputElement).value)
              setUntil(i >= steps.length - 1 ? '' : (steps[i] ?? ''))
            }}
          />
          <span class="mg-date mono dim">{effectiveUntil}</span>
        </div>
      )}
      <div class="mg-legend">
        {chips.map((k) => (
          <button
            key={k}
            type="button"
            class={`mg-legend-item ${KIND_CLASS[k] ?? 'mg-core'}${hiddenKinds.has(k) ? ' off' : ''}`}
            aria-pressed={!hiddenKinds.has(k)}
            onClick={() => toggleKind(k)}
          >
            <i /> {t(`memory.graph.${k}`, k)}
          </button>
        ))}
        {edgeLegend.map((k) => (
          <button
            key={k}
            type="button"
            class={`mg-legend-item mg-legend-edge-${k}${hiddenEdges.has(k) ? ' off' : ''}`}
            aria-pressed={!hiddenEdges.has(k)}
            onClick={() => toggleEdge(k)}
          >
            <svg width="22" height="8">
              <line x1="0" y1="4" x2="22" y2="4" />
            </svg>
            {t(`memory.graph.${k}`)}
          </button>
        ))}
      </div>
    </div>
  )
}

/** Edge thickness follows evidence, but stays a hairline: a repeated mention
 *  or several promotions draw a slightly heavier line than a stray one. In
 *  Obsidian's graph even the strongest link never becomes a thick band. */
function edgeWidth(e: MemoryGraphEdge): number {
  const base = e.kind === 'promotion' ? 1.25 : e.kind === 'mention' ? 0.75 : e.kind === 'co' ? 0.7 : 0.95
  const bonus = (Math.sqrt(e.weight ?? 1) - 1) * 0.42
  return Math.min(2.4, base + bonus)
}

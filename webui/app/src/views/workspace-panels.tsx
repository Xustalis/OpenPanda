// SPDX-License-Identifier: AGPL-3.0-or-later

import { type JSX } from 'preact'
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'preact/hooks'
import { DeviceGlyph, type Form } from '../components/device-glyph'
import { ContextMenu, type ContextMenuItem } from '../components/context-menu'
import { agentLogo } from '../format/agent-logo'
import { agentInitial } from '../format/agent-role'
import { buildWorkspaceEdges, workspaceLinkLine, type PanelRect } from './workspace-links'

/** 工作空间 — 设备面板视图.
 *
 *  One panel per machine, one tile per agent the machine advertises. This
 *  replaces the always-on topology map: the question this page answers first
 *  is "what is running where", and a panel of agent marks says that at a
 *  glance. Selecting a panel then reveals only that machine's live connections,
 *  so the graph is available without becoming the page's permanent subject.
 *
 *  The panel is the machine (name, OS, load, online), the tile is the agent
 *  (the CLI's own mark, and whether it is working right now). No agent names,
 *  no task text on the tiles: the drawer has all of that.
 *
 *  A tile is also the unit the reader acts on. A click picks it (and a second
 *  click puts it back), several tiles can be picked at once, and a right-click
 *  opens the card's own menu — pick, test the connection, remove it from the
 *  view. The menu is a list of items, not bespoke markup, so a new verb is one
 *  more entry at the call site. The panel keeps its own click: it is the
 *  machine, and the drawer is where its facts live.
 *
 *  The panda stands on the local machine's panel and does not travel. CSS
 *  places it inside that panel (`.ws-panel.self .ws-panda`), so it never
 *  needs the layout measured: a pet that walked between panels had to be
 *  positioned by script on every re-flow, and the one thing it reliably did
 *  was end up in the gap between two of them. It keeps its breathing bob and
 *  puts its arms up while this machine is holding work.
 *
 *  Panels can be dragged anywhere on the surface, and they stay where they
 *  are put. The arrangement is a preference, not fleet state, so it lives in
 *  localStorage and never travels to the node.
 *
 *  The first paint is the tidy one: the panels render in a wrapping flex row,
 *  are measured in a layout effect, and are frozen into absolute coordinates
 *  at exactly the pixels they already occupy. Nothing moves on load, and from
 *  then on a drag is a move rather than a reorder — a fleet of one has
 *  nothing to reorder, and a drag that puts a card back where it was is a
 *  drag that did not work. */

/** States that mean "this task is holding the machine right now". Mirrors
 *  workspace.tsx's RUNNING_STATES; duplicated rather than imported because
 *  that module imports this one. */
const RUNNING = new Set(['dispatched', 'waiting_context', 'running'])

/** Where the reader's own arrangement of the fleet is kept. */
const POS_KEY = 'openpanda.workspace.pos'
/** Pixels of travel before a press becomes a drag. Below this a press is a
 *  click, and a click opens the drawer. */
const DRAG_SLOP = 6

export interface PanelAgent {
  tier?: number
  best_at?: string[]
}

export interface PanelTask {
  id: string
  state: string
  last_event?: { data?: unknown }
}

/** The slice of a workspace Device this view reads. Declared structurally so
 *  the view owns no import cycle with views/workspace.tsx. */
export interface PanelDevice {
  id: string
  name: string
  form: string
  os: string
  online: boolean
  local: boolean
  /** Routing-graph adjacency and this console's own measurement toward the
   *  peer. Together they describe every connection the canvas may draw. */
  neighbors: string[]
  rtt: number
  transport: string
  tasks: number
  maxTasks: number
  busy: boolean
  agents: Record<string, PanelAgent>
  work: PanelTask[]
}

export interface WorkspacePanelsProps {
  devices: PanelDevice[]
  selected: string | null
  onPick: (id: string) => void
  /** "WINDOWS 11 · 笔记本" — the parent owns the vocabulary. */
  meta: (d: PanelDevice) => string
  /** Fallback roster for the local machine when its capability card is bare:
   *  the CLIs this console can actually drive. */
  fallbackAgents?: string[]
  /** Picked agent tiles, keyed by `tileKey`. */
  pickedTiles: Set<string>
  /** Tiles removed from view, keyed the same way. */
  hiddenTiles: Set<string>
  /** Tiles with a connectivity test in flight. */
  testingTiles: Set<string>
  onTogglePick: (key: string) => void
  onTest: (device: string, agent: string) => void
  onHide: (device: string, agent: string) => void
  /** Put every hidden tile of one machine back. */
  onShowAll: (device: string) => void
  /** "已隐藏 2 个智能体" — the note left where a panel's tiles used to be. */
  hiddenNote: (n: number) => string
  /** Card menu vocabulary. */
  menuSelect: string
  menuDeselect: string
  menuTest: string
  menuRemove: string
  /** The reason a remote tile's connection cannot be tested from here. */
  menuLocalOnly: string
  /** Appended to a tile's tooltip: the right-click is the only hint the menu
   *  gets, and a card that never says so is a card nobody right-clicks. */
  menuHint: string
  localLabel: string
  onlineLabel: string
  offlineLabel: string
  meshLabel: string
  measuredLabel: string
  /** Names the card an edge has to pass over, `{name}` standing in for the
   *  machine. The edge is dimmed rather than dropped: "this box and I are not
   *  connected" is a worse lie than a faint line. */
  occludedLabel: string
  emptyLabel: string
  idleLabel: string
  runningLabel: string
  /** A machine that advertises no agents at all. */
  noneLabel: string
  /** Bumped by the parent's 整理布局 control. Every increment throws the
   *  saved arrangement away and re-forms the tidy row: the one guaranteed way
   *  back from a card parked somewhere the reader cannot see. */
  resetSignal?: number
}

/** One agent tile's identity: an agent lives on a machine, so the machine is
 *  part of the key. The same CLI on two boxes is two tiles, two picks, and
 *  two menu targets — a name alone would conflate them. */
export function tileKey(device: string, agent: string): string {
  return `${device}/${agent}`
}

/** The roster a panel shows: what the machine's card declares, or — for this
 *  machine only — the CLIs the console can actually drive when the card is
 *  bare. Exported because the page's batch actions have to know the same set
 *  the panels drew. */
export function panelAgents(d: PanelDevice, fallback: string[] = []): string[] {
  const roster = Object.keys(d.agents ?? {})
  if (roster.length > 0) return roster
  return d.local ? fallback : []
}

/** The agent a task's newest event names, if it names one. The ledger stamps
 *  `last_event` on every row, and exec/model events carry `data.agent` — that
 *  is the only per-agent attribution the console has, so it is the one the
 *  tiles run on. A task with no attribution leaves its machine's tiles idle
 *  rather than guessing. */
function agentOf(task: PanelTask): string {
  const data = task.last_event?.data
  if (data && typeof data === 'object') {
    const a = (data as { agent?: unknown }).agent
    return typeof a === 'string' ? a.toLowerCase() : ''
  }
  if (typeof data === 'string') {
    const m = /"agent"\s*:\s*"([^"]+)"/.exec(data)
    return m?.[1] ? m[1].toLowerCase() : ''
  }
  return ''
}

function runningOf(d: PanelDevice): Set<string> {
  const out = new Set<string>()
  for (const t of d.work) {
    if (!RUNNING.has(t.state)) continue
    const a = agentOf(t)
    if (a) out.add(a)
  }
  return out
}

/** One panel's place on the surface, in the grid's own coordinates. */
interface Slot {
  x: number
  y: number
  w: number
  h: number
}

interface LinkLayout {
  rects: Map<string, PanelRect>
  width: number
  height: number
}

function sameLinkLayout(a: LinkLayout, b: LinkLayout): boolean {
  if (a.width !== b.width || a.height !== b.height || a.rects.size !== b.rects.size) return false
  for (const [id, r] of b.rects) {
    const prev = a.rects.get(id)
    if (!prev || prev.x !== r.x || prev.y !== r.y || prev.w !== r.w || prev.h !== r.h) return false
  }
  return true
}

function readSlots(): Record<string, Slot> {
  try {
    const raw = localStorage.getItem(POS_KEY)
    const v = raw ? (JSON.parse(raw) as unknown) : null
    if (!v || typeof v !== 'object') return {}
    const out: Record<string, Slot> = {}
    for (const [id, s] of Object.entries(v as Record<string, unknown>)) {
      const o = s as Partial<Slot> | null
      if (!o || typeof o.x !== 'number' || typeof o.y !== 'number') continue
      out[id] = {
        x: o.x,
        y: o.y,
        w: typeof o.w === 'number' ? o.w : 320,
        h: typeof o.h === 'number' ? o.h : 180,
      }
    }
    return out
  } catch {
    // A blocked localStorage (private mode, a locked-down profile) must not
    // cost the page its layout: the fleet simply re-forms on every visit.
    return {}
  }
}

function writeSlots(slots: Record<string, Slot>, live: Set<string>): void {
  try {
    const kept: Record<string, Slot> = {}
    for (const [id, s] of Object.entries(slots)) if (live.has(id)) kept[id] = s
    localStorage.setItem(POS_KEY, JSON.stringify(kept))
  } catch {
    /* preference not saved — the arrangement still holds for this visit */
  }
}

/** The ancestor that actually scrolls the surface. `.ws-panels-grid` never
 *  does — `.ws-map` does — so reading `scrollLeft`/`scrollTop` off the grid
 *  returned 0 and a card dropped on a scrolled surface landed somewhere else
 *  entirely. */
function scrollerOf(el: HTMLElement | null): HTMLElement | null {
  for (let n = el?.parentElement ?? null; n; n = n.parentElement) {
    const oy = getComputedStyle(n).overflowY
    if (oy === 'auto' || oy === 'scroll') return n
  }
  return null
}

/** The box a pointer can actually reach, in the grid's own coordinates: the
 *  scroller's visible window shifted by the current scroll offset. A card
 *  dropped outside it used to be written to localStorage at a y the reader
 *  could not scroll to, which is how a panel vanished for good. */
function reachableBand(
  grid: HTMLElement,
  view: HTMLElement | null,
  inset: number,
): { x0: number; x1: number; y0: number; y1: number } {
  const gr = grid.getBoundingClientRect()
  const vr = view?.getBoundingClientRect() ?? gr
  const ox = view?.scrollLeft ?? 0
  const oy = view?.scrollTop ?? 0
  return {
    x0: Math.max(0, vr.left + inset - gr.left + ox),
    x1: Math.max(0, vr.right - inset - gr.left + ox),
    y0: Math.max(0, vr.top + inset - gr.top + oy),
    y1: Math.max(0, vr.bottom - inset - gr.top + oy),
  }
}

/** Pin a panel's top-left into the reachable band: the result is the nearest
 *  position in [lo, hi] that keeps a `size`-long panel inside it, so a card
 *  dropped past an edge lands flush against it rather than out of sight. */
function clampInto(v: number, lo: number, hi: number, size: number): number {
  const top = Math.min(v, Math.max(lo, hi - size))
  return Math.max(lo, top)
}

interface Press {
  id: string
  x: number
  y: number
  grabX: number
  grabY: number
  w: number
  h: number
}

interface Drag extends Press {
  /** Live pointer position. */
  x: number
  y: number
}

export function WorkspacePanels(props: WorkspacePanelsProps) {
  const {
    devices,
    selected,
    onPick,
    meta,
    fallbackAgents,
    pickedTiles,
    hiddenTiles,
    testingTiles,
    onTogglePick,
    onTest,
    onHide,
    onShowAll,
    hiddenNote,
    menuSelect,
    menuDeselect,
    menuTest,
    menuRemove,
    menuLocalOnly,
    menuHint,
    localLabel,
    onlineLabel,
    offlineLabel,
    meshLabel,
    measuredLabel,
    occludedLabel,
    emptyLabel,
    idleLabel,
    runningLabel,
    noneLabel,
  } = props

  const [slots, setSlots] = useState<Record<string, Slot>>(readSlots)
  const [drag, setDrag] = useState<Drag | null>(null)
  /** The card a right-click landed on, and where. Coordinates are viewport
   *  pixels because the menu is fixed: it must not scroll with the map. */
  const [menu, setMenu] = useState<{
    x: number
    y: number
    device: PanelDevice
    agent: string
  } | null>(null)
  const dragRef = useRef<Drag | null>(null)
  const press = useRef<Press | null>(null)
  /** A press that turned into a drag must not also count as a click. */
  const dragged = useRef(false)
  const els = useRef(new Map<string, HTMLElement>())
  const gridRef = useRef<HTMLDivElement>(null)
  const ghostRef = useRef<HTMLElement | null>(null)
  const [linkLayout, setLinkLayout] = useState<LinkLayout>({
    rects: new Map(),
    width: 0,
    height: 0,
  })

  const edges = useMemo(() => buildWorkspaceEdges(devices), [devices])
  const visibleEdges = useMemo(
    () => (selected ? edges.filter((e) => e.a === selected || e.b === selected) : []),
    [edges, selected],
  )
  const linkedIds = useMemo(() => {
    const ids = new Set<string>()
    for (const e of visibleEdges) {
      ids.add(e.a)
      ids.add(e.b)
    }
    return ids
  }, [visibleEdges])
  const deviceOf = useMemo(() => new Map(devices.map((d) => [d.id, d])), [devices])
  /** Rect → device id, so an occluded edge can name the card it passes behind.
   *  Keyed on object identity: the layout hands out one rect object per card
   *  per measurement and `workspaceLinkLine` reports that same object. */
  const idByRect = useMemo(() => {
    const byRect = new Map<PanelRect, string>()
    for (const [id, r] of linkLayout.rects) byRect.set(r, id)
    return byRect
  }, [linkLayout.rects])

  const applyDrag = (next: Drag | null) => {
    dragRef.current = next
    setDrag(next)
  }

  /** Measure only the endpoints the current selection can draw. Keeping the
   *  rectangles in grid coordinates lets the SVG scroll with the cards; the
   *  floating drag copy is viewport-fixed and is converted into the same
   *  frame here. */
  const measureLinks = () => {
    const grid = gridRef.current
    const next: LinkLayout = { rects: new Map(), width: 0, height: 0 }
    if (grid) {
      const gr = grid.getBoundingClientRect()
      // Every pane, not just the endpoints of the visible edges: a line can
      // pass behind a pane the current selection does not connect, and
      // occlusion is exactly that question. Measuring only the endpoints made
      // an unconnected pane unable to block anything.
      const ids = new Set<string>(els.current.keys())
      for (const e of visibleEdges) {
        ids.add(e.a)
        ids.add(e.b)
      }
      for (const id of ids) {
        const el = dragRef.current?.id === id ? ghostRef.current : els.current.get(id)
        if (!el) continue
        const r = el.getBoundingClientRect()
        next.rects.set(id, {
          x: r.left - gr.left + grid.scrollLeft,
          y: r.top - gr.top + grid.scrollTop,
          w: r.width,
          h: r.height,
        })
      }
      next.width = Math.max(grid.clientWidth, grid.scrollWidth)
      next.height = Math.max(grid.clientHeight, grid.scrollHeight)
    }
    setLinkLayout((prev) => (sameLinkLayout(prev, next) ? prev : next))
  }

  // Layout changes that do not resize a card still move its endpoints: a
  // drag, a restored slot, and the first flex-to-absolute pass all land here.
  useLayoutEffect(() => {
    measureLinks()
  })

  useEffect(() => {
    if (visibleEdges.length === 0) return
    const grid = gridRef.current
    if (!grid) return
    const observer = new ResizeObserver(measureLinks)
    observer.observe(grid)
    for (const id of linkedIds) {
      const el = els.current.get(id)
      if (el) observer.observe(el)
    }
    window.addEventListener('resize', measureLinks)
    return () => {
      observer.disconnect()
      window.removeEventListener('resize', measureLinks)
    }
  }, [visibleEdges, linkedIds])

  // The drag is owned by the window, not by the panel: the panel unmounts the
  // moment it becomes the floating copy, and a pointer captured on a detached
  // node stops reporting.
  const dragging = drag !== null
  useEffect(() => {
    if (!dragging) return
    const move = (e: PointerEvent) => {
      const cur = dragRef.current
      if (!cur) return
      // The button came up somewhere we never heard about (a released press
      // that started a drag, a window that lost focus): stop following the
      // pointer rather than leaving the card stuck to it.
      if (e.buttons === 0) {
        cancel()
        return
      }
      applyDrag({ ...cur, x: e.clientX, y: e.clientY })
    }
    const cancel = () => {
      press.current = null
      applyDrag(null)
    }
    const end = () => {
      const cur = dragRef.current
      press.current = null
      if (!cur) return
      const grid = gridRef.current
      if (grid) {
        const view = scrollerOf(grid)
        // Viewport coordinates back into the grid's own frame. The scroll
        // offset comes from the surface, not from the grid (which never
        // scrolls), and the result is pinned to the band the pointer can
        // reach so a card can never be parked out of sight.
        const gr = grid.getBoundingClientRect()
        const band = reachableBand(grid, view, 8)
        const rawX = cur.x - cur.grabX - gr.left + (view?.scrollLeft ?? 0)
        const rawY = cur.y - cur.grabY - gr.top + (view?.scrollTop ?? 0)
        const x = clampInto(rawX, band.x0, band.x1, cur.w)
        const y = clampInto(rawY, band.y0, band.y1, cur.h)
        setSlots((prev) => {
          const next = { ...prev, [cur.id]: { x, y, w: cur.w, h: cur.h } }
          writeSlots(next, new Set(devices.map((d) => d.id)))
          return next
        })
      }
      applyDrag(null)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', end)
    window.addEventListener('pointercancel', cancel)
    return () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', end)
      window.removeEventListener('pointercancel', cancel)
    }
  }, [dragging, devices])

  // Escape puts the panel back where it was: a drag that cannot be abandoned
  // is a trap on a surface with no undo.
  useEffect(() => {
    if (!dragging) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      press.current = null
      applyDrag(null)
    }
    document.addEventListener('keydown', onKey, true)
    return () => document.removeEventListener('keydown', onKey, true)
  }, [dragging])

  // Re-form the tidy row on request. The saved arrangement is a preference,
  // not state, so forgetting it is a complete recovery.
  const resetSignal = props.resetSignal ?? 0
  useEffect(() => {
    if (!resetSignal) return
    try {
      localStorage.removeItem(POS_KEY)
    } catch {
      /* a blocked localStorage has nothing saved to forget */
    }
    setSlots({})
    applyDrag(null)
  }, [resetSignal])

  // Freeze the tidy layout. The panels render in a wrapping row first; this
  // measures the pixels they landed on and turns them into coordinates, so
  // the first paint is unchanged and every later frame is absolute. A machine
  // that arrives after that has no row to land in, so it goes under the
  // lowest card already on the surface.
  useLayoutEffect(() => {
    const grid = gridRef.current
    if (!grid || dragRef.current) return
    // A saved arrangement outlives the window it was made in. Pull every
    // stored card back inside the surface that exists now — a narrower
    // window, or a card stored before the band clamp existed, otherwise
    // leaves a panel parked past the right edge with no way back.
    const maxX = Math.max(0, grid.clientWidth)
    const repaired: Record<string, Slot> = {}
    let changed = false
    for (const [id, s] of Object.entries(slots)) {
      // A y this far down predates the band clamp and has no scroll extent
      // under it; dropping the entry re-forms the row instead of leaving a
      // card nobody can reach.
      if (!Number.isFinite(s.y) || s.y < 0 || s.y > 20_000) {
        changed = true
        continue
      }
      const x = Math.min(Math.max(0, s.x), Math.max(0, maxX - s.w))
      if (x !== s.x) changed = true
      repaired[id] = { ...s, x }
    }
    if (changed) {
      setSlots(repaired)
      writeSlots(repaired, new Set(devices.map((d) => d.id)))
    }
    const missing = devices.filter((d) => !repaired[d.id])
    if (missing.length === 0) return
    const gr = grid.getBoundingClientRect()
    const next = { ...repaired }
    // The first pass is the tidy row and is measured as drawn. A machine that
    // turns up later has no row to be part of — the rest of the fleet is
    // already pinned — so it goes under the lowest card instead.
    const firstPass = Object.keys(repaired).length === 0
    let floor = 0
    for (const s of Object.values(repaired)) floor = Math.max(floor, s.y + s.h)
    for (const d of missing) {
      const el = els.current.get(d.id)
      if (!el) continue
      const r = el.getBoundingClientRect()
      next[d.id] = firstPass
        ? { x: r.left - gr.left, y: r.top - gr.top, w: r.width, h: r.height }
        : { x: 0, y: floor + 18, w: r.width, h: r.height }
      floor = Math.max(floor, next[d.id]!.y + r.height)
    }
    if (Object.keys(next).length !== Object.keys(repaired).length) setSlots(next)
  })

  if (devices.length === 0) return <p class="dim ws-empty">{emptyLabel}</p>

  // One panda, on one card. `is_local` can name more than one directory row
  // (the same machine re-registered under a new runtime id, or a row filed
  // under its hostname beside its node id), and a pet on two cards stops
  // meaning "this is the machine you are on". The fleet is sorted local-first,
  // so the first local row is the live one.
  const pandaId = devices.find((d) => d.local)?.id ?? ''

  const bottom = Object.values(slots).reduce((m, s) => Math.max(m, s.y + s.h), 0)

  const panel = (d: PanelDevice, ghost?: Drag) => {
    const run = runningOf(d)
    const roster = panelAgents(d, fallbackAgents)
    const agents = roster.filter((cli) => !hiddenTiles.has(tileKey(d.id, cli)))
    const hiddenHere = roster.length - agents.length
    const working = run.size > 0 || d.busy
    const slot = slots[d.id]
    const style: JSX.CSSProperties | undefined = ghost
      ? {
          position: 'fixed',
          left: `${ghost.x - ghost.grabX}px`,
          top: `${ghost.y - ghost.grabY}px`,
          width: `${ghost.w}px`,
          height: `${ghost.h}px`,
          margin: 0,
        }
      : slot
        ? { position: 'absolute', left: `${slot.x}px`, top: `${slot.y}px`, width: `${slot.w}px` }
        : undefined
    return (
      <section
        key={d.id}
        class={`ws-panel${d.online ? '' : ' off'}${d.local ? ' self' : ''}${
          selected === d.id ? ' sel' : ''
        }${linkedIds.has(d.id) ? ' linked' : ''}${ghost ? ' dragging' : ''}`}
        data-form={d.form}
        data-device={d.id}
        style={style}
        ref={
          ghost
            ? (el: HTMLElement | null) => {
                ghostRef.current = el
              }
            : (el: HTMLElement | null) => {
                if (el) els.current.set(d.id, el)
                else els.current.delete(d.id)
              }
        }
        onClick={() => {
          if (dragged.current) {
            dragged.current = false
            return
          }
          onPick(d.id)
        }}
        onPointerDown={
          ghost
            ? undefined
            : (e) => {
                if (e.button !== 0) return
                // A tile is its own control (it opens the drawer for its
                // agent); a press there is a click, never a drag.
                if ((e.target as Element).closest('.ws-tile')) return
                const el = e.currentTarget as HTMLElement
                const r = el.getBoundingClientRect()
                press.current = {
                  id: d.id,
                  x: e.clientX,
                  y: e.clientY,
                  grabX: e.clientX - r.left,
                  grabY: e.clientY - r.top,
                  w: r.width,
                  h: r.height,
                }
                dragged.current = false
              }
        }
        onPointerMove={
          ghost
            ? undefined
            : (e) => {
                const p = press.current
                if (!p || p.id !== d.id || dragRef.current) return
                // No button, no drag. A press that has already ended must not
                // be able to start one later — that is how a plain click used
                // to leave the card glued to the pointer.
                if (e.buttons === 0) {
                  press.current = null
                  return
                }
                if (Math.abs(e.clientX - p.x) + Math.abs(e.clientY - p.y) < DRAG_SLOP) return
                dragged.current = true
                applyDrag({ ...p, x: e.clientX, y: e.clientY })
              }
        }
        onPointerUp={ghost ? undefined : () => (press.current = null)}
        onPointerCancel={ghost ? undefined : () => (press.current = null)}
        onPointerLeave={ghost ? undefined : () => (press.current = null)}
        role="button"
        tabIndex={0}
        aria-label={`${d.name} · ${meta(d)} · ${d.online ? onlineLabel : offlineLabel}`}
        aria-pressed={selected === d.id}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            onPick(d.id)
          }
        }}
      >
        {/* The pet, standing on this panel's top edge. Decoration: the live
            facts are the tiles and the load readout beside them. */}
        {d.id === pandaId && (
          <div class={`ws-panda${working ? ' cheer' : ''}`} aria-hidden="true">
            <span class="ws-panda-sp" />
          </div>
        )}

        <header class="ws-panel-hd">
          <span class="ws-panel-glyph">
            <svg viewBox="-14 -14 28 28" aria-hidden="true">
              <DeviceGlyph form={d.form as Form} />
            </svg>
          </span>
          <span class="ws-panel-name">{d.name}</span>
          <span class="ws-panel-meta">{meta(d)}</span>
          {d.local && <span class="ws-panel-tag">{localLabel}</span>}
          <span class="ws-panel-load mono">
            {d.tasks}/{d.maxTasks}
          </span>
          <span
            class={`ws-panel-dot${d.online ? '' : ' off'}${d.busy ? ' busy' : ''}`}
            aria-hidden="true"
          />
          <span class="ws-panel-state">{d.online ? onlineLabel : offlineLabel}</span>
        </header>

        <div class={`ws-panel-bays${agents.length === 0 ? ' none' : ''}`}>
          {agents.length === 0 ? (
            hiddenHere > 0 ? (
              // The panel is not empty, it is folded: a machine whose every
              // tile was removed would otherwise read as "declares no agents",
              // which is a different — and wrong — statement.
              <button type="button" class="ws-panel-restore" onClick={() => onShowAll(d.id)}>
                {hiddenNote(hiddenHere)}
              </button>
            ) : (
              <span class="ws-panel-none">{noneLabel}</span>
            )
          ) : (
            agents.map((cli) => {
              const on = run.has(cli.toLowerCase())
              const key = tileKey(d.id, cli)
              const picked = pickedTiles.has(key)
              const probing = testingTiles.has(key)
              // The mark carries the plate it is drawn for (`plate` in
              // format/agent-logo): near-black artwork only reads on a light
              // plate, near-white artwork only on a dark one. Same rule as the
              // roster avatars, so an agent looks identical wherever it shows.
              const logo = agentLogo(cli)
              const state = on ? runningLabel : idleLabel
              return (
                <button
                  key={cli}
                  type="button"
                  class="ws-tile"
                  data-run={on ? '1' : '0'}
                  data-picked={picked ? '1' : '0'}
                  data-testing={probing ? '1' : '0'}
                  title={`${cli} · ${state} · ${menuHint}`}
                  aria-label={`${cli} · ${state}`}
                  aria-pressed={picked}
                  onClick={(e) => {
                    e.stopPropagation()
                    onTogglePick(key)
                  }}
                  onKeyDown={(e) => {
                    // The tile is its own control, so the keys that activate a
                    // button must do what a click on it does. Without this the
                    // event bubbles to the panel's own Enter/Space handler,
                    // which opens the device drawer: the same control then
                    // behaved one way under the pointer and another under the
                    // keyboard. Only the keys a button answers to are claimed —
                    // Escape and the menu's own keys stay with the page.
                    if (e.key !== 'Enter' && e.key !== ' ') return
                    e.preventDefault()
                    e.stopPropagation()
                    onTogglePick(key)
                  }}
                  onContextMenu={(e) => {
                    // The card's verbs live behind this; the browser's own
                    // menu would cover them and say nothing about the agent.
                    e.preventDefault()
                    e.stopPropagation()
                    setMenu({ x: e.clientX, y: e.clientY, device: d, agent: cli })
                  }}
                >
                  <span class={`ws-tile-plate${logo ? ` plate-${logo.plate}` : ''}`}>
                    {logo ? (
                      <img
                        src={`/agents/${logo.file}`}
                        alt=""
                        draggable={false}
                        onError={(e) => {
                          ;(e.currentTarget as HTMLImageElement).style.visibility = 'hidden'
                        }}
                      />
                    ) : (
                      <span class="ws-tile-fb">{agentInitial(cli)}</span>
                    )}
                    {on && <span class="ws-tile-ring" aria-hidden="true" />}
                  </span>
                  {!on && <span class="ws-tile-dot" aria-hidden="true" />}
                </button>
              )
            })
          )}
        </div>
      </section>
    )
  }

  const menuItems: ContextMenuItem[] = menu
    ? [
        {
          id: 'pick',
          label: pickedTiles.has(tileKey(menu.device.id, menu.agent)) ? menuDeselect : menuSelect,
          icon: pickedTiles.has(tileKey(menu.device.id, menu.agent)) ? 'x' : 'check',
          onSelect: () => onTogglePick(tileKey(menu.device.id, menu.agent)),
        },
        {
          // A connectivity check is `<cli> --version` on this console's own
          // machine: a peer's CLI is not ours to spawn, so the verb is there
          // but greyed with the reason rather than silently testing the wrong
          // box. New verbs join this array — the menu itself is data.
          id: 'test',
          label: menuTest,
          icon: 'plug',
          hint: menu.device.local ? '--version' : menuLocalOnly,
          disabled: !menu.device.local,
          onSelect: () => onTest(menu.device.id, menu.agent),
        },
        {
          id: 'remove',
          label: menuRemove,
          icon: 'trash',
          danger: true,
          onSelect: () => onHide(menu.device.id, menu.agent),
        },
      ]
    : []

  return (
    <div class={`ws-panels${drag ? ' is-dragging' : ''}`}>
      <div
        class="ws-panels-grid"
        ref={gridRef}
        style={bottom > 0 ? { minHeight: `${bottom + 26}px` } : undefined}
      >
        {visibleEdges.length > 0 && linkLayout.width > 0 && linkLayout.height > 0 && (
          <svg
            class="ws-panel-links"
            width={linkLayout.width}
            height={linkLayout.height}
            viewBox={`0 0 ${linkLayout.width} ${linkLayout.height}`}
            aria-hidden="true"
          >
            {visibleEdges.map((e) => {
              const a = linkLayout.rects.get(e.a)
              const b = linkLayout.rects.get(e.b)
              if (!a || !b) return null
              const line = workspaceLinkLine(a, b, linkLayout.rects.values())
              if (!line) return null
              // `blockedBy` is a layout object, not an SVG attribute — spread
              // only the geometry onto the elements.
              const geometry = { x1: line.x1, y1: line.y1, x2: line.x2, y2: line.y2 }
              const names = `${deviceOf.get(e.a)?.name ?? e.a} ↔ ${deviceOf.get(e.b)?.name ?? e.b}`
              const blockerId = line.blockedBy ? idByRect.get(line.blockedBy) : undefined
              const blockerName = blockerId ? deviceOf.get(blockerId)?.name ?? blockerId : ''
              const title = [
                names,
                e.kind === 'mesh' ? meshLabel : measuredLabel,
                e.rtt > 0 ? `${e.rtt} ms` : '',
                e.transport,
                blockerName ? occludedLabel.replaceAll('{name}', blockerName) : '',
              ]
                .filter(Boolean)
                .join(' · ')
              return (
                <g
                  class={`ws-panel-link-group ${e.kind}${blockerName ? ' occluded' : ''}`}
                  key={`${e.a}-${e.b}`}
                >
                  <line class="ws-panel-link-halo" {...geometry} />
                  <line class={`ws-panel-link ${e.kind}`} {...geometry} />
                  <circle class="ws-panel-link-dot" cx={line.x1} cy={line.y1} r={3} />
                  <circle class="ws-panel-link-dot" cx={line.x2} cy={line.y2} r={3} />
                  <line class="ws-panel-link-hit" {...geometry}>
                    <title>{title}</title>
                  </line>
                </g>
              )
            })}
          </svg>
        )}
        {devices.filter((d) => !drag || d.id !== drag.id).map((d) => panel(d))}
      </div>
      {drag && devices.some((d) => d.id === drag.id) && panel(devices.find((d) => d.id === drag.id)!, drag)}
      {menu && (
        <ContextMenu
          x={menu.x}
          y={menu.y}
          title={menu.agent}
          sub={menu.device.name}
          items={menuItems}
          onClose={() => setMenu(null)}
        />
      )}
    </div>
  )
}

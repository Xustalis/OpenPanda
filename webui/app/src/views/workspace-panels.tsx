// SPDX-License-Identifier: AGPL-3.0-or-later

import { type JSX } from 'preact'
import { useEffect, useLayoutEffect, useRef, useState } from 'preact/hooks'
import { DeviceGlyph, type Form } from '../components/device-glyph'
import { agentLogo } from '../format/agent-logo'
import { agentInitial } from '../format/agent-role'

/** 工作空间 — 设备面板视图.
 *
 *  One panel per machine, one tile per agent the machine advertises. This
 *  replaces the topology map: the question this page answers is "what is
 *  running where", and a panel of agent marks says that at a glance, where a
 *  ring of plates said "what is connected to what".
 *
 *  The panel is the machine (name, OS, load, online), the tile is the agent
 *  (the CLI's own mark, and whether it is working right now). No agent names,
 *  no task text on the tiles: the drawer has all of that.
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
  localLabel: string
  onlineLabel: string
  offlineLabel: string
  emptyLabel: string
  idleLabel: string
  runningLabel: string
  /** A machine that advertises no agents at all. */
  noneLabel: string
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
    localLabel,
    onlineLabel,
    offlineLabel,
    emptyLabel,
    idleLabel,
    runningLabel,
    noneLabel,
  } = props

  const [slots, setSlots] = useState<Record<string, Slot>>(readSlots)
  const [drag, setDrag] = useState<Drag | null>(null)
  const dragRef = useRef<Drag | null>(null)
  const press = useRef<Press | null>(null)
  /** A press that turned into a drag must not also count as a click. */
  const dragged = useRef(false)
  const els = useRef(new Map<string, HTMLElement>())
  const gridRef = useRef<HTMLDivElement>(null)

  const applyDrag = (next: Drag | null) => {
    dragRef.current = next
    setDrag(next)
  }

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
        // Viewport coordinates back into the grid's own frame: the surface
        // scrolls, so its scroll offset is part of the answer.
        const gr = grid.getBoundingClientRect()
        const x = Math.max(0, cur.x - cur.grabX - gr.left + grid.scrollLeft)
        const y = Math.max(0, cur.y - cur.grabY - gr.top + grid.scrollTop)
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

  // Freeze the tidy layout. The panels render in a wrapping row first; this
  // measures the pixels they landed on and turns them into coordinates, so
  // the first paint is unchanged and every later frame is absolute. A machine
  // that arrives after that has no row to land in, so it goes under the
  // lowest card already on the surface.
  useLayoutEffect(() => {
    const grid = gridRef.current
    if (!grid || dragRef.current) return
    const missing = devices.filter((d) => !slots[d.id])
    if (missing.length === 0) return
    const gr = grid.getBoundingClientRect()
    const next = { ...slots }
    // The first pass is the tidy row and is measured as drawn. A machine that
    // turns up later has no row to be part of — the rest of the fleet is
    // already pinned — so it goes under the lowest card instead.
    const firstPass = Object.keys(slots).length === 0
    let floor = 0
    for (const s of Object.values(slots)) floor = Math.max(floor, s.y + s.h)
    for (const d of missing) {
      const el = els.current.get(d.id)
      if (!el) continue
      const r = el.getBoundingClientRect()
      next[d.id] = firstPass
        ? { x: r.left - gr.left, y: r.top - gr.top, w: r.width, h: r.height }
        : { x: 0, y: floor + 18, w: r.width, h: r.height }
      floor = Math.max(floor, next[d.id]!.y + r.height)
    }
    if (Object.keys(next).length !== Object.keys(slots).length) setSlots(next)
  })

  if (devices.length === 0) return <p class="dim ws-empty">{emptyLabel}</p>

  const bottom = Object.values(slots).reduce((m, s) => Math.max(m, s.y + s.h), 0)

  const panel = (d: PanelDevice, ghost?: Drag) => {
    const run = runningOf(d)
    const roster = Object.keys(d.agents ?? {})
    const agents = roster.length > 0 ? roster : d.local ? fallbackAgents ?? [] : []
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
        }${ghost ? ' dragging' : ''}`}
        data-form={d.form}
        data-device={d.id}
        style={style}
        ref={
          ghost
            ? undefined
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
        {d.local && (
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
            <span class="ws-panel-none">{noneLabel}</span>
          ) : (
            agents.map((cli) => {
              const on = run.has(cli.toLowerCase())
              // The mark carries the plate it is drawn for (`plate` in
              // format/agent-logo): near-black artwork only reads on a light
              // plate, near-white artwork only on a dark one. Same rule as the
              // roster avatars, so an agent looks identical wherever it shows.
              const logo = agentLogo(cli)
              return (
                <button
                  key={cli}
                  type="button"
                  class="ws-tile"
                  data-run={on ? '1' : '0'}
                  title={`${cli} · ${on ? runningLabel : idleLabel}`}
                  aria-label={`${cli} · ${on ? runningLabel : idleLabel}`}
                  onClick={(e) => {
                    e.stopPropagation()
                    onPick(d.id)
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

  return (
    <div class={`ws-panels${drag ? ' is-dragging' : ''}`}>
      <div
        class="ws-panels-grid"
        ref={gridRef}
        style={bottom > 0 ? { minHeight: `${bottom + 26}px` } : undefined}
      >
        {devices.filter((d) => !drag || d.id !== drag.id).map((d) => panel(d))}
      </div>
      {drag && devices.some((d) => d.id === drag.id) && panel(devices.find((d) => d.id === drag.id)!, drag)}
    </div>
  )
}

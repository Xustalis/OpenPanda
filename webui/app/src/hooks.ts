import { useEffect, useRef, useState } from 'preact/hooks'
import { subscribeEvents } from './api/client.ts'
import type { ChangeEvent, Task, TraceEvent } from './api/client.ts'
import { onLocaleChange } from './i18n/index.ts'

/** Re-render on locale change. */
export function useLocaleRerender(): void {
  const [, force] = useState(0)
  useEffect(() => onLocaleChange(() => force((v) => v + 1)), [])
}

/** Async data with reload; `deps` re-fetch like useEffect deps. `reloadKey`
 *  lets callers force a refresh (e.g. on an SSE change signal). */
export function useAsync<T>(fn: () => Promise<T>, deps: unknown[], reloadKey = 0) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<string | null>(null)
  const fnRef = useRef(fn)
  fnRef.current = fn

  useEffect(() => {
    let alive = true
    fnRef
      .current()
      .then((v) => alive && (setData(v), setError(null)))
      .catch((e: unknown) => alive && setError(e instanceof Error ? e.message : String(e)))
    return () => {
      alive = false
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, reloadKey])

  return { data, error }
}

// — Unified event bus. ONE fetch-SSE connection to /api/events?trace=1 is
//   shared by the entire app (the server pushes both change and trace frames
//   on a ?trace=1 stream), fanned out to two listener sets. The old design
//   gave every useChangeSignal() its own EventSource — N views meant N+1
//   connections and N tokens in URLs. Reference-counted with a grace period:
//   hooks mount/unmount on every view switch, and tearing down a long-lived
//   stream only to re-dial it a tick later reads as churn on the wire, so
//   the connection is only closed once the bus stays empty for BUS_GRACE_MS.
//   Authentication is header-based (openSSE), so no token ever enters a URL.
type ChangeListener = (ev: ChangeEvent) => void
type TraceListener = (ev: TraceEvent) => void

const changeListeners = new Set<ChangeListener>()
const traceListeners = new Set<TraceListener>()

/** How long an idle bus waits for a new subscriber before really closing. */
export const BUS_GRACE_MS = 30_000

let busStop: AbortController | null = null
let busGraceTimer: ReturnType<typeof setTimeout> | null = null

function startBusIfNeeded(): void {
  if (busGraceTimer !== null) {
    clearTimeout(busGraceTimer)
    busGraceTimer = null
  }
  if (busStop) return
  const ctrl = new AbortController()
  busStop = ctrl
  // subscribeEvents reconnects internally (exponential backoff); its promise
  // only settles when we abort it or the token is revoked. Either way the bus
  // slot frees itself, and the next subscriber re-dials.
  subscribeEvents({
    trace: true,
    signal: ctrl.signal,
    onChange: (ev) => {
      // Copy-on-iterate: a listener unsubscribing mid-fan-out must not skip
      // or double-visit its neighbors.
      for (const l of [...changeListeners]) l(ev)
    },
    onTrace: (ev) => {
      for (const l of [...traceListeners]) l(ev)
    },
  }).finally(() => {
    if (busStop === ctrl) busStop = null
  })
}

function maybeReleaseBus(): void {
  if (changeListeners.size + traceListeners.size > 0) return
  if (busGraceTimer !== null || !busStop) return
  busGraceTimer = setTimeout(() => {
    busGraceTimer = null
    if (changeListeners.size + traceListeners.size === 0 && busStop) {
      busStop.abort()
      busStop = null
    }
  }, BUS_GRACE_MS)
}

/** Subscribe to the shared change feed. Returns an unsubscribe function.
 *  Re-subscribing during the grace period reuses the live connection. */
export function busSubscribeChange(fn: ChangeListener): () => void {
  changeListeners.add(fn)
  startBusIfNeeded()
  return () => {
    changeListeners.delete(fn)
    maybeReleaseBus()
  }
}

/** Subscribe to the shared trace feed. Returns an unsubscribe function. */
export function busSubscribeTrace(fn: TraceListener): () => void {
  traceListeners.add(fn)
  startBusIfNeeded()
  return () => {
    traceListeners.delete(fn)
    maybeReleaseBus()
  }
}

/** Test seam: a snapshot of the bus internals. */
export function __busState(): { open: boolean; change: number; trace: number } {
  return { open: busStop !== null, change: changeListeners.size, trace: traceListeners.size }
}

/** Subscribe to the panel's change feed over the shared bus. Returns the
 *  latest change counter — bump it into useAsync's reloadKey to re-fetch on
 *  change. The synthetic "reconnect" event bumps it too, which is the
 *  "refetch once after a dropped link" behavior. */
export function useChangeSignal(): number {
  const [tick, setTick] = useState(0)
  useEffect(() => busSubscribeChange(() => setTick((v) => v + 1)), [])
  return tick
}

/** setInterval that skips ticks while the tab is hidden and while the
 *  previous tick is still in flight — for polling loops with no reason to
 *  run when nobody is looking (the system view's update-status poll). The
 *  callback rides a ref, so callers may pass inline closures. */
export function useVisibleInterval(fn: () => void | Promise<unknown>, ms: number): void {
  const fnRef = useRef(fn)
  fnRef.current = fn
  useEffect(() => {
    let inflight = false
    const tick = () => {
      if (inflight) return
      if (typeof document !== 'undefined' && document.hidden) return
      inflight = true
      Promise.resolve()
        .then(() => fnRef.current())
        .catch(() => {})
        .finally(() => {
          inflight = false
        })
    }
    const id = setInterval(tick, ms)
    return () => clearInterval(id)
  }, [ms])
}

/**
 * Tail the live trace stream for a single task id. Merges frames coming in
 * over SSE with the historical traces supplied by the caller (typically the
 * traces array already shipped inside Task.traces from getTask()). Returns a
 * deduplicated, ts-sorted array.
 *
 * Passing `undefined` taskId is safe — the hook just returns initialTraces
 * without subscribing, which matches the session chat view before a task is
 * actually created (empty orbit → collapsed placeholder).
 */
export function useTraceForTask(
  taskId: string | undefined,
  initialTraces: TraceEvent[] = [],
): TraceEvent[] {
  // Key the dedup map on trace id (the DB row id). If an SSE frame has id 0
  // (legacy / synthetic) we fall back to (ts,type,data-json) as the key.
  const [traces, setTraces] = useState<TraceEvent[]>(() =>
    Array.isArray(initialTraces) ? [...initialTraces] : [],
  )

  // Re-sync initialTraces when they actually change (e.g. getTask() resolves
  // after the hook's first render). Shallow compare by length + last id to
  // avoid thrashing every tick.
  const lastInit = useRef<{ n: number; lastId: number }>({ n: 0, lastId: 0 })
  useEffect(() => {
    const arr = Array.isArray(initialTraces) ? initialTraces : []
    const tail = arr.length ? arr[arr.length - 1]?.id ?? 0 : 0
    if (arr.length === lastInit.current.n && tail === lastInit.current.lastId) return
    lastInit.current = { n: arr.length, lastId: tail }
    setTraces((cur) => mergeTraces(cur, arr))
  }, [initialTraces])

  useEffect(() => {
    if (!taskId) return
    return busSubscribeTrace((ev) => {
      if (ev.task_id !== taskId) return
      setTraces((cur) => mergeTraces(cur, [ev]))
    })
  }, [taskId])

  return traces
}

/** Merge two trace lists, dedup on id, stable sorted by (ts,id). */
function mergeTraces(a: TraceEvent[], b: TraceEvent[]): TraceEvent[] {
  const seen = new Set<number>()
  const out: TraceEvent[] = []
  for (const e of [...a, ...b]) {
    if (!e) continue
    const k = e.id || hashTraceKey(e)
    if (seen.has(k)) continue
    seen.add(k)
    out.push(e)
  }
  out.sort((x, y) => (x.ts ?? 0) - (y.ts ?? 0) || (x.id ?? 0) - (y.id ?? 0))
  return out
}

function hashTraceKey(e: TraceEvent): number {
  let h = (e.ts ?? 0) | 0
  const s = `${e.type ?? ''}${typeof e.data === 'string' ? e.data : JSON.stringify(e.data ?? '')}`
  // DJB2-ish small hash — this only needs to deduplicate re-sent frames, not
  // survive adversarial input.
  for (let i = 0; i < s.length; i++) h = ((h << 5) + h + s.charCodeAt(i)) | 0
  return h
}

/** Derive the orbit's 4-step phase state from a trace set. Classify→Route→Exec
 *  →Done map to [0..3]. Returns `null` if nothing has happened yet (caller
 *  renders the collapsed brand-pill without a highlighted phase chip). */
export function orbitPhaseFromTraces(
  traces: TraceEvent[],
  taskState?: Task['state'],
): 0 | 1 | 2 | 3 | null {
  if (!traces?.length) {
    // If traces are empty but the task is terminated, we still paint Done so
    // the orbit never shows "no phase" for finished work.
    if (isTerminal(taskState)) return 3
    return null
  }
  let phase = 0 as 0 | 1 | 2 | 3
  for (const t of traces) {
    if (!t) continue
    switch (t.type) {
      case 'classify_result':
        phase = Math.max(phase, 0) as 0 | 1 | 2 | 3
        break
      case 'route_decision':
      case 'delegation_hop':
      case 'tier2_triggered':
        phase = Math.max(phase, 1) as 0 | 1 | 2 | 3
        break
      case 'exec_agent_start':
      case 'plan_stage_changed':
      case 'supervision_round':
      case 'artifact_transfer':
        phase = Math.max(phase, 2) as 0 | 1 | 2 | 3
        break
    }
  }
  if (phase === 2 && isTerminal(taskState)) phase = 3
  return phase
}

function isTerminal(s?: string): boolean {
  if (!s) return false
  switch (s) {
    case 'done':
    case 'completed':
    case 'success':
    case 'failed':
    case 'cancelled':
    case 'canceled':
    case 'rejected':
    case 'closed':
      return true
    default:
      return false
  }
}

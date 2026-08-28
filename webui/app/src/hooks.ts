import { useEffect, useRef, useState } from 'preact/hooks'
import {
  currentToken,
  subscribeEvents,
  TraceEvent,
  Task,
} from './api/client'
import { onLocaleChange } from './i18n'

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

/** Subscribe to the panel's SSE change feed. Returns the latest change
 *  counter — bump it into useAsync's reloadKey to re-fetch on change.
 *  EventSource cannot send an Authorization header, so the token rides
 *  along as ?token= (the panel accepts either). */
export function useChangeSignal(): number {
  const [tick, setTick] = useState(0)
  useEffect(() => {
    const es = new EventSource(`/api/events?token=${encodeURIComponent(currentToken())}`)
    es.addEventListener('change', () => setTick((v) => v + 1))
    es.onerror = () => {
      /* EventSource auto-reconnects; nothing to do */
    }
    return () => es.close()
  }, [])
  return tick
}

// — Global trace bus. Because subscribeEvents opens one EventSource for the
//   whole app, we fan out incoming trace frames to each per-task hook via a
//   simple list of listeners. Shared via a module-level mutable so two hooks
//   never open two /api/events?trace=1 streams.
type TraceListener = (ev: TraceEvent) => void
const traceListeners = new Set<TraceListener>()
let traceBusStarted = false

function startTraceBusIfNeeded(): void {
  if (traceBusStarted) return
  traceBusStarted = true
  // Swallow the promise: the bus is infinite by design. Reconnection is
  // handled inside subscribeEvents.
  subscribeEvents({
    trace: true,
    onTrace: (ev) => {
      for (const l of traceListeners) l(ev)
    },
  }).catch(() => {
    // If the bus itself dies (tab closure / dev-mode HMR) the next hook mount
    // will restart it with traceBusStarted reset below.
    traceBusStarted = false
  })
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
    startTraceBusIfNeeded()
    const listener: TraceListener = (ev) => {
      if (ev.task_id !== taskId) return
      setTraces((cur) => mergeTraces(cur, [ev]))
    }
    traceListeners.add(listener)
    return () => {
      traceListeners.delete(listener)
    }
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

import { useEffect, useRef, useState } from 'preact/hooks'
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
 *  counter — bump it into useAsync's reloadKey to re-fetch on change. */
export function useChangeSignal(): number {
  const [tick, setTick] = useState(0)
  useEffect(() => {
    const es = new EventSource('/api/events')
    es.addEventListener('change', () => setTick((v) => v + 1))
    es.onerror = () => {
      /* EventSource auto-reconnects; nothing to do */
    }
    return () => es.close()
  }, [])
  return tick
}

// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'preact/hooks'

import { ApiError } from '../api/client'

/** Private provider data must disappear on identity changes and failed reads.
 * A refresh never retains a previously readable draft after a revoked grant. */
export function useWorkRead<T>(fn: () => Promise<T>, deps: unknown[], tick = 0) {
  const key = JSON.stringify(deps)
  const [result, setResult] = useState<{ key: string; data: T | null; error: string | null; code?: string } | null>(null)
  const fnRef = useRef(fn)
  fnRef.current = fn
  useEffect(() => {
    let alive = true
    void fnRef.current().then(
      data => { if (alive) setResult({ key, data, error: null }) },
      error => { if (alive) setResult({ key, data: null, error: error instanceof Error ? error.message : String(error), code: error instanceof ApiError ? error.code : undefined }) },
    )
    return () => { alive = false }
  }, [key, tick])
  return result?.key === key ? result : { data: null, error: null }
}

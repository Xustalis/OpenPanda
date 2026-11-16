// SPDX-License-Identifier: AGPL-3.0-or-later

// This is a browser preference for the experimental console surface. Node
// execution/connection policies remain separate. Only an explicit opt-in
// enables it; legacy team preferences do not opt readers into the experiment.
export const TEAM_FEATURE_KEY = 'openpanda.experimental.tasks-collaboration'

const listeners = new Set<() => void>()
let sessionOverride: boolean | undefined

export function teamFeatureEnabled(): boolean {
  if (sessionOverride !== undefined) return sessionOverride
  try {
    return localStorage.getItem(TEAM_FEATURE_KEY) === 'true'
  } catch {
    return false
  }
}

export function setTeamFeatureEnabled(enabled: boolean): void {
  try {
    localStorage.setItem(TEAM_FEATURE_KEY, String(enabled))
    sessionOverride = undefined
  } catch {
    // Storage may be blocked or full; the switch still works for this page.
    sessionOverride = enabled
  }
  for (const fn of [...listeners]) fn()
}

/** Same-page updates and changes from another tab (including storage.clear). */
export function onTeamFeatureChange(fn: () => void): () => void {
  listeners.add(fn)
  const onStorage = (event: StorageEvent) => {
    if (event.key !== TEAM_FEATURE_KEY && event.key !== null) return
    sessionOverride = undefined
    fn()
  }
  if (typeof window !== 'undefined') window.addEventListener('storage', onStorage)
  return () => {
    listeners.delete(fn)
    if (typeof window !== 'undefined') window.removeEventListener('storage', onStorage)
  }
}

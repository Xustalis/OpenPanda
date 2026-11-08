// SPDX-License-Identifier: AGPL-3.0-or-later

// Opt-in console features. A feature that is still a placeholder (团队) must
// not occupy a rail row for every reader: the switch in 设置 → 偏好 → 团队 decides
// whether its destination, its ⌘K command and its deep link exist. Like the
// theme and locale choices this is a *browser* preference (localStorage), not
// node state — two people looking at the same panel may want different rail
// shapes, and no config.yaml write (or restart) is needed to flip it.
//
// The default is off: an unchecked feature is absent, never present-but-empty.

import { useEffect, useState } from 'preact/hooks'

const TEAM_KEY = 'openpanda.feature.team'

const listeners = new Set<() => void>()

function readTeam(): boolean {
  try {
    return localStorage.getItem(TEAM_KEY) === '1'
  } catch {
    // storage unavailable (private mode) — the feature just won't persist
    return false
  }
}

let teamOn = readTeam()

export function teamEnabled(): boolean {
  return teamOn
}

export function setTeamEnabled(next: boolean): void {
  if (next === teamOn) return
  teamOn = next
  try {
    localStorage.setItem(TEAM_KEY, next ? '1' : '0')
  } catch {
    // storage unavailable — the choice still applies until reload
  }
  listeners.forEach((fn) => fn())
}

/** Subscribe to switches (for the rail and the settings form). Returns
 *  unsubscribe. */
export function onTeamEnabledChange(fn: () => void): () => void {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

/** Re-render a component whenever the switch flips. */
export function useTeamEnabled(): boolean {
  const [on, setOn] = useState(teamEnabled)
  useEffect(() => onTeamEnabledChange(() => setOn(teamEnabled())), [])
  return on
}

// Another tab flipping the switch updates this one too, so a hidden rail row
// cannot come back as a stale render after an alt-tab.
if (typeof addEventListener !== 'undefined') {
  addEventListener('storage', (e: StorageEvent) => {
    if (e.key !== TEAM_KEY) return
    const next = e.newValue === '1'
    if (next === teamOn) return
    teamOn = next
    listeners.forEach((fn) => fn())
  })
}

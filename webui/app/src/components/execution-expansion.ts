// SPDX-License-Identifier: AGPL-3.0-or-later
export interface Expansion { open: boolean; rows: Record<string, boolean> }
const states = new Map<string, Expansion>()
export function expansion(scope?: string): Expansion {
  if (!scope) return { open: true, rows: {} }
  const cached = states.get(scope)
  if (cached) return cached
  try {
    const value = JSON.parse(sessionStorage.getItem('panda.steps.' + scope) ?? 'null')
    if (value && typeof value.open === 'boolean' && value.rows && typeof value.rows === 'object') {
      return { open: value.open, rows: Object.fromEntries(Object.entries(value.rows).filter(([, v]) => typeof v === 'boolean')) as Record<string, boolean> }
    }
  } catch { /* storage is optional */ }
  return { open: true, rows: {} }
}
export function rememberExpansion(scope: string | undefined, value: Expansion): Expansion {
  if (scope) {
    states.delete(scope)
    states.set(scope, value)
    if (states.size > 256) states.delete(states.keys().next().value!)
    try { sessionStorage.setItem('panda.steps.' + scope, JSON.stringify(value)) } catch { /* optional */ }
  }
  return value
}

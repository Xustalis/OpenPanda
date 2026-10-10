// SPDX-License-Identifier: AGPL-3.0-or-later

// Pure helpers behind the thread rail's organization: pinned-first ordering
// (the same shape the panel's List() emits, kept here so optimistic updates
// do not wait on a refetch to settle) and selection-set pruning when a
// refresh drops rows out from under the checkboxes.

import type { Session, SessionTurn } from '../api/client'

/** Pinned threads first, then newest activity first within each group.
 *  Returns a new array; the input list is left untouched. */
export function sortSessions(list: Session[]): Session[] {
  return [...list].sort((a, b) => {
    if (!!a.pinned !== !!b.pinned) return a.pinned ? -1 : 1
    return Date.parse(b.updated_at) - Date.parse(a.updated_at)
  })
}

/** Did the stored transcript actually move? Turns are append-only (plus
 *  whole-prefix compaction rewrites), so length + tail identity is a sound
 *  diff — and unlike updated_at it stays quiet when only title/pinned moved,
 *  sparing the pane a pointless transcript replace. */
export function turnsChanged(a: SessionTurn[] | undefined, b: SessionTurn[] | undefined): boolean {
  const al = a ?? []
  const bl = b ?? []
  if (al.length !== bl.length) return true
  const lastA = al[al.length - 1]
  const lastB = bl[bl.length - 1]
  if (!lastA && !lastB) return false
  return lastA?.text !== lastB?.text || lastA?.role !== lastB?.role
}

/** Drop selected ids that no longer exist in the fresh listing — a session
 *  deleted on another device must not linger as a ghost checkbox row. */
export function pruneSelection(selected: Set<string>, list: Session[]): Set<string> {
  const ids = new Set(list.map((s) => s.id))
  const next = new Set<string>()
  for (const id of selected) {
    if (ids.has(id)) next.add(id)
  }
  return next.size === selected.size ? selected : next
}

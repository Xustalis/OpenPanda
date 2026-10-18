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

/** What the thread's last word says about the run that produced it.
 *
 *  Two durable traces of a failure exist and nothing else does: a
 *  `kind: 'error'` turn when the ask itself broke (dead key, bad endpoint),
 *  and the `[failed] <reason>` digest the finalizer appends when a spawned
 *  task dies — lease expired, spawn error, rejected park. The session's own
 *  `operation` snapshot cannot answer this: it is written at dispatch time
 *  ("queued") and never learns the task's terminal state, so a task that
 *  failed an hour ago still reads as queued.
 *
 *  Only the tail counts. A thread that failed once and then answered fine is
 *  a working thread, and marking it would make the mark meaningless. */
export type ThreadOutcome = 'failed' | 'done' | ''

export function threadOutcome(s: Session): ThreadOutcome {
  const turns = s.turns ?? []
  const last = turns[turns.length - 1]
  if (!last || last.role !== 'assistant') return ''
  if (last.kind === 'error') return 'failed'
  const text = last.text ?? ''
  if (/^\[failed\]/i.test(text)) return 'failed'
  // A dispatch turn is the run still in flight — there is no outcome yet.
  if (last.kind === 'task') return ''
  // Cancelled, waiting on a human, or expired: none of them is a clean finish
  // and none of them is a failure worth interrupting the reader for.
  if (/^\[(cancelled|review|expired)\]/i.test(text)) return ''
  return 'done'
}

/** Identity of the thread's latest word, so "read" can be stamped against the
 *  exact run the reader looked at rather than against the thread id: a new
 *  failure must re-arm the mark even though the thread was read before. */
export function threadMarkKey(s: Session): string {
  const turns = s.turns ?? []
  const last = turns[turns.length - 1]
  if (!last) return ''
  // djb2 over the text: length alone would confuse two different failures that
  // happen to be the same size.
  const text = last.text ?? ''
  let h = 5381
  for (let i = 0; i < text.length; i++) h = ((h << 5) + h + text.charCodeAt(i)) | 0
  return `${turns.length}:${(h >>> 0).toString(36)}`
}

/** The mark a row should show: the outcome, unless this exact last word has
 *  already been read. The mark is a nudge, not a badge — reading the thread
 *  clears it, and the next run (success or failure) re-arms it. */
export function threadMark(s: Session, seen: Record<string, string>): ThreadOutcome {
  const outcome = threadOutcome(s)
  if (!outcome) return ''
  return seen[s.id] === threadMarkKey(s) ? '' : outcome
}

/** History grouped by working directory, in the order the rail — now the
 *  sidebar — shows it: a session with no project leads, then project groups
 *  ordered by the newest thread inside each. The grouping is what answers
 *  "what did I do in panda-w?" without a filter control. */
export function groupSessions(ordered: Session[]): Array<[string, Session[]]> {
  const groups = new Map<string, Session[]>()
  for (const s of ordered) {
    const key = s.project || ''
    const list = groups.get(key)
    if (list) list.push(s)
    else groups.set(key, [s])
  }
  return [...groups.entries()].sort(([, a], [, b]) => {
    const at = Date.parse(a[0]?.updated_at ?? '') || 0
    const bt = Date.parse(b[0]?.updated_at ?? '') || 0
    return bt - at
  })
}

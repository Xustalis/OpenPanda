// SPDX-License-Identifier: AGPL-3.0-or-later

/** One round, one bubble.
 *
 *  A round that fans out several `task_submit` calls is filed as one turn per
 *  task (webui/panel/sessions.go), because the transcript doubles as the
 *  model's history and it should see every job it handed out. On screen those
 *  turns are one statement — "I gave this to five agents" — so the view draws
 *  them as one reply: the first turn's face and clock, every card under it.
 *
 *  The grouping is pure list work, so it lives here rather than inside the
 *  view: `node --test` cannot import a .tsx file, and this rule has already
 *  been wrong once in a way no type would catch (a four-agent round drawn as
 *  three cards plus a second bubble, see `isBareDispatch`).
 */

/** The part of a turn this module reads. Structural, so the view's `ChatMsg`
 *  satisfies it without this file depending on the view. */
export interface BatchableTurn {
  /** Stable render key, unique within the thread. */
  k: string
  role: string
  kind?: string
  ref?: string
  streaming?: boolean
}

/** Whether a turn is nothing but a dispatch receipt.
 *
 *  A live result payload does NOT disqualify a dispatch turn. The round's own
 *  frame — the chain of thought, the measured duration, the receipt — lands on
 *  whichever bubble was streaming when the reply finished, and for a fan-out
 *  that is the LAST of the dispatch turns. Excluding it (the old `!m.result`
 *  rule) drew a four-agent round as three cards in one bubble and the fourth
 *  in a second bubble right underneath, which reads as two separate replies.
 *
 *  Only a turn still streaming stays out: that one is the round mid-flight,
 *  and it is not a receipt yet. */
export function isBareDispatch(m: BatchableTurn): boolean {
  return m.role === 'assistant' && m.kind === 'task' && Boolean(m.ref) && !m.streaming
}

/** Split a thread into dispatch runs.
 *
 *  Returns the sibling turns each run absorbed, keyed by the head's render key,
 *  plus the set of keys the view must not draw on its own. A run of one has no
 *  batch entry — it is already the bubble. */
export function groupDispatches<T extends BatchableTurn>(
  msgs: T[],
): { batchOf: Map<string, T[]>; absorbed: Set<string> } {
  const batchOf = new Map<string, T[]>()
  const absorbed = new Set<string>()
  for (let i = 0; i < msgs.length; i++) {
    if (!isBareDispatch(msgs[i]!)) continue
    const rest: T[] = []
    let j = i + 1
    while (j < msgs.length && isBareDispatch(msgs[j]!)) {
      rest.push(msgs[j]!)
      absorbed.add(msgs[j]!.k)
      j++
    }
    if (rest.length > 0) batchOf.set(msgs[i]!.k, rest)
    i = j - 1
  }
  return { batchOf, absorbed }
}

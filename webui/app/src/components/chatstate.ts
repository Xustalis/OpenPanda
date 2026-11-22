// SPDX-License-Identifier: AGPL-3.0-or-later

import { goalArgument } from './goal-command.ts'

// Pure state helpers for the chat composer, kept apart from the component so
// the streaming-target races that garbled transcripts can be tested head-on
// (see chatstate.test.ts).

/** Patch an identified assistant bubble. Never redirect a late frame into a
 * user message (or another operation) when its original target disappeared. */
export function patchStreaming<T extends { role?: string; streaming?: boolean; k?: string }>(
  msgs: T[],
  fn: (m: T) => T,
  key?: string,
): T[] {
  let idx = -1
  for (let i = msgs.length - 1; i >= 0; i--) {
    const m = msgs[i]!
    if (m.role === 'user') continue
    if (key ? m.k === key : m.streaming) { idx = i; break }
  }
  if (idx === -1) return msgs
  return msgs.map((m, i) => (i === idx ? fn(m) : m))
}

/** Locate the command being typed at the caret. A CJK phrase does not need
 * a separating space. Slashes inside paths and addresses belong to the text. */
export function slashAtCaret(input: string, caret: number) {
  const before = input.slice(0, caret)
  const match = /(?:^|[\s(（，。！？、；：]|[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}])\/([\p{L}\p{N}_:-]*)$/u.exec(before)
  if (!match) return null
  const query = match[1] ?? ''
  const start = before.length - query.length - 1
  const after = input.slice(caret)
  if (/^[./\\]/.test(after) || /^[\w-]+[./\\]/.test(after)) return null
  return { start, end: caret, query }
}

/** A plan prompt is distinct from the /plans navigation command. */
export function planArgument(input: string): string | null {
  const match = /^\/plan(?:\s+([\s\S]*))?$/i.exec(input.trim())
  return match ? (match[1] ?? '').trim() : null
}

/** The words a `/` query is matched against, one entry per word so the menu
 *  can rank on word starts (see rankWords): the label without its slash (the
 *  verb as displayed), the id's tail — `go:workspace` → "workspace", so
 *  `/workspace` works in any locale — and the palette's English keywords, so
 *  `/queue` still finds 队列. The id keeps its colons: `go:control:plans`
 *  stays one word, or every board would answer to `/pla`. */
export function slashWords(item: { id: string; label: string; alias?: string }): string[] {
  return [
    item.label.replace(/^\//, ''),
    item.id.replace(/^[^:]+:/, ''),
    ...(item.alias ?? '').split(/\s+/),
  ].filter(Boolean)
}

/** A composer mode armed from the command menu. Picking `/goal` or `/plan`
 *  takes the token out of the draft and leaves a chip beside the actions: the
 *  words typed next are the objective (or the plan request), not a command's
 *  argument, so there is no second place showing the same command. */
export type ComposerMode = 'goal' | 'plan'

/** What a send means. An armed chip owns the whole draft; without one the
 *  hand-typed `/goal …` / `/plan …` prefixes still parse, so the REPL's
 *  command syntax keeps working for anyone who types it out. */
export function composerIntent(armed: ComposerMode | null, typed: string): {
  goalText: string | null
  planText: string | null
} {
  if (armed === 'goal') return { goalText: typed, planText: null }
  if (armed === 'plan') return { goalText: null, planText: typed }
  return { goalText: goalArgument(typed), planText: planArgument(typed) }
}

/** A transcript turn, as far as reconciling the tail needs to know it. */
export interface TurnLike {
  role: 'user' | 'assistant'
  text: string
  kind?: string
  ref?: string
}

/** Fill the just-finished bubble in from its persisted turn.
 *
 *  The wire is not the transcript: a dispatched task's result frame carries
 *  the task id but no prose (the turn's text is written server-side as
 *  "已派发任务：…"), so a pane that trusts the stream alone ends up with an
 *  empty assistant bubble — no task card, no orbit, no link to the work. The
 *  persisted turn has all of it. Only the tail is reconciled (older turns are
 *  already server copies), and only when the server actually holds as many
 *  turns as the pane does: fewer means the bookkeeping write failed and the
 *  wire copy is all there is. Display-only fields the transcript never keeps
 *  (chain-of-thought, the streamed result) survive because the spread is
 *  applied to the local message.
 *
 *  A round can also persist MORE turns than the wire delivered: when the model
 *  fans out several `task_submit` calls, the engine enqueues every task but
 *  hands back a single receipt, and the panel files one dispatch turn per
 *  task. The pane holds one bubble for the round and the server holds one
 *  turn per task, so the rows in between are inserted ahead of the local tail
 *  — that is what used to leave one card in the thread over a board of three.
 *  `adopt` builds those rows in the pane's own shape (they need render keys
 *  the transcript has no notion of); without it, a surplus transcript is
 *  ignored and only the tail is reconciled. */
export function mergeTailTurn<T extends TurnLike>(
  msgs: T[],
  turns: TurnLike[],
  adopt?: (turn: TurnLike) => T,
): T[] {
  const last = turns[turns.length - 1]
  if (!last || last.role !== 'assistant') return msgs
  if (turns.length < msgs.length) return msgs
  const i = msgs.length - 1
  const m = msgs[i]
  if (!m || m.role !== 'assistant') return msgs
  const tail = {
    ...m,
    text: last.text || m.text,
    kind: last.kind || m.kind,
    ref: last.ref || m.ref,
  }
  const extra = turns.length - msgs.length
  if (extra === 0) return [...msgs.slice(0, i), tail]
  if (!adopt) return [...msgs.slice(0, i), tail]
  const inserted = turns.slice(turns.length - 1 - extra, turns.length - 1).map(adopt)
  return [...msgs.slice(0, i), ...inserted, tail]
}

/** Install persisted rows by identity, not by tail position. A task finalizer
 * may append a report before the ask refetch returns; that report must become
 * a new row, never replace the dispatch/user bubble carrying live metadata. */
export function reconcileTranscript<T extends TurnLike & { k: string; operation_id?: string; idempotency_key?: string }>(
  local: T[], stored: T[], retainOperation?: string,
): T[] {
  const used = new Set<string>()
  const reserved = new Set(local.map(m => m.k))
  const reconciled = stored.map(turn => {
    const match = local.find(m => !used.has(m.k) && m.role === turn.role && (
      m.idempotency_key && turn.idempotency_key
        ? m.idempotency_key === turn.idempotency_key
        : m.operation_id === turn.operation_id && (m.ref || '') === (turn.ref || '') &&
          (m.kind || 'answer') === (turn.kind || 'answer') && (turn.operation_id || m.text === turn.text)
    ))
    if (!match) {
      // An inserted row can be assigned the old position of a later row.
      // Keep that later row's DOM identity available to its actual owner.
      let key = turn.k
      let suffix = 0
      while (reserved.has(key)) key = turn.k + '-' + ++suffix
      reserved.add(key)
      return key === turn.k ? turn : { ...turn, k: key }
    }
    used.add(match.k)
    return { ...match, ...turn, k: match.k }
  })
  // A failed bookkeeping write must not erase the only streamed copy. This
  // exception is limited to the generation the server is reconciling.
  return [...reconciled, ...local.filter(m => retainOperation && m.operation_id === retainOperation &&
    !stored.some(t => t.operation_id === retainOperation && t.role === m.role))]
}

/** A reload drops the browser's local streaming flag, but the server keeps the
 *  running operation on the session. Count it as busy so the composer keeps
 *  showing Stop while the model is still answering. */
export function runningOperation(
  activeId: string | null,
  session: { id: string; operation?: { id: string; status: string } | null } | null,
): string | null {
  return session?.id === activeId && session.operation?.status === 'running'
    ? session.operation.id : null
}

/** Whether the thread is working, in the one sense the composer's primary
 *  action and the running placeholder promise.
 *
 *  Three inputs, and dropping any of them has already cost a way out of the
 *  pane: the browser's own stream (`localBusy`), the server's durable
 *  operation (a reload drops the first), and the jobs this thread dispatched
 *  that are still moving — a turn settles as soon as the handoff is made,
 *  while the employee it dispatched keeps running for minutes, and a stalled
 *  adapter is exactly the state that needs a Stop button. */
export function threadBusy(
  localBusy: boolean,
  activeId: string | null,
  session: { id: string; operation?: { id: string; status: string } | null } | null,
  liveTasks: number,
): boolean {
  return localBusy || Boolean(runningOperation(activeId, session)) || liveTasks > 0
}

// SPDX-License-Identifier: AGPL-3.0-or-later

// Pure state helpers for the chat composer, kept apart from the component so
// the streaming-target races that garbled transcripts can be tested head-on
// (see chatstate.test.ts).

/** Apply fn to the bubble the stream is actually writing to: the newest
 *  message still marked streaming — never blindly the last message. A
 *  transcript refetch racing the stream can leave a stale message at the
 *  tail, and a patch aimed there writes the reply into the user's own
 *  bubble. With nothing streaming (the closing patch after an error), fall
 *  back to the last message as before. */
export function patchStreaming<T extends { streaming?: boolean }>(
  msgs: T[],
  fn: (m: T) => T,
): T[] {
  if (msgs.length === 0) return msgs
  let idx = -1
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (msgs[i]?.streaming) {
      idx = i
      break
    }
  }
  if (idx === -1) idx = msgs.length - 1
  return msgs.map((m, i) => (i === idx ? fn(m) : m))
}

/** The composer's `/` completion query: the input opens the menu only when
 *  its first token starts with a slash and is still being typed. Returns the
 *  first token without the slash (possibly ""), or null when completion is
 *  not active. */
export function slashQuery(input: string): string | null {
  if (!input.startsWith('/')) return null
  const token = input.split(/\s/, 1)[0]?.slice(1) ?? ''
  return token
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
 *  applied to the local message. */
export function mergeTailTurn<T extends TurnLike>(msgs: T[], turns: TurnLike[]): T[] {
  const last = turns[turns.length - 1]
  if (!last || last.role !== 'assistant') return msgs
  if (turns.length < msgs.length) return msgs
  const i = msgs.length - 1
  const m = msgs[i]
  if (!m || m.role !== 'assistant') return msgs
  return [
    ...msgs.slice(0, i),
    {
      ...m,
      text: last.text || m.text,
      kind: last.kind || m.kind,
      ref: last.ref || m.ref,
    },
  ]
}

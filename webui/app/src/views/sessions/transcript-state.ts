// SPDX-License-Identifier: AGPL-3.0-or-later

import type { AskResult, AskStatusEvent, SessionTurn } from '../../api/client'
import { t } from '../../i18n'
import { displayUserTurn } from '../../components/turn-display'

/** Stable keys for chat bubbles. Server turns are keyed by transcript
 *  position (`srv-N`); locally created (optimistic / error) bubbles get a
 *  unique id from this counter so a streaming bubble is never re-keyed
 *  mid-flight when the list around it shifts. */
let nextLocalMsgId = 0
export function localMsgId(): string {
  nextLocalMsgId += 1
  return `opt-${nextLocalMsgId}`
}

/** A chat message in the transcript: a stored turn, or the in-flight
 * assistant reply being streamed right now. */
export interface ChatMsg extends SessionTurn {
  /** Stable render key — see `localMsgId` above. */
  k: string
  streaming?: boolean
  status?: string
  result?: AskResult
  /** Provider-supplied reasoning/summary, kept separate from answer replay. */
  thought?: string
  /** Newest delta/reasoning/usage/status timestamp (epoch ms). The live
   *  header uses it to tell a quiet turn from a stalled one. */
  lastActivity?: number
  /** The token figure is a live estimate, not the provider's count. The
   *  header prefixes it with "~" until the billed result lands. */
  tokensEstimated?: boolean
  /** @file paths that were expanded into fenced blocks for this prompt —
   *  shown as attachment chips on the user's bubble. */
  files?: string[]
  /** The task row this turn dispatched, adopted the moment the ask stream
   *  names it. It drives the live progress row while the turn is still
   *  waiting for the task to settle; `kind`/`ref` are only set once the
   *  result lands, which is far too late to show progress. */
  liveTask?: string
}

/** Install a server transcript as bubbles.
 *
 *  A stored user turn is the prompt the model read, not the sentence the
 *  composer showed: `sessions.go` files req.Prompt verbatim, and every @file
 *  rides along as a fenced copy of its contents. An unpurified reload — after
 *  a stop, a focus change, a change-feed update or a refresh — repainted that
 *  short bubble with the whole file (one real turn was 33k chars of a JVM
 *  classlist). Every path that adopts server turns funnels through here; the
 *  stored prompt itself is left alone so history still replays to the model. */
export function hydrateTurns(turns: SessionTurn[], key: (i: number) => string): ChatMsg[] {
  return turns.map((turn, i) => {
    const shown = turn.role === 'user' ? displayUserTurn(turn.text) : null
    return {
      ...turn,
      ...(shown
        ? { text: shown.text, files: shown.files.length ? shown.files : undefined }
        : {}),
      k: key(i),
    }
  })
}

// statusNote phrases one ask progress event for the composer status line.
// A retry milestone carries the numbers needed to say it in the browser's
// locale ("retrying in 3s · 2/6") instead of the server's English fallback;
// every other kind keeps the wire text.
export function statusNote(text: string, ev?: AskStatusEvent): string {
  if (ev?.kind === 'model') return t('sessions.modelWaiting', { name: ev.name || '' })
  if (ev?.kind === 'retry') {
    const reason = ev.http_status ? `HTTP ${ev.http_status}` : t('sessions.retry.transport')
    return t('sessions.retry', {
      reason,
      delay: Math.max(0, Math.round((ev.delay_ms ?? 0) / 1000)),
      attempt: ev.attempt ?? 0,
      max: ev.max ?? 0,
    })
  }
  if (ev?.kind === 'offline') {
    // An outage is not a provider failure: say the link is being watched and
    // that the turn picks itself back up, then report what the wait cost.
    const waited = Math.max(0, Math.round((ev.waited_ms ?? 0) / 1000))
    return ev.detail === 'resumed'
      ? t('sessions.offline.resumed', { delay: waited })
      : t('sessions.offline.waiting')
  }
  if (ev?.kind === 'spawn' && ev.name) {
    // The fan-out milestone in the browser's own language: the mode
    // descriptor says how the child relates to the shared tree — a private
    // copy that merges back, or the lanes it claims.
    if (ev.detail === 'isolated') return t('sessions.spawn.isolated', { name: ev.name })
    if (ev.detail) return t('sessions.spawn.scoped', { name: ev.name, paths: ev.detail })
    return t('sessions.spawn', { name: ev.name })
  }
  return text
}

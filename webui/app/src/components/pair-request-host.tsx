// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useRef, useState } from 'preact/hooks'
import { api, type PairRequest } from '../api/client'
import { busSubscribeChange, useVisibleInterval } from '../hooks'
import { t } from '../i18n'
import { Icon } from './icons'
import { PairInboundCard } from './pair-inbound'
import { formatLeft, requestLeft } from './pair-flow'
import { toast, toastError } from './toast'

/** Somebody knocked while you were reading a thread.
 *
 *  An inbound pairing request used to be visible only to whoever had
 *  工作空间 → 连接设备 open at that moment. The request lives four minutes; a
 *  prompt that surfaces nowhere is a prompt that expires unanswered, and the
 *  other human just sees "waiting for the other device" until it times out.
 *
 *  So this host sits at the root of the app, beside the toast and confirm
 *  hosts, and watches the pairing table for as long as the console is open.
 *  It is a card, not a modal: pairing takes one glance and one press, and
 *  covering the page someone is reading (or stealing their caret mid-sentence)
 *  to ask for it is a worse interruption than the request itself. Dismissing
 *  collapses it to a badge that keeps counting down, so "later" is possible
 *  without losing the request.
 *
 *  It also answers the case the plan started from: a node with no browser
 *  open at all. That one is covered off-console — the daemon logs the knock,
 *  the TUI raises a banner and `/pair`, and `panda status` says a request is
 *  waiting. This component only owns the third front end. */
export function PairRequestHost() {
  const [reqs, setReqs] = useState<PairRequest[]>([])
  const [arrivedAt, setArrivedAt] = useState(() => Date.now())
  const [now, setNow] = useState(() => Date.now())
  const [collapsed, setCollapsed] = useState(false)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  // Which request ids have already been shown. A *second* device knocking must
  // resurface the card even though the operator dismissed the first — the
  // dismiss said "not now" to that request, not "never again" to the feature.
  const seen = useRef<Set<string>>(new Set())

  const load = useCallback(async () => {
    try {
      const view = await api.pair()
      const live = view.requests.filter((r) => r.state === 'ready' || r.state === 'confirmed')
      setReqs(live)
      setArrivedAt(Date.now())
      setError('')
      if (live.some((r) => !seen.current.has(r.id))) {
        setCollapsed(false)
      }
      for (const r of live) seen.current.add(r.id)
    } catch (e) {
      // A console that cannot read the pairing table is a console with a
      // problem worth naming — silence here would look identical to "nobody
      // is asking", which is the exact confusion this component exists to
      // end. Only the open card shows it; the badge stays quiet.
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  // The change feed names the tables that moved, so a knock is delivered the
  // moment the daemon writes the row — no extra polling on an idle console.
  useEffect(() => busSubscribeChange((ev) => {
    if (ev.kinds.includes('pairs')) void load()
  }), [load])

  // The fallback beat. It also covers a dropped SSE connection (which the bus
  // silently re-establishes) and a request expiring on its own — nothing is
  // written when a row ages out, so no change event fires for it.
  useVisibleInterval(load, 5000)

  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [])

  async function answer(id: string, confirm: boolean) {
    if (busy) return
    setBusy(id)
    setError('')
    try {
      const res = await api.pairAnswer(id, confirm)
      toast(
        confirm
          ? res.state === 'done'
            ? t('nodes.pair.answerDone')
            : t('nodes.pair.confirmed')
          : t('nodes.pair.answerRejected'),
        confirm ? 'success' : 'info',
      )
    } catch (e) {
      toastError(e)
    } finally {
      setBusy('')
      void load()
    }
  }

  const req = reqs[0]
  if (!req) return null

  if (collapsed) {
    const left = formatLeft(requestLeft(req, arrivedAt, now))
    return (
      <button
        type="button"
        class="pair-req-badge"
        title={t('pairReq.reopen')}
        onClick={() => setCollapsed(false)}
      >
        <span class="pair-req-badge-dot" aria-hidden="true" />
        <span class="pair-req-badge-label">{t('pairReq.badge', { n: reqs.length })}</span>
        {left !== '' && <span class="pair-req-badge-clock mono">{left}</span>}
      </button>
    )
  }

  return (
    <aside class="pair-req" aria-label={t('pairReq.title')}>
      {/* The announcement, not the card. A live region wrapped around the
          card would re-read the whole thing every second the countdown
          ticks; this line changes only when the peer does. */}
      <p class="sr-only" role="status">
        {t('pairReq.announce', { name: req.peer_name || req.peer_addr })}
      </p>
      <div class="pair-req-head">
        <span class="pair-req-label">
          <span class="pair-req-dot" aria-hidden="true" />
          {t('pairReq.title')}
          {reqs.length > 1 && <span class="pair-req-more">{t('pairReq.more', { n: reqs.length - 1 })}</span>}
        </span>
        <button
          type="button"
          class="icon-btn pair-req-close"
          aria-label={t('pairReq.later')}
          title={t('pairReq.later')}
          onClick={() => setCollapsed(true)}
        >
          <Icon name="x" size={13} />
        </button>
      </div>
      <PairInboundCard
        req={req}
        busy={busy !== ''}
        pending={busy === req.id}
        left={formatLeft(requestLeft(req, arrivedAt, now))}
        onAnswer={(confirm) => answer(req.id, confirm)}
      />
      {error && <p class="pair-req-error">{error}</p>}
    </aside>
  )
}

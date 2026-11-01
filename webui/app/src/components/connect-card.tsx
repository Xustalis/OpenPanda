// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'preact/hooks'
import { api, type PairView } from '../api/client'
import { useAsync } from '../hooks'
import { t } from '../i18n'
import { isPeerAddr } from '../utils/peer-address'
import { Icon } from './icons'
import { DeviceSection } from './device-section'

/** Connecting another machine, in the two shapes the CLI offers: a pairing
 *  session started from LAN discovery (both screens show the same code), and
 *  an address written straight into the dial list.
 *
 *  One body, two shells. 节点网络 reads it as a card at the bottom of its aside;
 *  工作空间 hangs the very same body off its header, because "add a device"
 *  belongs next to the device list rather than on a page of its own. Keeping
 *  the form in one place is the point: the two surfaces cannot drift into
 *  asking for the address differently, and a fix to pairing lands on both. */

export function ConnectCard({ onChanged }: { onChanged(): void }) {
  return (
    <div class="card connect-card">
      <div class="card-head">
        <h2 class="block-title">{t('nodes.connect.title')}</h2>
      </div>
      <ConnectBody onChanged={onChanged} />
    </div>
  )
}

/** The popover shell: a title row with its own ×, then the same body.
 *  It hangs off a header corner like the device list and the orchestration
 *  card beside it, so it is dismissed the way those are — the trigger, the ×,
 *  or Escape. */
export function ConnectPanel({
  onChanged,
  onClose,
}: {
  onChanged(): void
  onClose(): void
}) {
  return (
    <div class="connect-panel">
      <div class="connect-panel-head">
        <h2 class="connect-panel-title">{t('nodes.connect.title')}</h2>
        <button
          type="button"
          class="icon-btn connect-panel-close"
          aria-label={t('common.close')}
          title={t('common.close')}
          onClick={onClose}
        >
          <Icon name="x" size={14} />
        </button>
      </div>
      <ConnectBody onChanged={onChanged} />
    </div>
  )
}

type ConnectTab = 'pair' | 'manual'

function ConnectBody({ onChanged }: { onChanged(): void }) {
  const [tick, setTick] = useState(0)
  const { data } = useAsync<PairView>(() => api.pair(), [], tick)
  const [tab, setTab] = useState<ConnectTab>('manual')
  const [manual, setManual] = useState('')
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')
  const [notice, setNotice] = useState('')
  const autoOpened = useRef(false)

  const waiting = (data?.outgoing ?? []).some((o) => o.state === 'waiting')
  const pendingReqs = data?.requests ?? []

  // While a session waits on the remote human — or a request sits
  // unanswered — poll so state flips show without a manual refresh.
  useEffect(() => {
    if (!waiting && pendingReqs.length === 0) return
    const id = setInterval(() => setTick((v) => v + 1), 2500)
    return () => clearInterval(id)
  }, [waiting, pendingReqs.length])

  // A request means somebody is standing at the other machine waiting for an
  // answer. Land on that tab once, without stealing the tab after the
  // operator has already chosen one.
  useEffect(() => {
    if (autoOpened.current || !data) return
    if (pendingReqs.length > 0) {
      autoOpened.current = true
      setTab('pair')
    }
  }, [data, pendingReqs.length])

  async function start(target: string) {
    if (busy) return
    setErr('')
    setNotice('')
    setBusy(target)
    try {
      await api.pairInitiate(target)
      if (manual.trim() === target) setManual('')
    } catch (e) {
      // Leave a typed address in place: an unreachable peer is usually a
      // typo or a daemon that is not up yet, and retyping host:port is the
      // wrong penalty for either.
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy('')
      setTick((v) => v + 1)
    }
  }

  async function answer(id: string, confirm: boolean) {
    if (busy) return
    setErr('')
    setNotice('')
    setBusy(id)
    try {
      const res = await api.pairAnswer(id, confirm)
      if (!confirm) {
        setNotice(t('nodes.pair.answerRejected'))
      } else if (res.state === 'done') {
        setNotice(t('nodes.pair.answerDone'))
      } else {
        setNotice(t('nodes.pair.confirmed'))
      }
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy('')
      setTick((v) => v + 1)
      onChanged()
    }
  }

  const discovered = data?.discovered ?? []
  const outgoing = data?.outgoing ?? []

  return (
    <div class="connect-body">
      <p class="connect-lead">{t('nodes.connect.lead')}</p>

      <div class="connect-tabs" role="tablist" aria-label={t('nodes.connect.title')}>
        <button
          class={`connect-tab${tab === 'pair' ? ' active' : ''}`}
          type="button"
          role="tab"
          aria-selected={tab === 'pair'}
          onClick={() => setTab('pair')}
        >
          <Icon name="wifi" size={14} />
          {t('nodes.connect.tab.pair')}
          {pendingReqs.length > 0 && (
            <span class="connect-tab-count" title={t('nodes.pair.requests')}>
              {pendingReqs.length}
            </span>
          )}
        </button>
        <button
          class={`connect-tab${tab === 'manual' ? ' active' : ''}`}
          type="button"
          role="tab"
          aria-selected={tab === 'manual'}
          onClick={() => setTab('manual')}
        >
          <Icon name="plus" size={14} />
          {t('nodes.connect.tab.manual')}
        </button>
      </div>

      {(err || notice) && (
        <p class={err ? 'node-remove-error connect-feedback' : 'connect-notice connect-feedback'}>
          {err || notice}
        </p>
      )}

      {tab === 'manual' ? (
        <div class="connect-pane" role="tabpanel">
          <DeviceSection onChanged={onChanged} />
        </div>
      ) : (
        <div class="connect-pane pair-pane" role="tabpanel">
          <div class="pair-hero">
            <span class="pair-hero-icon" aria-hidden="true">
              <Icon name="wifi" size={18} />
            </span>
            <div class="pair-hero-copy">
              <p class="pair-hero-title">{t('nodes.pair.heroTitle')}</p>
              <p class="pair-hero-hint">{t('nodes.pair.heroHint')}</p>
            </div>
          </div>
          {pendingReqs.length === 0 && outgoing.length === 0 && discovered.length === 0 && (
            <ol class="pair-steps">
              <li>
                <span class="pair-step-num">1</span>
                {t('nodes.pair.stepFind')}
              </li>
              <li>
                <span class="pair-step-num">2</span>
                {t('nodes.pair.stepCompare')}
              </li>
              <li>
                <span class="pair-step-num">3</span>
                {t('nodes.pair.stepConfirm')}
              </li>
            </ol>
          )}

          {pendingReqs.length > 0 && (
            <section class="pair-group">
              <p class="pair-group-title">{t('nodes.pair.requests')}</p>
              {pendingReqs.map((r) => (
                <div class="pair-item request" key={r.id}>
                  <div class="pair-item-top">
                    <span class="pair-name" title={r.peer_name || r.peer_addr}>
                      {r.peer_name || r.peer_addr}
                    </span>
                    {r.state === 'confirmed' ? (
                      <span class="dim pair-wait">
                        <span class="spinner spinner-inline" aria-hidden="true" />
                        {t('nodes.pair.joining')}
                      </span>
                    ) : (
                      <div class="pair-actions">
                        <button
                          class="btn small primary"
                          disabled={busy !== ''}
                          onClick={() => answer(r.id, true)}
                        >
                          {busy === r.id ? t('common.loading') : t('nodes.pair.confirm')}
                        </button>
                        <button
                          class="btn small danger"
                          disabled={busy !== ''}
                          onClick={() => answer(r.id, false)}
                        >
                          {t('nodes.pair.reject')}
                        </button>
                      </div>
                    )}
                  </div>
                  <div class="pair-item-sub">
                    <span class="pair-addr mono">{r.peer_addr}</span>
                    <span class="pair-code mono lg" title={t('nodes.pair.code')}>
                      {r.code}
                    </span>
                  </div>
                  <p class="pair-request-hint">{t('nodes.pair.requestHint')}</p>
                </div>
              ))}
            </section>
          )}

          {outgoing.length > 0 && (
            <section class="pair-group">
              <p class="pair-group-title">{t('nodes.pair.outgoing')}</p>
              {outgoing.map((o) => (
                <div class="pair-item" key={o.session}>
                  <div class="pair-item-top">
                    <span class="pair-name" title={o.target || o.addr}>
                      {o.target || o.addr}
                    </span>
                    {o.state === 'waiting' && (
                      <span class="dim pair-wait">
                        <span class="spinner spinner-inline" aria-hidden="true" />
                        {t('nodes.pair.waiting')}
                      </span>
                    )}
                    {o.state === 'done' && <span class="badge green">{t('nodes.pair.done')}</span>}
                    {o.state === 'rejected' && (
                      <span class="badge red" title={o.err}>
                        {t('nodes.pair.rejected')}
                      </span>
                    )}
                    {o.state === 'expired' && <span class="badge">{t('nodes.pair.expired')}</span>}
                    {o.state === 'error' && (
                      <span class="badge red" title={o.err}>
                        {t('nodes.pair.error')}
                        {o.err ? `: ${o.err}` : ''}
                      </span>
                    )}
                  </div>
                  <div class="pair-item-sub">
                    <span class="pair-addr mono">{o.addr}</span>
                    <span
                      class={`pair-code mono${o.state === 'waiting' ? ' lg' : ''}`}
                      title={t('nodes.pair.code')}
                    >
                      {o.code}
                    </span>
                  </div>
                </div>
              ))}
              {outgoing.some((o) => o.state === 'waiting') && (
                <p class="hint">{t('nodes.pair.compare')}</p>
              )}
            </section>
          )}

          <section class="pair-group">
            <p class="pair-group-title">{t('nodes.pair.discovered')}</p>
            {discovered.length === 0 ? (
              <>
                <div class="pair-empty">
                  <span class="pair-empty-icon" aria-hidden="true">
                    <Icon name="devices" size={17} />
                  </span>
                  <div class="pair-empty-copy">
                    <p class="pair-empty-title">{t('nodes.pair.emptyTitle')}</p>
                    <p class="dim">{t('nodes.pair.emptyHint')}</p>
                  </div>
                </div>
                <button
                  class="btn small pair-refresh"
                  type="button"
                  onClick={() => setTick((v) => v + 1)}
                >
                  <Icon name="refresh" size={12} />
                  {t('nodes.pair.refresh')}
                </button>
              </>
            ) : (
              discovered.map((d) => (
                <div class="pair-item" key={d.id}>
                  <div class="pair-item-top">
                    {/* Discovered, not paired: the dot stays neutral — green is
                        reserved for a node that is actually connected. */}
                    <span class="node-dot" aria-hidden="true" />
                    <span class="pair-name" title={d.id}>
                      {d.id}
                    </span>
                    {!d.verified && <span class="badge">{t('nodes.pair.unverified')}</span>}
                    <button class="btn small" disabled={busy !== ''} onClick={() => start(d.id)}>
                      {busy === d.id ? t('common.loading') : t('nodes.pair.start')}
                    </button>
                  </div>
                  <div class="pair-item-sub">
                    <span class="pair-addr mono">{d.addr}</span>
                  </div>
                </div>
              ))
            )}
          </section>

          <section class="pair-manual-group">
            <p class="pair-group-title">{t('nodes.pair.manualTitle')}</p>
            <p class="hint">{t('nodes.pair.manualHint')}</p>
            <form
              class="pair-manual"
              onSubmit={(e) => {
                e.preventDefault()
                const target = manual.trim()
                if (target === '') return
                if (!isPeerAddr(target)) {
                  setNotice('')
                  setErr(t('nodes.addDevice.badAddr'))
                  return
                }
                start(target)
              }}
            >
              <input
                class="input"
                type="text"
                inputMode="url"
                spellcheck={false}
                autoComplete="off"
                value={manual}
                placeholder={t('nodes.pair.manual')}
                onInput={(e) => {
                  setManual((e.target as HTMLInputElement).value)
                  if (err) setErr('')
                }}
              />
              <button class="btn small" type="submit" disabled={busy !== '' || manual.trim() === ''}>
                {busy === manual.trim() && manual.trim() !== ''
                  ? t('common.loading')
                  : t('nodes.pair.start')}
              </button>
            </form>
          </section>
        </div>
      )}
    </div>
  )
}

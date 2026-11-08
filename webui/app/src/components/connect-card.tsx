// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'preact/hooks'
import { api, type PairScanResult, type PairView } from '../api/client'
import { useAsync } from '../hooks'
import { t } from '../i18n'
import { isPeerAddr } from '../utils/peer-address'
import { Icon } from './icons'
import { DeviceSection } from './device-section'
import { toastError } from './toast'
import { PairInboundCard } from './pair-inbound'
import {
  formatLeft,
  mergeCandidates,
  requestLeft,
  type PairCandidate,
} from './pair-flow'

/** Connecting another machine, in the two shapes the CLI offers: a pairing
 *  session started from LAN discovery (both screens show the same code), and
 *  an address written straight into the dial list.
 *
 *  One body, one shell. The 节点网络 page that used to reuse this body as a
 *  card is gone — 工作空间's header is the only way in — so the card shell went
 *  with it rather than sitting here as a second copy that nothing renders.
 *
 *  The panel is ordered by what the operator came to do, not by the order the
 *  API returns things in. Discovery is the first screen — it runs by itself on
 *  open, and a device that answers is one press away. Everything that is *not*
 *  the happy path (an address to type, the configured dial list, the capability
 *  card) folds away underneath, so the common case never has to scroll past the
 *  rare one. An inbound request jumps the queue: somebody is standing at the
 *  other machine waiting, and that is the only urgent thing in here. */

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

/** The last sweep, module-wide, with the moment it finished.
 *
 *  The trigger mounts and unmounts this body, so panel state dies with the
 *  popover. Reopening used to cost a fresh /24 sweep every time — three
 *  seconds of "scanning" for an answer that cannot have changed — and the
 *  previous results were gone even when it had. Keeping the last result here
 *  makes a reopen instant, and the refresh button stays for the case where the
 *  LAN did change. */
let lastSweep: { at: number; result: PairScanResult } | null = null
const SWEEP_FRESH_MS = 30_000

function freshSweep(): PairScanResult | null {
  if (!lastSweep) return null
  return Date.now() - lastSweep.at < SWEEP_FRESH_MS ? lastSweep.result : null
}

function ConnectBody({ onChanged }: { onChanged(): void }) {
  const [tick, setTick] = useState(0)
  const { data } = useAsync<PairView>(() => api.pair(), [], tick)
  const [manual, setManual] = useState('')
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')
  const [notice, setNotice] = useState('')
  const [rowErr, setRowErr] = useState<Record<string, string>>({})
  const [scan, setScan] = useState<PairScanResult | null>(freshSweep)
  // Starts true when no fresh sweep exists: the first paint is either
  // "searching" or a real list, never a "nothing found" that the scan is about
  // to contradict half a second later.
  const [scanning, setScanning] = useState(() => freshSweep() === null)
  const [manualOpen, setManualOpen] = useState(false)
  const [peersOpen, setPeersOpen] = useState(false)
  // The dial-list half is its own request (GET /api/peers). Mounting it only
  // after the fold is opened keeps opening the panel to one read instead of
  // two, and keeps a management surface out of the hot path.
  const [peersMounted, setPeersMounted] = useState(false)
  const [dismissed, setDismissed] = useState<string[]>([])
  const [now, setNow] = useState(() => Date.now())
  const autoOpenedManual = useRef(false)
  const notifiedDone = useRef<Set<string>>(new Set())
  const inFlight = useRef(false)
  // When this copy of the request list landed. The countdown counts from the
  // server's `expires_in` plus this anchor, so the panel agrees with the
  // daemon's clock instead of the browser's (see requestLeft).
  const arrivedAt = useRef(Date.now())
  useEffect(() => {
    arrivedAt.current = Date.now()
  }, [data])

  const waiting = (data?.outgoing ?? []).some((o) => o.state === 'waiting')
  const pendingReqs = data?.requests ?? []
  const outgoing = (data?.outgoing ?? []).filter((o) => !dismissed.includes(o.session))
  const linked = data?.linked ?? 0
  const candidates = mergeCandidates(data?.discovered ?? [], scan?.candidates ?? [])
  const linkedHosts = new Set(data?.linked_hosts ?? [])
  const beaconOnly = (data?.discovered ?? []).length

  /** "Rescan" does both halves of looking again: it re-reads the beacon table
   *  and sweeps the local /24 over TCP. The sweep is what makes the button
   *  meaningful behind an access point that filters broadcast — there the
   *  beacon list stays empty forever while the peer sits one hop away and is
   *  perfectly reachable. */
  async function rescan() {
    if (inFlight.current) return
    inFlight.current = true
    setScanning(true)
    setErr('')
    try {
      const result = await api.pairScan()
      setScan(result)
      lastSweep = { at: Date.now(), result }
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      inFlight.current = false
      setScanning(false)
      setTick((v) => v + 1)
    }
  }

  // Discovery runs on open. The operator pressed "add a device" — that is
  // already the request to look, and asking for a second press on "scan" was
  // the same intention counted twice. Skipped while the last sweep is fresh.
  useEffect(() => {
    if (freshSweep() !== null) return
    void rescan()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // While a session waits on the remote human — or a request sits
  // unanswered — poll so state flips show without a manual refresh.
  useEffect(() => {
    if (!waiting && pendingReqs.length === 0) return
    const id = setInterval(() => setTick((v) => v + 1), 2500)
    return () => clearInterval(id)
  }, [waiting, pendingReqs.length])

  // Only the expiry clock needs a one-second beat, and only while a request is
  // on screen. Ticking the whole panel every second would re-render the device
  // list for a number that changes once a minute.
  useEffect(() => {
    if (pendingReqs.length === 0) return
    setNow(Date.now())
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [pendingReqs.length])

  // A green line that never goes away stops being feedback and becomes
  // furniture. Success retires itself; errors stay until the next attempt.
  useEffect(() => {
    if (!notice) return
    const id = setTimeout(() => setNotice(''), 6000)
    return () => clearTimeout(id)
  }, [notice])

  // A finished pairing changed the fleet, so the map behind the popover has to
  // hear about it. `answer` reports the responder side; this is the initiator
  // side, which only learns about it from polling.
  useEffect(() => {
    for (const o of data?.outgoing ?? []) {
      if (o.state !== 'done' || notifiedDone.current.has(o.session)) continue
      notifiedDone.current.add(o.session)
      onChanged()
    }
  }, [data, onChanged])

  // Landing on the empty state with nothing connected is exactly the moment
  // the address field is the way out, so it opens itself — once. Reopening it
  // after the operator closed it would be the panel arguing with them.
  useEffect(() => {
    if (autoOpenedManual.current || !scan) return
    if (candidates.length > 0 || linked > 0) return
    autoOpenedManual.current = true
    setManualOpen(true)
  }, [scan, candidates.length, linked])

  async function start(target: string) {
    if (busy) return
    setErr('')
    setNotice('')
    setRowErr((prev) => {
      if (!(target in prev)) return prev
      const next = { ...prev }
      delete next[target]
      return next
    })
    setBusy(target)
    try {
      await api.pairInitiate(target)
      if (manual.trim() === target) setManual('')
    } catch (e) {
      // Leave a typed address in place: an unreachable peer is usually a typo
      // or a daemon that is not up yet, and retyping host:port is the wrong
      // penalty for either. The message lands on the row that failed rather
      // than at the top of the panel, so it cannot be mistaken for a verdict
      // on some other device.
      const msg = e instanceof Error ? e.message : String(e)
      setRowErr((prev) => ({ ...prev, [target]: msg }))
      setManualOpen(true)
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

  function submitManual(e: Event) {
    e.preventDefault()
    const target = manual.trim()
    if (target === '') return
    if (!isPeerAddr(target)) {
      setErr(t('nodes.addDevice.badAddr'))
      return
    }
    setErr('')
    start(target)
  }

  const showEmpty = candidates.length === 0 && !scanning
  // "Ready to pair (n)" is a promise; when every row on the LAN is already
  // joined there is nothing to pair, and the count would be a lie.
  const pairable = candidates.filter((c) => !linkedHosts.has(c.key)).length

  return (
    <div class="connect-body">
      <SelfStrip name={data?.self_name ?? ''} addr={data?.self_addr ?? ''} />

      {(err || notice) && (
        <p
          class={err ? 'connect-feedback is-error' : 'connect-feedback is-ok'}
          role="status"
          aria-live="polite"
        >
          {err || notice}
        </p>
      )}

      {/* Somebody is standing at the other machine, mid-ceremony. This is the
          only blocking thing in the panel and it goes first, at full size: as
          a row in a list it was one more grey box among the candidates. */}
      {pendingReqs.map((r) => (
        <PairInboundCard
          key={r.id}
          req={r}
          busy={busy !== ''}
          pending={busy === r.id}
          left={formatLeft(requestLeft(r, arrivedAt.current, now))}
          onAnswer={(confirm) => answer(r.id, confirm)}
        />
      ))}

      {outgoing.map((o) => (
        <section
          class={`pair-callout outgoing${o.state === 'done' ? ' done' : ''}`}
          key={o.session}
          aria-labelledby={`out-${o.session}`}
        >
          <div class="pair-callout-head">
            <span class="pair-callout-plate" aria-hidden="true">
              <Icon name={o.state === 'done' ? 'check' : 'wifi'} size={15} />
            </span>
            <div class="pair-callout-copy">
              <p class="pair-callout-title" id={`out-${o.session}`}>
                {o.state === 'waiting'
                  ? t('nodes.pair.waitTarget', { name: o.target || o.addr })
                  : o.state === 'done'
                    ? t('nodes.pair.done')
                    : o.state === 'rejected'
                      ? t('nodes.pair.rejected')
                      : o.state === 'expired'
                        ? t('nodes.pair.expired')
                        : t('nodes.pair.error')}
              </p>
              {o.target && o.target !== o.addr && <p class="pair-callout-sub">{o.addr}</p>}
            </div>
            {o.state !== 'waiting' && (
              <button
                class="icon-btn pair-callout-close"
                type="button"
                aria-label={t('common.close')}
                title={t('common.close')}
                onClick={() => setDismissed((d) => [...d, o.session])}
              >
                <Icon name="x" size={12} />
              </button>
            )}
          </div>

          {o.state === 'waiting' && (
            <>
              <div class="pair-callout-code">
                <span class="pair-code-chip mono" title={t('nodes.pair.code')}>
                  {o.code}
                </span>
                <span class="pair-callout-code-hint">{t('nodes.pair.compare')}</span>
              </div>
              <p class="pair-callout-wait">
                <span class="spinner spinner-inline" aria-hidden="true" />
                {t('nodes.pair.waiting')}
              </p>
            </>
          )}
          {o.state === 'done' && <p class="pair-callout-wait ok">{t('nodes.pair.doneHint')}</p>}
          {o.state === 'error' && o.err && <p class="pair-callout-wait">{o.err}</p>}
        </section>
      ))}

      <section class="pair-group pair-candidates" aria-labelledby="pair-found-title">
        <div class="pair-group-head">
          <p class="pair-group-title" id="pair-found-title">
            {pairable > 0
              ? t('nodes.pair.foundCount', { n: pairable })
              : t('nodes.pair.discovered')}
          </p>
          <button
            class="btn small ghost pair-refresh"
            type="button"
            disabled={scanning}
            onClick={rescan}
            title={t('nodes.pair.refresh')}
          >
            <Icon name="refresh" size={12} />
            {scanning ? t('nodes.pair.scanningShort') : t('nodes.pair.refresh')}
          </button>
        </div>

        {scanning && candidates.length === 0 && (
          <div class="pair-searching" role="status" aria-live="polite">
            <span class="spinner" aria-hidden="true" />
            <p class="pair-searching-copy">{t('nodes.pair.searching')}</p>
            <p class="pair-searching-sub">{t('nodes.pair.searchingHint')}</p>
          </div>
        )}

        {showEmpty && (
          <>
            <div class="pair-empty">
              <span class="pair-empty-icon" aria-hidden="true">
                <Icon name="devices" size={17} />
              </span>
              <div class="pair-empty-copy">
                <p class="pair-empty-title">
                  {linked > 0
                    ? t('nodes.pair.linkedTitle', { n: linked })
                    : t('nodes.pair.emptyTitle')}
                </p>
                <p class="dim">
                  {linked > 0
                    ? t('nodes.pair.linkedHint', { n: linked })
                    : t('nodes.pair.emptyHint')}
                </p>
              </div>
            </div>
            {linked === 0 && (
              <ul class="pair-checks">
                <li>{t('nodes.pair.checkDaemon')}</li>
                <li>{t('nodes.pair.checkSameLan')}</li>
                <li>{t('nodes.pair.checkFirewall')}</li>
              </ul>
            )}
          </>
        )}

        {candidates.map((c) => (
          <CandidateRow
            key={c.key}
            c={c}
            linked={linkedHosts.has(c.key)}
            busy={busy === c.target}
            disabled={busy !== ''}
            error={rowErr[c.target]}
            onStart={() => start(c.target)}
          />
        ))}

        {scan && !scanning && (
          <p class="pair-scan-note">
            {beaconOnly === 0
              ? t('nodes.pair.scanNoteSweepOnly', {
                  n: scan.probed,
                  found: scan.candidates.length,
                })
              : t('nodes.pair.scanNote', {
                  n: scan.probed,
                  subnets: scan.subnets.join(', ') || '—',
                  found: scan.candidates.length,
                })}
          </p>
        )}
      </section>

      {/* Everything below is the long tail: a typed address, the configured
          dial list, the capability card. Folded, because the operator who needs
          them knows they need them — and the one who does not was scrolling
          past them to reach the device list above. */}
      <details
        class="pair-fold"
        open={manualOpen}
        onToggle={(e) => setManualOpen((e.currentTarget as HTMLDetailsElement).open)}
      >
        <summary class="pair-fold-head">
          <Icon name="plug" size={13} />
          <span class="pair-fold-title">{t('nodes.pair.manualTitle')}</span>
        </summary>
        <p class="hint pair-fold-hint">{t('nodes.pair.manualHint')}</p>
        <form class="pair-manual" onSubmit={submitManual} noValidate>
          <input
            class="input"
            type="text"
            inputMode="url"
            spellcheck={false}
            autoComplete="off"
            value={manual}
            placeholder={t('nodes.pair.manual')}
            aria-label={t('nodes.pair.manualTitle')}
            onInput={(e) => {
              setManual((e.target as HTMLInputElement).value)
              if (err) setErr('')
            }}
          />
          <button
            class="btn small primary"
            type="submit"
            disabled={busy !== '' || manual.trim() === ''}
          >
            {busy === manual.trim() && manual.trim() !== ''
              ? t('common.loading')
              : t('nodes.pair.start')}
          </button>
        </form>
      </details>

      <details
        class="pair-fold"
        open={peersOpen}
        onToggle={(e) => {
          const open = (e.currentTarget as HTMLDetailsElement).open
          setPeersOpen(open)
          if (open) setPeersMounted(true)
        }}
      >
        <summary class="pair-fold-head">
          <Icon name="network" size={13} />
          <span class="pair-fold-title">{t('nodes.pair.peersFold')}</span>
        </summary>
        <p class="hint pair-fold-hint">{t('nodes.pair.peersFoldHint')}</p>
        {peersMounted && (
          <div class="pair-fold-body">
            <DeviceSection onChanged={onChanged} />
          </div>
        )}
      </details>
    </div>
  )
}

/** This machine, in one line.
 *
 *  Pairing is a two-screen act: the code has to match on both, and the human
 *  at the other screen has to know which row is the machine they just left.
 *  The panel used to know every device on the LAN except itself. The address
 *  is also the only fallback when no beacon arrives — it is the thing to read
 *  out loud and type on the other side. */
function SelfStrip({ name, addr }: { name: string; addr: string }) {
  return (
    <div class="connect-id">
      <span class="connect-id-plate" aria-hidden="true">
        <Icon name="cpu" size={14} />
      </span>
      <div class="connect-id-copy">
        <span class="connect-id-label">{t('nodes.pair.selfLabel')}</span>
        <span class="connect-id-name">
          {/* The name gives up its width first: it is a reminder, the address
              is a value the operator has to read or type elsewhere. */}
          <span class="connect-id-text" title={name}>
            {name || '—'}
          </span>
          {addr && (
            <span class="connect-id-addr mono" title={addr}>
              {addr}
            </span>
          )}
        </span>
      </div>
      {addr && <CopyAddrButton value={addr} />}
    </div>
  )
}

/** Copy with an outcome visible on the button itself.
 *
 *  `navigator.clipboard` needs a secure context, and a console opened at
 *  http://10.86.36.57:7840 — the address this very panel prints — is not one.
 *  The textarea fallback covers that case; both paths end on the same check
 *  mark, so the operator learns the address reached the clipboard without a
 *  toast for a one-word result. */
function CopyAddrButton({ value }: { value: string }) {
  const [done, setDone] = useState(false)
  async function copy() {
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(value)
      } else {
        const el = document.createElement('textarea')
        el.value = value
        el.setAttribute('readonly', '')
        el.style.position = 'fixed'
        el.style.opacity = '0'
        document.body.appendChild(el)
        el.select()
        document.execCommand('copy')
        document.body.removeChild(el)
      }
      setDone(true)
      setTimeout(() => setDone(false), 1600)
    } catch (e) {
      toastError(e)
    }
  }
  return (
    <button
      class={`icon-btn connect-id-copybtn${done ? ' done' : ''}`}
      type="button"
      aria-label={t('nodes.pair.copyAddr')}
      title={done ? t('nodes.pair.copied') : t('nodes.pair.copyAddr')}
      onClick={copy}
    >
      <Icon name={done ? 'check' : 'copy'} size={13} />
    </button>
  )
}

/** One pairable device. The row states its evidence — signed beacon, bare port,
 *  or both — because that is the difference between "this is the machine I
 *  think it is" and "something answers at this address".
 *
 *  A device already in the fleet keeps its row (hiding it would make the list
 *  lie about what is on the LAN) but loses the button: pairing a joined node is
 *  a ceremony that can only end in "refused". */
function CandidateRow({
  c,
  linked,
  busy,
  disabled,
  error,
  onStart,
}: {
  c: PairCandidate
  linked: boolean
  busy: boolean
  disabled: boolean
  error?: string
  onStart(): void
}) {
  return (
    <div class={`pair-item${error ? ' has-error' : ''}${linked ? ' linked' : ''}`}>
      <div class="pair-item-top">
        {/* Discovered, not paired: the dot stays neutral — green is reserved
            for a node that is actually connected. */}
        <span class={`node-dot${linked ? ' on' : ''}`} aria-hidden="true" />
        <span class={`pair-name${c.name ? '' : ' mono'}`} title={c.name || c.addr}>
          {c.name || c.addr}
        </span>
        {linked ? (
          <span class="badge green">{t('nodes.pair.alreadyLinked')}</span>
        ) : (
          <>
            {c.name && c.source !== 'scan' && !c.verified && (
              <span class="badge">{t('nodes.pair.unverified')}</span>
            )}
            {c.source === 'scan' && <span class="badge">{t('nodes.pair.scanned')}</span>}
            <button
              class="btn small pair-item-cta"
              type="button"
              disabled={disabled}
              onClick={onStart}
            >
              {busy ? t('common.loading') : t('nodes.pair.start')}
            </button>
          </>
        )}
      </div>
      {/* A sweep-only row has no name, so its address is already the title;
          repeating it underneath was two identical lines for one fact. */}
      {(c.name !== '' || c.source === 'both') && (
        <div class="pair-item-sub">
          {c.name !== '' && <span class="pair-addr mono">{c.addr}</span>}
          {c.source === 'both' && <span class="pair-src">{t('nodes.pair.srcBoth')}</span>}
        </div>
      )}
      {error && <p class="pair-row-error">{error}</p>}
    </div>
  )
}

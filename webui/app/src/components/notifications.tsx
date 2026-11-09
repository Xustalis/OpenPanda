// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ComponentChildren } from 'preact'
import { useCallback, useEffect, useMemo, useRef, useState } from 'preact/hooks'
import {
  ApiError,
  api,
  type DiscoveredDevice,
  type DoctorCheck,
  type OutgoingPair,
  type PairRequest,
  type PairView,
  type PeerEntry,
  type PeersView,
  type Reminder,
  type UpdateStatus,
} from '../api/client'
import { formatDuration, relativeTime } from '../format/time'
import { useAsync, useChangeSignal, useLocaleRerender, useVisibleInterval } from '../hooks'
import { locale, t } from '../i18n'
import { navigate } from '../nav'
import { Icon, type IconName } from './icons'
import { toast, toastError } from './toast'
import { confirmDialog } from './confirm'

/** 通知 — the bell in the rail's head and the window behind it.
 *
 *  The window is a fixed-size inbox with four sources. Three of them raise
 *  themselves: 设备 (a machine on the LAN waiting for a pairing code), 系统 (a
 *  check that stopped passing, a release in the update channel) and 团队 (a
 *  placeholder until that feature lands). The fourth, 提醒, is the one the
 *  reader creates — it lives here rather than in the popover it used to be,
 *  because a schedule and the things that interrupt you are the same question
 *  asked twice: what do I need to know, and when.
 *
 *  Every row in the list has the same skeleton — glyph, title, one line of
 *  evidence, and a right-hand slot that holds either a time or the actions the
 *  row supports. A notification list is read by scanning, so the eye should
 *  learn one shape and then only read the words; the earlier version mixed
 *  icon rows, bare-label rows and boxed detail blocks, and it read as three
 *  different lists stacked on top of each other.
 *
 *  It is a native `<dialog>` opened with `showModal()` for the same reason 设置
 *  is — the top layer, focus trapping and Escape come from the platform
 *  instead of from a hand-rolled overlay.
 *
 *  Read state is local: "unread" means "this console has not shown you this row
 *  yet", which is a property of this browser, not of the node — the same reason
 *  the thread list keeps its seen marks in localStorage.
 */

/** Raise the window from anywhere — the ⌘K palette, or a `#/reminders`
 *  bookmark saved before the bell was rebuilt. No-op until the bell mounted. */
let raise: (() => void) | null = null
export function openNotifications(): void {
  raise?.()
}

/** One row of the inbox, discriminated by where it came from. Keeping the
 *  payload rather than pre-rendered strings means a row can offer the action
 *  its source supports (a pairing request is answered here; a doctor failure
 *  can only be walked to). */
type Item =
  | { id: string; kind: 'device'; type: 'request'; request: PairRequest }
  | { id: string; kind: 'device'; type: 'discovered'; device: DiscoveredDevice }
  | { id: string; kind: 'device'; type: 'outgoing'; pair: OutgoingPair }
  | { id: string; kind: 'device'; type: 'offline'; peer: PeerEntry }
  | { id: string; kind: 'system'; type: 'doctor'; check: DoctorCheck }
  | { id: string; kind: 'system'; type: 'update'; status: UpdateStatus }
  | { id: string; kind: 'reminder'; type: 'fired'; reminder: Reminder }

type Kind = Item['kind']
/** 团队 has no producer yet — the filter exists so the window says where team
 *  messages will land instead of leaving the reader guessing. */
type Filter = 'all' | Kind | 'team'

/** Sections are the order the sources matter in: a machine waiting at the other
 *  end, then what the reader scheduled, then what the node reports. */
const KIND_ORDER: Kind[] = ['device', 'reminder', 'system']

const TABS: Array<{ id: Filter; label: string; icon: IconName }> = [
  { id: 'all', label: 'notifications.tabAll', icon: 'bell' },
  { id: 'device', label: 'notifications.tabDevice', icon: 'devices' },
  { id: 'system', label: 'notifications.tabSystem', icon: 'alert' },
  { id: 'reminder', label: 'notifications.tabReminder', icon: 'clock' },
  { id: 'team', label: 'notifications.tabTeam', icon: 'users' },
]

const SECTION_LABEL: Record<Kind, string> = {
  device: 'notifications.tabDevice',
  reminder: 'notifications.tabReminder',
  system: 'notifications.tabSystem',
}

const READ_KEY = 'openpanda.notifications-read'

function loadRead(): Set<string> {
  try {
    const raw = localStorage.getItem(READ_KEY)
    if (!raw) return new Set()
    const arr: unknown = JSON.parse(raw)
    if (!Array.isArray(arr)) return new Set()
    return new Set(arr.filter((v): v is string => typeof v === 'string'))
  } catch {
    return new Set()
  }
}

function saveRead(ids: Set<string>): void {
  try {
    // Bounded: the tail is what matters, and a set that grows forever would
    // eventually cost more to parse than the list is worth.
    localStorage.setItem(READ_KEY, JSON.stringify([...ids].slice(-300)))
  } catch {
    /* private mode: unread counts simply reset on the next load */
  }
}

/** A reminder is pending while it has not fired yet, or while it repeats: a
 *  recurring row keeps its LAST fire in `fired_at`, so a non-zero stamp is not
 *  a retirement. */
function isPending(r: Reminder): boolean {
  return r.fired_at === 0 || r.repeat_seconds > 0
}

/** Turn the live reads into rows, grouped by source. Pure, so the ordering
 *  rules live in one place: within a source, the newest question first. */
function buildItems(
  pair: PairView | null,
  peers: PeersView | null,
  doctor: DoctorCheck[] | null,
  update: UpdateStatus | null,
  reminders: Reminder[] | null,
): Item[] {
  const items: Item[] = []
  for (const r of pair?.requests ?? []) {
    items.push({ id: `pair-request:${r.id}`, kind: 'device', type: 'request', request: r })
  }
  for (const d of pair?.discovered ?? []) {
    items.push({ id: `lan:${d.id}`, kind: 'device', type: 'discovered', device: d })
  }
  for (const o of pair?.outgoing ?? []) {
    if (o.state !== 'waiting') continue
    items.push({ id: `pair-out:${o.session}`, kind: 'device', type: 'outgoing', pair: o })
  }
  for (const p of peers?.peers ?? []) {
    // A dial-list entry that has never been seen is a typo waiting to happen,
    // not a device that went away — only a peer with a last_seen can be "off".
    if (p.online || !p.last_seen) continue
    items.push({ id: `peer-off:${p.addr}`, kind: 'device', type: 'offline', peer: p })
  }
  for (const r of reminders ?? []) {
    // A recurring reminder never retires, so it never becomes a row of its own:
    // each occurrence is a moment (the toast), not a record. A one-shot row
    // that has fired is exactly a notification, and keeps its fire stamp in the
    // id so two schedules with the same text stay two rows.
    if (isPending(r)) continue
    items.push({ id: `reminder-fired:${r.id}:${r.fired_at}`, kind: 'reminder', type: 'fired', reminder: r })
  }
  for (const c of doctor ?? []) {
    if (c.ok) continue
    items.push({ id: `doctor:${c.key}`, kind: 'system', type: 'doctor', check: c })
  }
  if (update?.available) {
    items.push({ id: `update:${update.latest ?? ''}`, kind: 'system', type: 'update', status: update })
  }
  return items.sort((a, b) => KIND_ORDER.indexOf(a.kind) - KIND_ORDER.indexOf(b.kind))
}

/** Items bucketed by source, in KIND_ORDER, skipping empty buckets. */
function sectionsOf(items: Item[]): Array<{ kind: Kind; items: Item[] }> {
  const out: Array<{ kind: Kind; items: Item[] }> = []
  for (const kind of KIND_ORDER) {
    const members = items.filter((it) => it.kind === kind)
    if (members.length > 0) out.push({ kind, items: members })
  }
  return out
}

export function NotificationBell() {
  useLocaleRerender()
  const [open, setOpen] = useState(false)
  const [reload, setReload] = useState(0)
  // Countdowns ("expires in 4 min") are painted from a clock, so they go stale
  // while the window sits open. A 30s beat re-renders them without re-fetching.
  const [now, setNow] = useState(() => Date.now())
  const [read, setRead] = useState<Set<string>>(loadRead)
  const sheet = useRef<HTMLDialogElement>(null)
  const change = useChangeSignal()

  // 设备 is the source that changes on its own while nobody is looking, so it
  // loads with the app: the badge has to be honest before the window is opened.
  const { data: pair } = useAsync<PairView>(() => api.pair(), [], change + reload)
  const { data: peers } = useAsync<PeersView>(() => api.peers(), [], change + reload)
  const { data: reminders } = useAsync<Reminder[]>(() => api.reminders(), [], change + reload)

  // 系统 is a slower read (a doctor run dials the model endpoint), so it is
  // polled on a visible-tab beat instead of riding the change feed. The
  // window's "check again" calls the same function, so there is one answer.
  const [doctor, setDoctor] = useState<DoctorCheck[] | null>(null)
  const [update, setUpdate] = useState<UpdateStatus | null>(null)
  const [checkedAt, setCheckedAt] = useState(0)
  const [checking, setChecking] = useState(false)
  const inflight = useRef(false)

  const checkSystem = useCallback(async () => {
    if (inflight.current) return
    inflight.current = true
    setChecking(true)
    try {
      // Independent reads: a failure of one must not hide the other.
      const [report, status] = await Promise.all([
        api.doctor().catch(() => null),
        api.updateStatus().catch(() => null),
      ])
      if (report) setDoctor(report.checks)
      setUpdate(status)
      setCheckedAt(Date.now())
    } finally {
      inflight.current = false
      setChecking(false)
    }
  }, [])

  useEffect(() => {
    void checkSystem()
  }, [checkSystem])
  // Ten minutes: this is an inbox, not a monitoring dashboard, and a doctor
  // run dials the model endpoint. "Check again" covers impatience; the beat is
  // only here so a failure that appears while the tab sits open still reaches
  // the badge.
  useVisibleInterval(() => checkSystem(), 600_000)

  // A reminder that fires has to say so even when nobody is looking at the bell:
  // the change feed refetches the list, so diff each snapshot against the last
  // one and speak up. The first snapshot only sets the baseline — reminders
  // that fired before the console opened are history, not news. A recurring row
  // bumps `fired_at` on every occurrence, so it announces each one.
  const seenFired = useRef<Map<number, number> | null>(null)
  useEffect(() => {
    if (!reminders) return
    const seen = seenFired.current
    if (seen) {
      for (const r of reminders) {
        if (r.fired_at <= (seen.get(r.id) ?? 0)) continue
        toast(t('reminders.toastFired', { message: r.message }), 'success')
      }
    }
    const next = new Map<number, number>()
    for (const r of reminders) if (r.fired_at > 0) next.set(r.id, r.fired_at)
    seenFired.current = next
  }, [reminders])

  useEffect(() => {
    raise = () => setOpen(true)
    return () => {
      raise = null
    }
  }, [])

  // showModal rather than a fixed div: the platform puts the dialog in the top
  // layer (no ancestor overflow or transform can clip it), traps focus, and
  // gives Escape for free. The effect syncs the element with the state both
  // ways — `close` fires on Escape too, so the state follows the browser.
  useEffect(() => {
    const el = sheet.current
    if (!el) return
    if (open && !el.open) el.showModal()
    if (!open && el.open) el.close()
  }, [open])

  useEffect(() => {
    if (!open) return
    setNow(Date.now())
    const id = setInterval(() => setNow(Date.now()), 30_000)
    return () => clearInterval(id)
  }, [open])

  const items = useMemo(
    () => buildItems(pair, peers, doctor, update, reminders),
    [pair, peers, doctor, update, reminders],
  )
  const pendingReminders = useMemo(() => (reminders ?? []).filter(isPending), [reminders])
  const unread = items.filter((it) => !read.has(it.id)).length
  const label = unread ? `${t('notifications.title')} (${unread})` : t('notifications.title')

  function markRead(id: string) {
    if (read.has(id)) return
    const next = new Set(read)
    next.add(id)
    setRead(next)
    saveRead(next)
  }

  function markAll() {
    if (unread === 0) return
    const next = new Set(read)
    for (const it of items) next.add(it.id)
    setRead(next)
    saveRead(next)
  }

  return (
    <div class="notif-bell">
      <button
        type="button"
        class={`icon-btn tip-down${open ? ' open' : ''}`}
        data-tip={open ? undefined : t('notifications.title')}
        aria-label={label}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        <Icon name="bell" />
        {unread > 0 && (
          <span class="notif-bell-dot" aria-hidden="true">
            {unread > 9 ? '9+' : unread}
          </span>
        )}
      </button>

      {open && (
        <dialog
          ref={sheet}
          class="notif-dialog"
          aria-label={t('notifications.title')}
          // Escape is the browser's own exit from a modal dialog; stopping the
          // keydown here keeps the thread drawer behind from closing with it.
          onKeyDown={(e) => {
            if (e.key === 'Escape') e.stopPropagation()
          }}
          onClose={() => setOpen(false)}
          // A click that lands on the dialog element itself is a backdrop
          // click (the panel fills the box), and an inbox is a glance: walking
          // away should not require hunting for the ✕.
          onClick={(e) => {
            if (e.target === sheet.current) setOpen(false)
          }}
        >
          <NotificationsWindow
            items={items}
            pair={pair}
            reminders={reminders}
            pendingReminders={pendingReminders}
            now={now}
            read={read}
            unread={unread}
            checking={checking}
            checkedAt={checkedAt}
            onMarkRead={markRead}
            onMarkAll={markAll}
            onChanged={() => setReload((v) => v + 1)}
            onRecheck={() => void checkSystem()}
            onClose={() => setOpen(false)}
          />
        </dialog>
      )}
    </div>
  )
}

/** The window: a source rail on the left, the rows on the right, one status
 *  line at the foot. */
function NotificationsWindow(props: {
  items: Item[]
  pair: PairView | null
  reminders: Reminder[] | null
  pendingReminders: Reminder[]
  now: number
  read: Set<string>
  unread: number
  checking: boolean
  checkedAt: number
  onMarkRead(id: string): void
  onMarkAll(): void
  onChanged(): void
  onRecheck(): void
  onClose(): void
}) {
  const [filter, setFilter] = useState<Filter>('all')
  const [busy, setBusy] = useState('')

  const shown = filter === 'all' ? props.items : props.items.filter((it) => it.kind === filter)
  const counts: Record<Filter, number> = {
    all: props.items.length,
    device: props.items.filter((it) => it.kind === 'device').length,
    system: props.items.filter((it) => it.kind === 'system').length,
    // The reminder count is the plan, not the inbox: "2" is what the reader
    // scheduled, and a fired one has already moved into 全部.
    reminder: props.pendingReminders.length,
    team: 0,
  }

  function goSystem() {
    props.onClose()
    navigate({ view: 'settings', tab: 'system' })
  }

  async function answer(id: string, confirm: boolean) {
    if (busy) return
    setBusy(id)
    try {
      await api.pairAnswer(id, confirm)
      const req = props.pair?.requests.find((r) => r.id === id)
      const name = req?.peer_name || req?.peer_addr || ''
      toast(confirm ? t('notifications.allowed', { name }) : t('notifications.rejected', { name }), 'success')
      props.onChanged()
    } catch (e) {
      toastError(e)
    } finally {
      setBusy('')
    }
  }

  async function pairWith(device: DiscoveredDevice) {
    if (busy) return
    setBusy(device.id)
    try {
      const res = await api.pairInitiate(device.id)
      // The code is the whole point of the ceremony: the reader has to compare
      // it against the other screen, so it goes in the toast rather than
      // disappearing behind a reload.
      toast(`${t('notifications.code', { code: res.code })} — ${t('notifications.outgoingHint')}`, 'success')
      props.onChanged()
    } catch (e) {
      toastError(e)
    } finally {
      setBusy('')
    }
  }

  const sections = sectionsOf(shown)

  return (
    <div class="notif-shell">
      <header class="notif-head">
        <span class="notif-head-title">{t('notifications.title')}</span>
        {props.unread > 0 && (
          <span class="notif-head-count">{t('notifications.unread', { n: props.unread })}</span>
        )}
        <button
          type="button"
          class="link-btn notif-mark"
          onClick={props.onMarkAll}
          disabled={props.unread === 0}
        >
          {t('notifications.markAll')}
        </button>
        <button
          type="button"
          class="icon-btn notif-close"
          aria-label={t('common.close')}
          title={t('common.close')}
          onClick={props.onClose}
        >
          <Icon name="x" size={15} />
        </button>
      </header>

      <div class="notif-main">
        <nav class="notif-rail" aria-label={t('notifications.title')}>
          {TABS.map((tab) => (
            <button
              key={tab.id}
              type="button"
              class={`notif-tab${filter === tab.id ? ' on' : ''}`}
              aria-current={filter === tab.id ? 'true' : undefined}
              onClick={() => setFilter(tab.id)}
            >
              <Icon name={tab.icon} size={14} />
              <span class="notif-tab-label">{t(tab.label)}</span>
              {counts[tab.id] > 0 && <span class="notif-tab-count">{counts[tab.id]}</span>}
            </button>
          ))}
          <p class="notif-rail-note dim">{t('notifications.railNote')}</p>
        </nav>

        {filter === 'reminder' ? (
          <ReminderPane reminders={props.reminders} now={props.now} onChanged={props.onChanged} />
        ) : (
          <div class="notif-list">
            {sections.length === 0 ? (
              <div class="notif-empty">
                <span class="notif-empty-glyph" aria-hidden="true">
                  <Icon name={filter === 'team' ? 'users' : filter === 'system' ? 'check' : 'bell'} size={20} />
                </span>
                <p class="notif-empty-title">{emptyTitle(filter)}</p>
                <p class="notif-empty-hint dim">{emptyCopy(filter)}</p>
              </div>
            ) : (
              sections.map((section) => (
                <section class="notif-section" key={section.kind}>
                  {/* The section label is the context the rows used to repeat in
                      every title ("系统检查未通过：…"); saying it once is what
                      lets the rows stay one short line. */}
                  {filter === 'all' && (
                    <p class="notif-section-label">{t(SECTION_LABEL[section.kind])}</p>
                  )}
                  {section.items.map((it) => (
                    <NotifRow
                      key={it.id}
                      item={it}
                      now={props.now}
                      read={props.read.has(it.id)}
                      busy={busy}
                      onRead={() => props.onMarkRead(it.id)}
                      onAnswer={answer}
                      onPair={pairWith}
                      onSystem={goSystem}
                    />
                  ))}
                  {section.kind === 'system' && (
                    <button type="button" class="notif-section-foot" onClick={goSystem}>
                      {t('notifications.openSystem')}
                      <span aria-hidden="true">→</span>
                    </button>
                  )}
                </section>
              ))
            )}
          </div>
        )}
      </div>

      <footer class="notif-foot">
        <span class="dim">
          {props.checking
            ? t('notifications.checking')
            : props.checkedAt
              ? t('notifications.checked', {
                  when: relativeTime(new Date(props.checkedAt).toISOString(), locale(), props.now),
                })
              : ''}
        </span>
        <button type="button" class="link-btn notif-recheck" onClick={props.onRecheck} disabled={props.checking}>
          {t('notifications.recheck')}
        </button>
      </footer>
    </div>
  )
}

function emptyTitle(filter: Filter): string {
  if (filter === 'device') return t('notifications.emptyDeviceTitle')
  if (filter === 'system') return t('notifications.emptySystemTitle')
  if (filter === 'team') return t('notifications.emptyTeamTitle')
  return t('notifications.emptyTitle')
}

function emptyCopy(filter: Filter): string {
  if (filter === 'device') return t('notifications.emptyDevice')
  if (filter === 'system') return t('notifications.emptySystem')
  if (filter === 'team') return t('notifications.emptyTeam')
  return t('notifications.empty')
}

/** The one row shape in the list: glyph, title, one line of evidence, and a
 *  right-hand slot. Everything below composes it rather than inventing its own
 *  layout — that is what keeps a list of five different sources from reading
 *  like five different lists. */
function Row(props: {
  unread?: boolean
  tone?: 'warn' | 'bad' | 'ok' | 'dim'
  icon: IconName
  title: string
  detail?: ComponentChildren
  side?: ComponentChildren
  onClick?(): void
  children?: ComponentChildren
}) {
  return (
    <article
      class={`notif-row${props.unread ? ' unread' : ''}${props.onClick ? '' : ' static'}`}
      onClick={props.onClick}
    >
      <span class={`notif-glyph${props.tone ? ' ' + props.tone : ''}`} aria-hidden="true">
        <Icon name={props.icon} size={15} />
      </span>
      <div class="notif-body">
        <p class="notif-title">{props.title}</p>
        {props.detail && <p class="notif-detail">{props.detail}</p>}
        {props.children}
      </div>
      {props.side && <div class="notif-side">{props.side}</div>}
    </article>
  )
}

function NotifRow(props: {
  item: Item
  now: number
  read: boolean
  busy: string
  onRead(): void
  onAnswer(id: string, confirm: boolean): void
  onPair(device: DiscoveredDevice): void
  onSystem(): void
}) {
  const { item } = props
  const unread = !props.read

  if (item.type === 'request') {
    const r = item.request
    const name = r.peer_name || r.peer_addr
    return (
      <Row
        unread={unread}
        tone="warn"
        icon="devices"
        title={t('notifications.requestTitle', { name })}
        detail={
          <>
            <span class="mono">{r.peer_addr}</span>
            {' · '}
            {t('notifications.code', { code: r.code })}
            {typeof r.expires_in === 'number' && r.expires_in > 0
              ? ` · ${t('notifications.expires', { d: formatDuration(r.expires_in * 1000) })}`
              : ''}
          </>
        }
        side={
          <div class="notif-actions">
            <button class="btn small primary" disabled={props.busy !== ''} onClick={() => props.onAnswer(r.id, true)}>
              {t('notifications.allow')}
            </button>
            <button class="btn small danger" disabled={props.busy !== ''} onClick={() => props.onAnswer(r.id, false)}>
              {t('notifications.reject')}
            </button>
          </div>
        }
        onClick={props.onRead}
      >
        <p class="notif-hint">{t('notifications.requestHint')}</p>
      </Row>
    )
  }

  if (item.type === 'discovered') {
    const d = item.device
    return (
      <Row
        unread={unread}
        icon="wifi"
        title={t('notifications.discoveredTitle', { name: d.id })}
        detail={
          <>
            <span class="mono">{d.addr}</span>
            {' · '}
            {d.verified ? t('notifications.verified') : t('notifications.unverified')}
          </>
        }
        side={
          <button
            class="btn small"
            disabled={props.busy !== ''}
            onClick={(e) => {
              e.stopPropagation()
              props.onRead()
              props.onPair(d)
            }}
          >
            {t('notifications.pair')}
          </button>
        }
        onClick={props.onRead}
      />
    )
  }

  if (item.type === 'outgoing') {
    const o = item.pair
    return (
      <Row
        unread={unread}
        icon="refresh"
        title={t('notifications.outgoingTitle', { name: o.target || o.addr })}
        detail={
          <>
            <span class="mono">{o.addr}</span>
            {' · '}
            {t('notifications.code', { code: o.code })}
          </>
        }
        side={<span class="spinner spinner-inline" />}
        onClick={props.onRead}
      />
    )
  }

  if (item.type === 'offline') {
    const p = item.peer
    return (
      <Row
        unread={unread}
        icon="plug"
        tone="dim"
        title={t('notifications.offlineTitle', { name: p.name || p.addr })}
        detail={
          <>
            <span class="mono">{p.addr}</span>
            {p.last_seen
              ? ` · ${t('notifications.offlineMeta', { when: relativeTime(p.last_seen, locale(), props.now) })}`
              : ''}
            {p.trust ? ` · ${t('notifications.trust', { tier: p.trust })}` : ''}
          </>
        }
        side={
          <span class="notif-time">
            {p.last_seen ? relativeTime(p.last_seen, locale(), props.now) : ''}
          </span>
        }
        onClick={props.onRead}
      />
    )
  }

  if (item.type === 'fired') {
    const r = item.reminder
    return (
      <Row
        unread={unread}
        icon="clock"
        title={r.message}
        detail={
          <>
            {t('reminders.firedAt')}{' '}
            <span class="mono">{relativeTime(new Date(r.fired_at * 1000).toISOString(), locale(), props.now)}</span>
            {` · ${r.source}`}
          </>
        }
        onClick={props.onRead}
      />
    )
  }

  if (item.type === 'doctor') {
    const c = item.check
    const pairs = pairUp(c.pairs)
    return (
      <Row
        unread={unread}
        icon="alert"
        tone="bad"
        title={t(c.key, c.key)}
        detail={
          pairs.length > 0 ? (
            <>
              {pairs.map(([k, v], i) => (
                <span key={k}>
                  {i > 0 && ' · '}
                  <span class="notif-k">{k}</span> <span class="mono">{v}</span>
                </span>
              ))}
            </>
          ) : undefined
        }
        onClick={() => {
          props.onRead()
          props.onSystem()
        }}
      />
    )
  }

  const s = item.status
  return (
    <Row
      unread={unread}
      icon="download"
      tone="ok"
      title={t('notifications.updateTitle', { version: s.latest ?? '' })}
      detail={
        <span class="mono">
          {t('notifications.updateFrom', { current: s.current || '—', latest: s.latest ?? '' })}
        </span>
      }
      onClick={() => {
        props.onRead()
        props.onSystem()
      }}
    >
      {s.notes && <p class="notif-notes">{s.notes}</p>}
    </Row>
  )
}

/** Doctor pairs arrive flattened as k,v,k,v… — the row folds them into one
 *  line, which is all the detail a scan needs. */
function pairUp(pairs: string[] | undefined): Array<[string, string]> {
  if (!pairs) return []
  const out: Array<[string, string]> = []
  for (let i = 0; i + 1 < pairs.length; i += 2) out.push([pairs[i]!, pairs[i + 1]!])
  return out
}

/** Web Push is a property of this browser *and* of the node it talks to: with
 *  `push.enabled: false` there is no VAPID key to subscribe with. `off` is the
 *  node's answer, kept apart from a failed subscription (`error`) because the
 *  footer says different things — one is configuration, the other a retry. */
type PushState = 'checking' | 'idle' | 'on' | 'off' | 'unsupported' | 'denied' | 'error'

/** 提醒 — the one source in this window the reader creates rather than the node
 *  raising it. Pending rows are a schedule (they are not unread — they have not
 *  happened yet), so they get their own pane with the compose form; a reminder
 *  that fires moves into 全部 as an ordinary row. */
function ReminderPane(props: { reminders: Reminder[] | null; now: number; onChanged(): void }) {
  const [composing, setComposing] = useState(false)
  const [message, setMessage] = useState('')
  const [mode, setMode] = useState<WhenMode>('relative')
  const [minutes, setMinutes] = useState('10')
  const [dueAt, setDueAt] = useState('')
  const [everyMin, setEveryMin] = useState('')
  const [adding, setAdding] = useState(false)
  const [busy, setBusy] = useState('')
  const [push, setPush] = useState<PushState>('checking')
  const firstField = useRef<HTMLInputElement>(null)

  const list = props.reminders ?? []
  const pending = list.filter(isPending)
  const fired = list.filter((r) => !isPending(r))

  // The switch is a statement about *this* browser on *this* node, so it starts
  // from what the two of them say rather than from an idle "enable it" button.
  // The node is asked first: a 404 here means push is not configured, and
  // offering a subscription that can never be stored would be a lie the first
  // click exposes.
  useEffect(() => {
    if (typeof navigator === 'undefined' || !navigator.serviceWorker || !('PushManager' in window)) {
      setPush('unsupported')
      return
    }
    if (typeof Notification === 'undefined' || !window.isSecureContext) {
      // Push needs a secure context, and so does the permission prompt.
      setPush('unsupported')
      return
    }
    let alive = true
    api
      .pushKey()
      .then(() => navigator.serviceWorker.ready)
      .then((reg) => reg.pushManager.getSubscription())
      .then((sub) => {
        if (alive) setPush(sub ? 'on' : 'idle')
      })
      .catch((e: unknown) => {
        if (!alive) return
        // A missing route is the node saying `push.enabled: false`; anything
        // else is a transient read, and the footer should still offer the
        // switch rather than claim the node is misconfigured.
        setPush(e instanceof ApiError && e.status === 404 ? 'off' : 'idle')
      })
    return () => {
      alive = false
    }
  }, [])

  // Resolve the form into a date now, so the preview and the submit path read
  // the same value. An absolute time in the past is refused rather than
  // stored — the scanner would fire it on the next tick, which reads as the
  // form ignoring the input.
  let due: Date | null = null
  if (mode === 'absolute') {
    if (dueAt) {
      const at = new Date(dueAt)
      if (!Number.isNaN(at.getTime())) due = at
    }
  } else {
    const n = Number(minutes)
    if (n > 0) due = new Date(Date.now() + n * 60_000)
  }
  const past = mode === 'absolute' && due !== null && due.getTime() <= Date.now()
  const canAdd = !adding && message.trim().length > 0 && due !== null && !past
  const repeat = Number(everyMin)
  const repeatSeconds = repeat > 0 ? Math.round(repeat * 60) : undefined

  async function add(e: Event) {
    e.preventDefault()
    if (!canAdd || !due) return
    setAdding(true)
    try {
      await api.createReminder({
        message: message.trim(),
        ...(mode === 'absolute' ? { due_at: due.toISOString() } : { after_minutes: Number(minutes) }),
        repeat_seconds: repeatSeconds,
      })
      toast(t('reminders.added', { when: stamp(due) }), 'success')
      // The change feed usually lands first, but a dropped SSE link must not
      // leave the new row invisible until the window is reopened.
      props.onChanged()
      setMessage('')
      setDueAt('')
      setEveryMin('')
      setComposing(false)
    } catch (err) {
      toastError(err)
    } finally {
      setAdding(false)
    }
  }

  async function remove(id: number) {
    if (busy) return
    // Deleting a reminder cancels it for good — confirm first.
    const ok = await confirmDialog({
      title: t('reminders.deleteTitle'),
      message: t('reminders.deleteMsg'),
      confirmLabel: t('reminders.delete'),
    })
    if (!ok) return
    setBusy(String(id))
    try {
      await api.deleteReminder(id)
      props.onChanged()
    } catch (e) {
      toastError(e)
    } finally {
      setBusy('')
    }
  }

  /** Ask for notification permission and subscribe this browser to the node's
   *  Web Push service so reminders fire as OS notifications. */
  async function enableNotifications() {
    try {
      // The key comes first: without it there is nothing to subscribe to.
      const { key } = await api.pushKey()
      const perm = await Notification.requestPermission()
      if (perm !== 'granted') {
        setPush('denied')
        return
      }
      const reg = await navigator.serviceWorker.ready
      const sub = await reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: urlBase64ToBytes(key),
      })
      await api.pushSubscribe(sub.toJSON())
      setPush('on')
    } catch (e) {
      setPush(e instanceof ApiError && e.status === 404 ? 'off' : 'error')
      console.warn('push subscribe', e)
    }
  }

  function openComposer() {
    setComposing(true)
    // The message field is the first thing to fill; focusing it from the click
    // that opened the form keeps the flow one keystroke shorter.
    requestAnimationFrame(() => firstField.current?.focus())
  }

  return (
    <div class="rem-pane">
      <div class="rem-toolbar">
        <span class="rem-toolbar-title">
          {t('reminders.pending')}
          <span class="rem-toolbar-count">{pending.length}</span>
        </span>
        <button type="button" class="btn small primary rem-new" onClick={openComposer} disabled={composing}>
          <Icon name="plus" size={13} />
          {t('reminders.new')}
        </button>
      </div>

      {composing && (
        <form class="rem-form" onSubmit={add}>
          <input
            ref={firstField}
            class="input rem-message"
            type="text"
            placeholder={t('reminders.messagePlaceholder')}
            value={message}
            onInput={(e) => setMessage((e.target as HTMLInputElement).value)}
            required
          />
          <div class="rem-form-line">
            <div class="segmented rem-mode" role="radiogroup" aria-label={t('reminders.whenLabel')}>
              <button
                type="button"
                role="radio"
                aria-checked={mode === 'relative'}
                class={`seg${mode === 'relative' ? ' on' : ''}`}
                onClick={() => setMode('relative')}
              >
                {t('reminders.modeRelative')}
              </button>
              <button
                type="button"
                role="radio"
                aria-checked={mode === 'absolute'}
                class={`seg${mode === 'absolute' ? ' on' : ''}`}
                onClick={() => setMode('absolute')}
              >
                {t('reminders.modeAbsolute')}
              </button>
            </div>
            {mode === 'relative' ? (
              <>
                <input
                  class="input rem-minutes"
                  type="number"
                  min={1}
                  step={1}
                  inputMode="numeric"
                  placeholder="10"
                  title={t('reminders.afterMinutes')}
                  value={minutes}
                  onInput={(e) => setMinutes((e.target as HTMLInputElement).value)}
                />
                <span class="rem-suffix">{t('reminders.minutes')}</span>
              </>
            ) : (
              <input
                class="input rem-at"
                type="datetime-local"
                min={localStamp(new Date(Date.now() + 60_000))}
                aria-label={t('reminders.dueAt')}
                title={t('reminders.dueAt')}
                value={dueAt}
                onInput={(e) => setDueAt((e.target as HTMLInputElement).value)}
              />
            )}
            <input
              class="input rem-every"
              type="number"
              min={1}
              step={1}
              inputMode="numeric"
              placeholder={t('reminders.everyMinShort')}
              title={t('reminders.everyMinTitle')}
              value={everyMin}
              onInput={(e) => setEveryMin((e.target as HTMLInputElement).value)}
            />
            <span class="rem-preview dim">
              {past ? (
                <span class="gate-error">{t('reminders.pastTime')}</span>
              ) : due ? (
                `${stamp(due)} · ${t('reminders.dueIn', { d: formatDuration(due.getTime() - Date.now()) })}`
              ) : (
                ''
              )}
            </span>
            <span class="rem-actions">
              <button class="btn small rem-cancel" type="button" onClick={() => setComposing(false)}>
                {t('reminders.cancel')}
              </button>
              <button class="btn small primary rem-add" type="submit" disabled={!canAdd}>
                {t('reminders.add')}
              </button>
            </span>
          </div>
        </form>
      )}

      <div class="rem-scroll">
        {pending.length === 0 && !composing && (
          <div class="notif-empty">
            <span class="notif-empty-glyph" aria-hidden="true">
              <Icon name="clock" size={20} />
            </span>
            <p class="notif-empty-title">{t('notifications.emptyReminderTitle')}</p>
            <p class="notif-empty-hint dim">{t('reminders.empty')}</p>
          </div>
        )}
        {pending.map((r) => (
          <ReminderRow key={r.id} r={r} now={props.now} busy={busy === String(r.id)} onDelete={remove} />
        ))}
        {fired.length > 0 && (
          <>
            <p class="rem-section">{t('reminders.fired')}</p>
            {fired.map((r) => (
              <ReminderRow key={r.id} r={r} now={props.now} busy={busy === String(r.id)} onDelete={remove} />
            ))}
          </>
        )}
      </div>

      <div class="rem-push">
        {push === 'idle' && (
          <button type="button" class="link-btn" onClick={enableNotifications}>
            {t('reminders.notify')}
          </button>
        )}
        {push === 'on' && <span class="dim">{t('reminders.notifyOn')}</span>}
        {push === 'off' && <span class="dim">{t('reminders.pushOff')}</span>}
        {push === 'unsupported' && <span class="dim">{t('reminders.notifyUnsupported')}</span>}
        {push === 'denied' && <span class="gate-error">{t('reminders.notifyDenied')}</span>}
        {push === 'error' && <span class="gate-error">{t('reminders.notifyError')}</span>}
      </div>
    </div>
  )
}

function ReminderRow(props: { r: Reminder; now: number; busy: boolean; onDelete(id: number): void }) {
  const { r } = props
  const pending = isPending(r)
  const left = r.due_at * 1000 - props.now
  const when = pending
    ? left <= 0
      ? t('reminders.overdue')
      : t('reminders.dueIn', { d: formatDuration(left) })
    : stamp(new Date(r.fired_at * 1000))
  const repeat =
    r.repeat_seconds > 0 ? ' · ' + t('reminders.repeats', { d: formatDuration(r.repeat_seconds * 1000) }) : ''
  return (
    <article class={`notif-row rem-row${pending ? '' : ' fired'}`}>
      <span class={`notif-glyph${pending ? '' : ' dim'}`} aria-hidden="true">
        <Icon name={pending ? 'clock' : 'check'} size={15} />
      </span>
      <div class="notif-body">
        <p class="notif-title">{r.message}</p>
        <p class="notif-detail">
          <span class="mono">{pending ? stamp(new Date(r.due_at * 1000)) : `${t('reminders.firedAt')} ${when}`}</span>
          {repeat} · {r.source}
        </p>
      </div>
      <div class="notif-side">
        {/* Only a pending row needs a countdown chip; a fired one already says
            "触发于 …" in its evidence line, and a second tick beside the trash
            reads as a checkbox. */}
        {pending && (
          <span class={`badge ${left <= 0 ? 'yellow' : 'green'}`}>{when}</span>
        )}
        <button
          type="button"
          class="icon-btn rem-x"
          title={t('reminders.delete')}
          aria-label={t('reminders.delete')}
          disabled={props.busy}
          onClick={() => props.onDelete(r.id)}
        >
          <Icon name="trash" size={13} />
        </button>
      </div>
    </article>
  )
}

type WhenMode = 'relative' | 'absolute'

/** A wall-clock stamp without seconds: reminders fire on the minute, and the
 *  seconds field only competes with the message for width. */
function stamp(d: Date): string {
  try {
    return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(d)
  } catch {
    return d.toLocaleString()
  }
}

/** `datetime-local` speaks the browser's wall clock, not ISO; the `min`
 *  attribute has to be the same dialect or Chrome ignores it. */
function localStamp(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** VAPID keys arrive URL-base64 encoded; PushManager wants raw bytes. */
function urlBase64ToBytes(b64: string): Uint8Array<ArrayBuffer> {
  const pad = '='.repeat((4 - (b64.length % 4)) % 4)
  const raw = atob((b64 + pad).replace(/-/g, '+').replace(/_/g, '/'))
  const out = new Uint8Array(new ArrayBuffer(raw.length))
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i)
  return out
}

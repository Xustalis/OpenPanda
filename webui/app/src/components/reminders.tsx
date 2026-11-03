// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'preact/hooks'
import { ApiError, api, type Reminder } from '../api/client'
import { formatDuration } from '../format/time'
import { useAsync, useChangeSignal, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { Icon } from './icons'
import { toast, toastError } from './toast'
import { confirmDialog } from './confirm'
import { layerAboveOwnsEscape } from './card-dismiss'

/** The reminder system's one door.
 *
 *  提醒 used to be a tab of 设置, which buried a thing you touch several times
 *  a day behind a page whose other tabs you open once a month. It is a bell in
 *  the rail's head now, beside 搜索 — the two things you reach for from
 *  wherever you are — and the bell opens a card instead of navigating: you look
 *  at what is pending, add one, close. The card carries the whole feature
 *  (relative or absolute due time, repeat, the live pending queue, what just
 *  fired, the browser-push switch), so the settings tab could go without
 *  leaving anything behind.
 *
 *  A card, not a page, is also why it dismisses on an outside click: the header
 *  cards in the chat pane are read *while* the thread is used (see
 *  card-dismiss), but nobody reads a reminder list while doing something else. */

/** Raise the card from anywhere — the ⌘K palette, or a `#/reminders` bookmark
 *  saved before the tab went away. No-op until the bell has mounted, which is
 *  the same contract openPalette keeps. */
let raise: (() => void) | null = null
export function openReminders(): void {
  raise?.()
}

/** A reminder is pending while it has not fired yet, or while it repeats: a
 *  recurring row keeps its LAST fire in `fired_at`, so a non-zero stamp is not
 *  a retirement. */
function isPending(r: Reminder): boolean {
  return r.fired_at === 0 || r.repeat_seconds > 0
}

/** Web Push is a property of this browser *and* of the node it talks to: with
 *  `push.enabled: false` there is no VAPID key to subscribe with. `off` is the
 *  node's answer, kept apart from a failed subscription (`error`) because the
 *  footer says different things — one is configuration, the other a retry. */
type PushState = 'checking' | 'idle' | 'on' | 'off' | 'unsupported' | 'denied' | 'error'

export function ReminderBell() {
  useLocaleRerender()
  const [open, setOpen] = useState(false)
  const [reload, setReload] = useState(0)
  // Countdowns are painted from a clock, so they go stale while the card sits
  // open. A 30s beat re-renders them without re-fetching the list.
  const [now, setNow] = useState(() => Date.now())
  const [push, setPush] = useState<PushState>('checking')
  const root = useRef<HTMLDivElement>(null)
  const change = useChangeSignal()
  const { data, error } = useAsync(() => api.reminders(), [], change + reload)
  const pending = (data ?? []).filter(isPending)

  useEffect(() => {
    raise = () => setOpen(true)
    return () => {
      raise = null
    }
  }, [])

  // The switch is a statement about *this* browser on *this* node, so it
  // starts from what the two of them say rather than from an idle "enable it"
  // button. The node is asked first: a 404 here means push is not configured,
  // and offering a subscription that can never be stored would be a lie the
  // first click exposes.
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

  // The console can tell the user a reminder fired even without a push
  // subscription: the change feed already refetches the list when a row
  // fires, so diff each snapshot against the last one and speak up here. The
  // first snapshot only sets the baseline — reminders that fired before the
  // tab opened are history, not news. A recurring row bumps `fired_at` on
  // every occurrence, so it announces each one.
  const seenFired = useRef<Map<number, number> | null>(null)
  useEffect(() => {
    if (!data) return
    const seen = seenFired.current
    if (seen) {
      for (const r of data) {
        if (r.fired_at <= (seen.get(r.id) ?? 0)) continue
        toast(t('reminders.toastFired', { message: r.message }), 'success')
        // A hidden tab shows no toast, and with Web Push off there is no
        // service-worker notification either; fall back to the page's own
        // Notification so a backgrounded console still alerts.
        if (
          push !== 'on' &&
          typeof Notification !== 'undefined' &&
          Notification.permission === 'granted' &&
          document.hidden
        ) {
          try {
            new Notification(t('reminders.title'), { body: r.message, tag: `reminder-${r.id}` })
          } catch {
            /* some platforms refuse page-level notifications; the toast stands */
          }
        }
      }
    }
    const next = new Map<number, number>()
    for (const r of data) if (r.fired_at > 0) next.set(r.id, r.fired_at)
    seenFired.current = next
  }, [data, push])

  useEffect(() => {
    if (!open) return
    setNow(Date.now())
    const id = setInterval(() => setNow(Date.now()), 30_000)
    // Typing into the composer is what opening the card is for; on a phone
    // the focus would summon the keyboard and cover the queue, so only a
    // pointer-sized viewport gets the cursor for free.
    if (window.matchMedia('(min-width: 721px)').matches) {
      requestAnimationFrame(() => root.current?.querySelector<HTMLInputElement>('.reminder-message')?.focus())
    }
    return () => clearInterval(id)
  }, [open])

  // Escape closes the card, and stops there so it cannot also reach the thread
  // drawer behind it. A modal (the delete confirm) owns Escape while it is up,
  // and an outside click on that modal must not tear the card down either.
  useEffect(() => {
    if (!open) return
    const onDown = (e: PointerEvent) => {
      if (root.current?.contains(e.target as Node) || layerAboveOwnsEscape('.reminder-pop')) return
      setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || layerAboveOwnsEscape('.reminder-pop')) return
      e.stopPropagation()
      setOpen(false)
    }
    document.addEventListener('pointerdown', onDown, true)
    document.addEventListener('keydown', onKey, true)
    return () => {
      document.removeEventListener('pointerdown', onDown, true)
      document.removeEventListener('keydown', onKey, true)
    }
  }, [open])

  const label = pending.length
    ? `${t('nav.reminders')} (${pending.length})`
    : t('nav.reminders')

  return (
    <div class="reminder-bell" ref={root}>
      <button
        type="button"
        class={`icon-btn tip-down${open ? ' open' : ''}`}
        data-tip={open ? undefined : t('nav.reminders')}
        aria-label={label}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        <Icon name="bell" />
        {pending.length > 0 && (
          <span class="reminder-bell-dot" aria-hidden="true">
            {pending.length > 9 ? '9+' : pending.length}
          </span>
        )}
      </button>
      {open && (
        <div class="reminder-pop" role="dialog" aria-label={t('reminders.title')}>
          <RemindersCard
            reminders={data}
            error={error}
            now={now}
            push={push}
            onPush={setPush}
            onRetry={() => setReload((v) => v + 1)}
            onAdded={() => setReload((v) => v + 1)}
            onClose={() => setOpen(false)}
          />
        </div>
      )}
    </div>
  )
}

/** How the composer names a due time: a wait, or a wall clock. Only one form
 *  is on screen at a time — two fields joined by "or" read as two questions
 *  that both want an answer. */
type WhenMode = 'relative' | 'absolute'

/** The card's body: compose, then the pending queue, then what just fired. */
function RemindersCard(props: {
  reminders: Reminder[] | null
  error: string | null
  now: number
  push: PushState
  onPush(s: PushState): void
  onRetry(): void
  onAdded(): void
  onClose(): void
}) {
  const [message, setMessage] = useState('')
  const [mode, setMode] = useState<WhenMode>('relative')
  const [minutes, setMinutes] = useState('10')
  const [dueAt, setDueAt] = useState('')
  const [everyMin, setEveryMin] = useState('')
  const [adding, setAdding] = useState(false)

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
      // leave the new row invisible until the card is reopened.
      props.onAdded()
      setMessage('')
      setDueAt('')
      setEveryMin('')
    } catch (err) {
      toastError(err)
    } finally {
      setAdding(false)
    }
  }

  async function remove(id: number) {
    // Deleting a pending reminder cancels it for good — confirm first.
    const ok = await confirmDialog({
      title: t('reminders.deleteTitle'),
      message: t('reminders.deleteMsg'),
      confirmLabel: t('reminders.delete'),
    })
    if (!ok) return
    try {
      await api.deleteReminder(id)
    } catch (e) {
      toastError(e)
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
        props.onPush('denied')
        return
      }
      const reg = await navigator.serviceWorker.ready
      const sub = await reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: urlBase64ToBytes(key),
      })
      await api.pushSubscribe(sub.toJSON())
      props.onPush('on')
    } catch (e) {
      props.onPush(e instanceof ApiError && e.status === 404 ? 'off' : 'error')
      console.warn('push subscribe', e)
    }
  }

  const list = props.reminders ?? []
  const pending = list.filter(isPending)
  const fired = list.filter((r) => !isPending(r))

  return (
    <>
      <div class="reminder-pop-head">
        <span class="reminder-pop-title">{t('reminders.title')}</span>
        <button
          type="button"
          class="icon-btn reminder-pop-close"
          aria-label={t('common.close')}
          onClick={props.onClose}
        >
          <Icon name="x" size={14} />
        </button>
      </div>

      <form class="reminder-form" onSubmit={add}>
        <input
          class="input reminder-message"
          type="text"
          placeholder={t('reminders.messagePlaceholder')}
          value={message}
          onInput={(e) => setMessage((e.target as HTMLInputElement).value)}
          required
        />
        <div class="segmented reminder-mode" role="radiogroup" aria-label={t('reminders.whenLabel')}>
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
          <div class="reminder-line">
            <input
              class="input reminder-minutes"
              type="number"
              min={1}
              step={1}
              inputMode="numeric"
              placeholder="10"
              title={t('reminders.afterMinutes')}
              value={minutes}
              onInput={(e) => setMinutes((e.target as HTMLInputElement).value)}
            />
            <span class="reminder-suffix">{t('reminders.minutes')}</span>
          </div>
        ) : (
          <input
            class="input reminder-at"
            type="datetime-local"
            min={localStamp(new Date(Date.now() + 60_000))}
            aria-label={t('reminders.dueAt')}
            title={t('reminders.dueAt')}
            value={dueAt}
            onInput={(e) => setDueAt((e.target as HTMLInputElement).value)}
          />
        )}
        <input
          class="input reminder-every"
          type="number"
          min={1}
          step={1}
          inputMode="numeric"
          placeholder={t('reminders.everyMin')}
          title={t('reminders.everyMinTitle')}
          value={everyMin}
          onInput={(e) => setEveryMin((e.target as HTMLInputElement).value)}
        />
        <div class="reminder-submit-line">
          {past ? (
            <span class="reminder-preview gate-error">{t('reminders.pastTime')}</span>
          ) : due ? (
            <span class="reminder-preview dim">
              {stamp(due)} · {t('reminders.dueIn', { d: formatDuration(due.getTime() - Date.now()) })}
            </span>
          ) : (
            <span class="reminder-preview dim" />
          )}
          <button class="btn primary reminder-add" type="submit" disabled={!canAdd}>
            {t('reminders.add')}
          </button>
        </div>
      </form>

      <div class="reminder-pop-scroll">
        {props.error ? (
          <div class="reminder-pop-err">
            <p class="dim">{props.error}</p>
            <button class="btn" type="button" onClick={props.onRetry}>
              {t('common.retry')}
            </button>
          </div>
        ) : (
          <>
            <p class="reminder-pop-label">
              {t('reminders.pending')} ({pending.length})
            </p>
            {pending.length === 0 && <p class="reminder-pop-empty dim">{t('reminders.empty')}</p>}
            {pending.map((r) => (
              <ReminderRow key={r.id} r={r} now={props.now} onDelete={remove} />
            ))}
            {fired.length > 0 && (
              <>
                <p class="reminder-pop-label">{t('reminders.fired')}</p>
                {fired.map((r) => (
                  <ReminderRow key={r.id} r={r} now={props.now} onDelete={remove} />
                ))}
              </>
            )}
          </>
        )}
      </div>

      <div class="reminder-pop-foot">
        {props.push === 'idle' && (
          <button type="button" class="link-btn reminder-notify" onClick={enableNotifications}>
            {t('reminders.notify')}
          </button>
        )}
        {props.push === 'on' && <span class="dim">{t('reminders.notifyOn')}</span>}
        {props.push === 'off' && <span class="dim">{t('reminders.pushOff')}</span>}
        {props.push === 'unsupported' && <span class="dim">{t('reminders.notifyUnsupported')}</span>}
        {props.push === 'denied' && <span class="gate-error">{t('reminders.notifyDenied')}</span>}
        {props.push === 'error' && <span class="gate-error">{t('reminders.notifyError')}</span>}
      </div>
    </>
  )
}

function ReminderRow({ r, now, onDelete }: { r: Reminder; now: number; onDelete(id: number): void }) {
  const pending = isPending(r)
  const left = r.due_at * 1000 - now
  const when = pending
    ? left <= 0
      ? t('reminders.overdue')
      : t('reminders.dueIn', { d: formatDuration(left) })
    : stamp(new Date(r.fired_at * 1000))
  const repeat =
    r.repeat_seconds > 0
      ? ' · ' + t('reminders.repeats', { d: formatDuration(r.repeat_seconds * 1000) })
      : ''
  return (
    <div class={`reminder-row${pending ? '' : ' fired'}`}>
      <div class="reminder-info">
        <span class="reminder-msg">{r.message}</span>
        <span class="reminder-meta dim">
          {pending ? stamp(new Date(r.due_at * 1000)) : t('reminders.firedAt') + ' ' + when}
          {repeat} · {r.source}
        </span>
      </div>
      <div class="reminder-side">
        <span class={`badge ${pending ? (left <= 0 ? 'yellow' : 'green') : 'dim'}`}>
          {pending ? when : '✓'}
        </span>
        <button
          type="button"
          class="icon-btn reminder-x"
          title={t('reminders.delete')}
          aria-label={t('reminders.delete')}
          onClick={() => onDelete(r.id)}
        >
          <Icon name="x" size={13} />
        </button>
      </div>
    </div>
  )
}

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

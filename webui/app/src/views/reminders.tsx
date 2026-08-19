import { useState } from 'preact/hooks'
import { api, type Reminder } from '../api/client'
import { useAsync, useChangeSignal, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { ErrorState, PageHeader } from '../components/page'
import { toast, toastError } from '../components/toast'
import { confirmDialog } from '../components/confirm'

/** The reminders view — the web face of the reminder system (design P1-28):
 *  add ("remind me in N minutes" or at an absolute time), watch the pending
 *  queue live (SSE change signal), see what just fired, and enable browser
 *  push so reminders reach the browser even with the tab in the background. */
export function RemindersView() {
  useLocaleRerender()
  const change = useChangeSignal()
  const [tick, setTick] = useState(0)
  const { data: reminders, error } = useAsync(() => api.reminders(), [], change + tick)
  const [message, setMessage] = useState('')
  const [minutes, setMinutes] = useState('')
  const [dueAt, setDueAt] = useState('')
  const [adding, setAdding] = useState(false)
  const [notifyState, setNotifyState] = useState<'idle' | 'on' | 'unsupported' | 'denied' | 'error'>('idle')

  async function add(e: Event) {
    e.preventDefault()
    if (adding || !message.trim()) return
    setAdding(true)
    try {
      if (dueAt) {
        // datetime-local has no timezone; interpret it in the browser's zone.
        await api.createReminder({ message: message.trim(), due_at: new Date(dueAt).toISOString() })
      } else {
        const n = Number(minutes)
        await api.createReminder({ message: message.trim(), after_minutes: n > 0 ? n : 1 })
      }
      setMessage('')
      setMinutes('')
      setDueAt('')
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

  /** Ask for notification permission and subscribe this browser to the
   *  node's Web Push service so reminders fire as OS notifications. */
  async function enableNotifications() {
    if (!('serviceWorker' in navigator) || !('PushManager' in window)) {
      setNotifyState('unsupported')
      return
    }
    try {
      const perm = await Notification.requestPermission()
      if (perm !== 'granted') {
        setNotifyState('denied')
        return
      }
      const { key } = await api.pushKey()
      const reg = await navigator.serviceWorker.ready
      const sub = await reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: urlBase64ToBytes(key),
      })
      await api.pushSubscribe(sub.toJSON())
      setNotifyState('on')
    } catch (e) {
      setNotifyState('error')
      console.warn('push subscribe', e)
    }
  }

  if (error)
    return (
      <ErrorState
        title={t('reminders.title')}
        sub={t('reminders.subtitle')}
        error={error}
        onRetry={() => setTick((v) => v + 1)}
      />
    )

  const pending = (reminders ?? []).filter((r) => r.fired_at === 0)
  const fired = (reminders ?? []).filter((r) => r.fired_at !== 0)

  return (
    <section>
      <PageHeader title={t('reminders.title')} sub={t('reminders.subtitle')} />

      <form class="card reminder-form" onSubmit={add}>
        <input
          class="input reminder-message"
          type="text"
          placeholder={t('reminders.messagePlaceholder')}
          value={message}
          onInput={(e) => setMessage((e.target as HTMLInputElement).value)}
          required
        />
        <input
          class="input reminder-minutes"
          type="number"
          min={1}
          step={1}
          placeholder={t('reminders.minutes')}
          title={t('reminders.afterMinutes')}
          value={minutes}
          onInput={(e) => setMinutes((e.target as HTMLInputElement).value)}
          disabled={!!dueAt}
        />
        <span class="dim reminder-or">{t('reminders.or')}</span>
        <input
          class="input reminder-at"
          type="datetime-local"
          title={t('reminders.dueAt')}
          value={dueAt}
          onInput={(e) => setDueAt((e.target as HTMLInputElement).value)}
        />
        <button class="btn primary" type="submit" disabled={adding || !message.trim()}>
          {t('reminders.add')}
        </button>
      </form>

      <NotifyBanner state={notifyState} onEnable={enableNotifications} />

      <div class="card">
        <h2 class="block-title">
          {t('reminders.pending')} ({pending.length})
        </h2>
        {pending.length === 0 && <p class="dim">{t('reminders.empty')}</p>}
        {pending.map((r) => (
          <ReminderRow key={r.id} r={r} onDelete={remove} />
        ))}
      </div>

      {fired.length > 0 && (
        <div class="card">
          <h2 class="block-title">{t('reminders.fired')}</h2>
          {fired.map((r) => (
            <ReminderRow key={r.id} r={r} onDelete={remove} />
          ))}
        </div>
      )}
    </section>
  )
}

function ReminderRow({ r, onDelete }: { r: Reminder; onDelete(id: number): void }) {
  const due = new Date(r.due_at * 1000)
  const pending = r.fired_at === 0
  const left = r.due_at * 1000 - Date.now()
  let when: string
  if (pending) {
    when =
      left <= 0
        ? t('reminders.overdue')
        : t('reminders.dueIn', { d: formatDuration(left) })
  } else {
    when = new Date(r.fired_at * 1000).toLocaleString()
  }
  return (
    <div class={`reminder-row${pending ? '' : ' fired'}`}>
      <div class="reminder-info">
        <span class="reminder-msg">{r.message}</span>
        <span class="reminder-meta dim">
          {pending ? due.toLocaleString() : t('reminders.firedAt') + ' ' + when} · {r.source}
        </span>
      </div>
      <div class="reminder-side">
        <span class={`badge ${pending ? (left <= 0 ? 'yellow' : 'green') : 'dim'}`}>
          {pending ? when : '✓'}
        </span>
        <button class="btn danger" onClick={() => onDelete(r.id)}>
          {t('reminders.delete')}
        </button>
      </div>
    </div>
  )
}

function NotifyBanner({
  state,
  onEnable,
}: {
  state: 'idle' | 'on' | 'unsupported' | 'denied' | 'error'
  onEnable(): void
}) {
  if (state === 'on') return <p class="test-result ok">{t('reminders.notifyOn')}</p>
  if (state === 'unsupported') return <p class="hint">{t('reminders.notifyUnsupported')}</p>
  if (state === 'denied') return <p class="gate-error">{t('reminders.notifyDenied')}</p>
  if (state === 'error') return <p class="gate-error">{t('reminders.notifyError')}</p>
  return (
    <div class="card notify-card">
      <span class="dim">{t('reminders.notifyHint')}</span>
      <button class="btn" onClick={onEnable}>
        {t('reminders.notify')}
      </button>
    </div>
  )
}

/** "5d 3h" / "2h 05m" / "12m" / "45s" — compact human countdown. */
function formatDuration(ms: number): string {
  const s = Math.max(1, Math.round(ms / 1000))
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${String(m).padStart(2, '0')}m`
  if (m > 0) return `${m}m`
  return `${sec}s`
}

/** VAPID keys arrive URL-base64 encoded; PushManager wants raw bytes. */
function urlBase64ToBytes(b64: string): Uint8Array<ArrayBuffer> {
  const pad = '='.repeat((4 - (b64.length % 4)) % 4)
  const raw = atob((b64 + pad).replace(/-/g, '+').replace(/_/g, '/'))
  const out = new Uint8Array(new ArrayBuffer(raw.length))
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i)
  return out
}

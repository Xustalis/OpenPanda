import { useState } from 'preact/hooks'
import { api, type Task } from '../api/client'
import { useAsync, useChangeSignal, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { StateBadge } from '../components/state-badge'

export function DetailView({ id, onBack }: { id: string; onBack(): void }) {
  useLocaleRerender()
  const change = useChangeSignal()
  const { data: task, error } = useAsync(() => api.task(id), [id], change)
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState('')
  const [rejecting, setRejecting] = useState(false)
  const [rejectReason, setRejectReason] = useState('')

  async function act(fn: () => Promise<unknown>) {
    if (busy) return
    setBusy(true)
    setActionError('')
    try {
      await fn()
    } catch (e) {
      setActionError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  if (error) return <p class="dim">{t('common.error')} ({error})</p>
  if (!task) return <p class="dim">{t('common.loading')}</p>

  const cancellable = !['done', 'failed', 'cancelled', 'expired'].includes(task.state)

  return (
    <section>
      <button class="btn" onClick={onBack}>
        ← {t('detail.back')}
      </button>

      <div class="card detail-head">
        <div class="detail-title">
          <h1 class="page-title">{task.title || task.id}</h1>
          <StateBadge state={task.state} />
        </div>
        <div class="detail-actions">
          {task.state === 'review' && !rejecting && (
            <>
              <button class="btn primary" disabled={busy} onClick={() => act(() => api.approve(task.id))}>
                {t('detail.approve')}
              </button>
              <button class="btn danger" disabled={busy} onClick={() => setRejecting(true)}>
                {t('detail.reject')}
              </button>
            </>
          )}
          {task.state === 'review' && rejecting && (
            <div class="reject-form">
              <input
                class="input"
                type="text"
                placeholder={t('detail.rejectReason')}
                value={rejectReason}
                onInput={(e) => setRejectReason((e.target as HTMLInputElement).value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    act(() => api.reject(task.id, rejectReason.trim() || t('detail.rejectedViaWeb'))).then(() =>
                      setRejecting(false),
                    )
                  }
                  if (e.key === 'Escape') setRejecting(false)
                }}
              />
              <button
                class="btn danger"
                disabled={busy}
                onClick={() =>
                  act(() => api.reject(task.id, rejectReason.trim() || t('detail.rejectedViaWeb'))).then(() =>
                    setRejecting(false),
                  )
                }
              >
                {t('detail.rejectConfirm')}
              </button>
              <button class="btn" disabled={busy} onClick={() => setRejecting(false)}>
                {t('common.cancel')}
              </button>
            </div>
          )}
          {cancellable && (
            <button class="btn" disabled={busy} onClick={() => act(() => api.cancel(task.id))}>
              {t('detail.cancelTask')}
            </button>
          )}
        </div>
        {actionError && <p class="gate-error">{actionError}</p>}
      </div>

      <div class="detail-grid">
        <Field label={t('detail.id')} value={task.id} mono />
        <Field label={t('detail.project')} value={task.project || '—'} />
        <Field label={t('detail.owner')} value={task.owner || '—'} />
        <Field label={t('detail.attempt')} value={task.attempt_id || '—'} mono />
        <Field label={t('detail.created')} value={fmt(task.created_at)} />
        <Field label={t('detail.updated')} value={fmt(task.updated_at)} />
        {task.risk && <Field label={t('detail.risk')} value={task.risk} />}
      </div>

      {task.result && (
        <div class="detail-block">
          <h2>{t('detail.result')}</h2>
          <pre>{task.result}</pre>
        </div>
      )}

      <div class="detail-block">
        <h2>{t('detail.timeline')}</h2>
        {task.events && task.events.length > 0 ? (
          <Timeline events={task.events} />
        ) : (
          <p class="dim">{t('common.empty')}</p>
        )}
      </div>
    </section>
  )
}

function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div class="field">
      <span class="dim">{label}</span>
      <span class={mono ? 'mono' : ''}>{value}</span>
    </div>
  )
}

function Timeline({ events }: { events: NonNullable<Task['events']> }) {
  return (
    <ol class="timeline">
      {events.map((ev, i) => (
        <li key={i}>
          <span class="dim">{new Date(ev.ts * 1000).toLocaleTimeString()}</span>{' '}
          <code>{ev.type}</code>
          {ev.data && ev.data !== '{}' && <pre class="timeline-data">{ev.data}</pre>}
        </li>
      ))}
    </ol>
  )
}

function fmt(iso: string): string {
  return iso ? new Date(iso).toLocaleString() : '—'
}

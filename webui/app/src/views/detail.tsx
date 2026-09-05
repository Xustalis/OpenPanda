import { useState } from 'preact/hooks'
import { api, type Task } from '../api/client'
import { useAsync, useChangeSignal, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { StateBadge } from '../components/state-badge'
import { toast, toastError } from '../components/toast'
import { confirmDialog } from '../components/confirm'
import DecisionOrbit from '../components/orbit'
import { EventTimeline } from '../components/event-timeline'
import { prettifyJson } from '../format/json'

export function DetailView({ id, onBack }: { id: string; onBack(): void }) {
  useLocaleRerender()
  const change = useChangeSignal()
  const [tick, setTick] = useState(0)
  const { data: task, error } = useAsync(() => api.task(id), [id], change + tick)
  const [busy, setBusy] = useState(false)
  const [rejecting, setRejecting] = useState(false)
  const [rejectReason, setRejectReason] = useState('')

  async function act(fn: () => Promise<unknown>, okMsg?: string) {
    if (busy) return
    setBusy(true)
    try {
      await fn()
      if (okMsg) toast(okMsg, 'success')
    } catch (e) {
      toastError(e)
    } finally {
      setBusy(false)
    }
  }

  async function cancelTask() {
    const ok = await confirmDialog({
      title: t('detail.cancelConfirmTitle'),
      message: t('detail.cancelConfirmMsg'),
      confirmLabel: t('detail.cancelTask'),
    })
    if (!ok) return
    await act(() => api.cancel(task!.id), t('detail.cancelledToast'))
  }

  if (error)
    return (
      <section>
        <button class="btn" onClick={onBack}>
          ← {t('detail.back')}
        </button>
        <div class="card error-state u-mt-16">
          <span class="error-state-title">{t('common.loadFail')}</span>
          <span class="dim error-state-detail">{error}</span>
          <div>
            <button class="btn" onClick={() => setTick((v) => v + 1)}>
              {t('common.retry')}
            </button>
          </div>
        </div>
      </section>
    )
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
              <button
                class="btn primary"
                disabled={busy}
                onClick={() => act(() => api.approve(task.id), t('detail.approvedToast'))}
              >
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
            <button class="btn" disabled={busy} onClick={cancelTask}>
              {t('detail.cancelTask')}
            </button>
          )}
        </div>
      </div>

      {/* The orbit tails this task's trace stream over SSE by itself, so
          routing, execution and supervision land here as they happen instead
          of waiting for the next poll of the task row. */}
      <DecisionOrbit task={task} defaultOpen />

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
          <ResultView raw={task.result} />
        </div>
      )}

      <div class="detail-block">
        <h2>{t('detail.timeline')}</h2>
        {task.events && task.events.length > 0 ? (
          <EventTimeline events={task.events} />
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

/** A task result is a JSON blob with a handful of well-known keys — ok,
 *  exit_code, stdout, stderr, agent, plus whatever the supervisor added.
 *  Rendering the blob itself is what made this page read as a wall of JSON:
 *  the useful parts were in there, buried in escaping. This lifts them out and
 *  keeps the original one click away rather than one keypress of devtools.
 *
 *  Anything that is not an object we recognise falls back to the raw text —
 *  a half-understood result shown verbatim beats a blank panel. */
function ResultView({ raw }: { raw: string }) {
  const r = parseResult(raw)
  if (!r) return <pre class="detail-raw">{prettifyJson(raw)}</pre>

  const ok = r.ok !== false
  const exit = typeof r.exit_code === 'number' ? r.exit_code : null
  const agent = text(r.agent)
  const stdout = text(r.stdout)
  const stderr = text(r.stderr)
  const verdict = text(r.verdict)
  const reason = text(r.verdict_reason)

  return (
    <div class="result-view">
      <div class="result-meta">
        <span class={ok ? 'result-ok' : 'result-bad'}>
          {ok ? t('detail.result.ok') : t('detail.result.failed')}
        </span>
        {exit !== null && <span class="dim">{t('detail.result.exit', { n: String(exit) })}</span>}
        {agent && <span class="dim">{t('detail.result.agent', { agent })}</span>}
      </div>

      {verdict && <p class="dim">{t('detail.result.verdict', { verdict })}</p>}
      {reason && <p class="dim result-reason">{reason}</p>}

      {stdout && (
        <div class="result-block">
          <span class="dim">{t('detail.result.stdout')}</span>
          <pre>{stdout}</pre>
        </div>
      )}
      {stderr && (
        <div class="result-block">
          <span class="dim">{t('detail.result.stderr')}</span>
          <pre class="result-err">{stderr}</pre>
        </div>
      )}

      <details class="raw-toggle">
        <summary class="dim">{t('detail.result.raw')}</summary>
        <pre class="detail-raw">{prettifyJson(raw)}</pre>
      </details>
    </div>
  )
}

/** Parses a result blob into an object, or returns null when it is not one.
 *  Callers treat null as "show the text as-is". */
function parseResult(raw: string): Record<string, unknown> | null {
  try {
    const v: unknown = JSON.parse(raw)
    return v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : null
  } catch {
    return null
  }
}

function text(v: unknown): string {
  return typeof v === 'string' ? v : ''
}

function fmt(iso: string): string {
  return iso ? new Date(iso).toLocaleString() : '—'
}

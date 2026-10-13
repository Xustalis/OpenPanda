// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from 'preact/hooks'
import { api, isTaskStalled, type ApprovalScope } from '../api/client'
import { useAsync, useChangeSignal, useLocaleRerender, useVisibleInterval } from '../hooks'
import { t } from '../i18n'
import { ScopeSelect } from '../components/scope-select'
import { StateBadge } from '../components/state-badge'
import { toast, toastError } from '../components/toast'
import { confirmDialog } from '../components/confirm'
import DecisionOrbit from '../components/orbit'
import { EventTimeline } from '../components/event-timeline'
import { Markdown } from '../md/render'
import { Icon } from '../components/icons'
import { prettifyJson } from '../format/json'

export function DetailView({
  id,
  onBack,
  onOpenSession,
}: {
  id: string
  onBack(): void
  onOpenSession?(id: string): void
}) {
  useLocaleRerender()
  const change = useChangeSignal()
  const [tick, setTick] = useState(0)
  const { data: task, error } = useAsync(() => api.task(id), [id], change + tick)
  // A stalled task emits no SSE event, so the badge can only appear on a
  // local clock tick.
  useVisibleInterval(() => setTick((v) => v + 1), 30_000)
  const [busy, setBusy] = useState(false)
  const [rejecting, setRejecting] = useState(false)
  const [rejectReason, setRejectReason] = useState('')
  // Where the review answer is remembered — "once" by default; session and
  // project write a standing gate answer for what comes after.
  const [scope, setScope] = useState<ApprovalScope>('once')

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

  async function revertTask() {
    const ok = await confirmDialog({
      title: t('detail.revertConfirmTitle'),
      message: t('detail.revertConfirmMsg'),
      confirmLabel: t('detail.revert'),
    })
    if (!ok) return
    await act(async () => {
      const r = await api.taskRevert(task!.id)
      toast(
        t('detail.revertedToast', {
          restored: String(r.restored?.length ?? 0),
          removed: String(r.removed?.length ?? 0),
        }),
        'success',
      )
    })
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
          {task.session_id && onOpenSession && (
            <button
              type="button"
              class="btn primary"
              onClick={() => onOpenSession(task.session_id!)}
            >
              <Icon name="message" size={13} /> {t('detail.openSession')}
            </button>
          )}
          {task.state === 'review' && task.approval_disposition && (
            <span class="dim review-kind">{t(`detail.reviewKind.${task.approval_disposition}`)}</span>
          )}
          {task.state === 'review' && !rejecting && (
            <>
              <ScopeSelect value={scope} onChange={setScope} disabled={busy} />
              <button
                class="btn primary"
                disabled={busy || task.approval_disposition === 'needs_changed_input'}
                onClick={() => act(() => api.approve(task.id, scope), t('detail.approvedToast'))}
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
                    act(() =>
                      api.reject(task.id, rejectReason.trim() || t('detail.rejectedViaWeb'), scope),
                    ).then(() => setRejecting(false))
                  }
                  if (e.key === 'Escape') setRejecting(false)
                }}
              />
              <ScopeSelect value={scope} onChange={setScope} disabled={busy} />
              <button
                class="btn danger"
                disabled={busy}
                onClick={() =>
                  act(() =>
                    api.reject(task.id, rejectReason.trim() || t('detail.rejectedViaWeb'), scope),
                  ).then(() => setRejecting(false))
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
          {!cancellable && task.state !== 'expired' && (
            <button class="btn danger" disabled={busy} onClick={revertTask}>
              {t('detail.revert')}
            </button>
          )}
        </div>
      </div>

      {isTaskStalled(task) && (
        // The row has not moved past its own bound — nothing is consuming,
        // expiring, or answering for it. Surface that instead of letting the
        // open state sit silently.
        <div class="card warn-state u-mt-16">
          <span class="badge yellow">{t('task.stalled')}</span>
          <span class="dim">{t('task.stalledHint')}</span>
        </div>
      )}

      {/* The orbit tails this task's trace stream over SSE by itself, so
          routing, execution and supervision land here as they happen instead
          of waiting for the next poll of the task row. */}
      <DecisionOrbit task={task} defaultOpen />

      {/* What the run was asked to produce — target, guardrails, scope and
          the bar the supervisor judges against. Without this card the spec
          only existed as a JSON blob users had to decode. */}
      <SpecCard spec={task.spec} />

      {/* Structured delegation chain from the ledger: each hop is one leg of
          the task's trip across nodes (originator → executor → …). Unacked
          hops render hollow — the outbound leg fired but no node confirmed. */}
      {task.delegation_chain && task.delegation_chain.length > 0 && (
        <div class="detail-block">
          <h2>{t('detail.chain')}</h2>
          <div class="chain">
            <span class="chain-node root">{task.delegation_chain[0]?.from_node}</span>
            {task.delegation_chain.map((hop, i) => (
              <span key={i} style="display:contents">
                <span class="chain-arrow" aria-hidden="true">→</span>
                <span
                  class={`chain-node${hop.accepted ? '' : ' unacked'}`}
                  title={`hop ${hop.hop}${hop.via ? ` · ${hop.via}` : ''} · ${fmt(new Date(hop.ts * 1000).toISOString())}`}
                >
                  {hop.to_node}
                </span>
              </span>
            ))}
          </div>
        </div>
      )}

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

/** The task spec (spec_json) as a readable brief: target as prose,
 *  constraints as a checklist, scope as file chips, success as the bar the
 *  supervisor evaluates. Absent or unparseable specs render nothing —
 *  older tasks simply have no brief. */
function SpecCard({ spec }: { spec?: string }) {
  const s = spec ? parseResult(spec) : null
  if (!s) return null
  const target = text(s.target)
  const success = text(s.success_definition)
  const scope = text(s.scope)
  const constraints = Array.isArray(s.constraints)
    ? s.constraints.filter((c): c is string => typeof c === 'string' && c !== '')
    : []
  if (!target && !success && !scope && constraints.length === 0) return null
  const scopeFiles = scope
    .split(/[,;\n]/)
    .map((f) => f.trim())
    .filter(Boolean)
  return (
    <div class="detail-block spec-card">
      <h2>{t('detail.spec.title')}</h2>
      {target && <p class="spec-target">{target}</p>}
      {constraints.length > 0 && (
        <ul class="spec-constraints">
          {constraints.map((c, i) => (
            <li key={i}>
              <Icon name="check" size={12} class="spec-constraint-ico" />
              <span>{c}</span>
            </li>
          ))}
        </ul>
      )}
      {(scopeFiles.length > 0 || success) && (
        <div class="spec-foot">
          {scopeFiles.length > 0 && (
            <div class="spec-scope">
              <span class="dim spec-foot-label">{t('detail.spec.scope')}</span>
              <div class="spec-scope-files">
                {scopeFiles.map((f, i) => (
                  <code key={i} class="result-file-chip" title={f}>
                    {f}
                  </code>
                ))}
              </div>
            </div>
          )}
          {success && (
            <div class="spec-success">
              <span class="dim spec-foot-label">{t('detail.spec.success')}</span>
              <span class="spec-success-text">{success}</span>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

/** A task result is a JSON blob with a handful of well-known keys — ok,
 *  exit_code, stdout, stderr, agent, plus whatever the supervisor added.
 *  Rendering the blob itself is what made this page read as a wall of JSON:
 *  the useful parts were in there, buried in escaping. This lifts them out,
 *  renders the agent's markdown the way it was written, and keeps the
 *  original one click away rather than one keypress of devtools.
 *
 *  Anything that is not an object we recognise falls back to the raw text —
 *  a half-understood result shown verbatim beats a blank panel. */
function ResultView({ raw }: { raw: string }) {
  const r = parseResult(raw)
  if (!r) return <pre class="detail-raw detail-raw-capped">{raw}</pre>

  const ok = r.ok !== false
  const exit = typeof r.exit_code === 'number' ? r.exit_code : null
  const agent = text(r.agent)
  const model = text(r.model)
  const executor = text(r.executor)
  const injected = r.injected === true
  const stdout = text(r.stdout)
  const stderr = text(r.stderr)
  const verdict = text(r.verdict)
  const reason = text(r.verdict_reason)
  const tokens = typeof r.tokens === 'number' && r.tokens > 0 ? r.tokens : null
  const cost = typeof r.cost === 'number' && r.cost > 0 ? r.cost : null
  const files = Array.isArray(r.files_changed)
    ? r.files_changed
        .filter((f): f is string => typeof f === 'string' && f !== '')
        .filter((f) => !FILE_NOISE.test(f))
    : []

  return (
    <div class="result-view">
      <div class="result-meta">
        <span class={ok ? 'result-ok' : 'result-bad'}>
          {ok ? t('detail.result.ok') : t('detail.result.failed')}
        </span>
        {exit !== null && <span class="dim">{t('detail.result.exit', { n: String(exit) })}</span>}
        {agent && <span class="dim">{t('detail.result.agent', { agent })}</span>}
        {model && <span class="dim">{t('detail.result.model', { model })}</span>}
        {injected && <span class="dim">{t('detail.result.injected')}</span>}
        {executor && <span class="dim">{t('detail.result.node', { node: executor })}</span>}
        {tokens !== null && (
          <span class="dim">{t('detail.result.tokens', { n: String(tokens) })}</span>
        )}
        {cost !== null && (
          <span class="dim">{t('detail.result.cost', { n: cost.toFixed(4) })}</span>
        )}
      </div>

      {verdict && <p class="dim">{t('detail.result.verdict', { verdict })}</p>}
      {reason && <p class="dim result-reason">{reason}</p>}

      {files.length > 0 && (
        <div class="result-files">
          <span class="dim result-files-label">
            {t('detail.result.files', { n: String(files.length) })}
          </span>
          <FileChips files={files} />
        </div>
      )}

      {stdout && <CollapsibleOutput text={stdout} label={t('detail.result.stdout')} />}
      {stderr && (
        <CollapsibleOutput text={stderr} label={t('detail.result.stderr')} error />
      )}

      <details class="raw-toggle">
        <summary class="dim">{t('detail.result.raw')}</summary>
        <pre class="detail-raw">{prettifyJson(raw)}</pre>
      </details>
    </div>
  )
}

/** VCS internals, dependency trees and build output a task's file list
 *  picks up along the way — real on disk, noise on the card. */
const FILE_NOISE = /(?:^|\/|\\)(?:\.git|\.svn|\.hg|node_modules|dist|build|__pycache__|\.next|\.cache)(?:\/|\\|$)|\.DS_Store$/

const FILE_CHIP_CAP = 12

/** Changed-file chips: capped so a large change set reads as a summary
 *  instead of a wall, with the rest one click away. */
function FileChips({ files }: { files: string[] }) {
  const [open, setOpen] = useState(false)
  const shown = open ? files : files.slice(0, FILE_CHIP_CAP)
  const hidden = files.length - shown.length
  return (
    <div class="result-file-list">
      {shown.map((f, i) => (
        <code key={i} class="result-file-chip" title={f}>
          {f}
        </code>
      ))}
      {hidden > 0 && (
        <button type="button" class="result-file-more" onClick={() => setOpen(true)}>
          {t('detail.result.filesMore', { n: String(hidden) })}
        </button>
      )}
    </div>
  )
}

/** Agent output reads as markdown (the adapters report it that way); long
 *  dumps start collapsed so a wall of text never buries the page. */
function CollapsibleOutput({
  text,
  label,
  error = false,
}: {
  text: string
  label: string
  error?: boolean
}) {
  const [open, setOpen] = useState(false)
  const long = text.length > 600
  const shown = open || !long ? text : text.slice(0, 600) + '…'
  return (
    <div class="result-block">
      <span class="dim">{label}</span>
      {error ? (
        <pre class="result-err">{shown}</pre>
      ) : (
        <div class="result-out">
          <Markdown text={shown} />
        </div>
      )}
      {long && (
        <button type="button" class="btn small" onClick={() => setOpen((v) => !v)}>
          {open ? t('detail.result.collapse') : t('detail.result.expand')}
        </button>
      )}
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

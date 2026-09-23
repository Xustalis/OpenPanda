import { useCallback, useEffect, useState } from 'preact/hooks'
import {
  api,
  type AuditEntry,
  type DelegationMetric,
  type DoctorReport,
  type UpdateStatus,
} from '../api/client'
import { useAsync, useChangeSignal, useLocaleRerender, useVisibleInterval } from '../hooks'
import { t } from '../i18n'

/** The system view: version, delegation metrics (`panda metrics`), and the
 *  tamper-evident audit log with chain verification (`panda audit verify`) —
 *  everything that used to be terminal-only operational visibility. */
export function SystemView() {
  useLocaleRerender()
  const change = useChangeSignal()
  const { data: version } = useAsync(() => api.version(), [])
  const { data: metrics, error: metricsError } = useAsync(() => api.metrics(), [], change)
  const { data: audit, error: auditError } = useAsync(() => api.auditEntries(), [], change)
  const [verify, setVerify] = useState<{ ok: boolean; entries?: number; error?: string } | null>(null)
  const [verifying, setVerifying] = useState(false)

  async function runVerify() {
    setVerifying(true)
    try {
      setVerify(await api.verifyAudit())
    } catch (e) {
      setVerify({ ok: false, error: e instanceof Error ? e.message : String(e) })
    } finally {
      setVerifying(false)
    }
  }

  return (
    <section>
      <h1 class="page-title">{t('system.title')}</h1>
      <p class="page-sub">{t('system.subtitle')}</p>

      <div class="system-head">
        <div class="card version-card">
          <span class="dim">{t('system.version')}</span>
          <span class="version-num mono">{version?.version ?? '…'}</span>
          {version?.codename && <span class="dim">“{version.codename}”</span>}
        </div>
        <div class="card audit-card">
          <div class="audit-head">
            <span class="dim">{t('system.auditChain')}</span>
            <button class="btn" disabled={verifying} onClick={runVerify}>
              {verifying ? t('system.verifying') : t('system.verify')}
            </button>
          </div>
          {verify && (
            <p class={`test-result ${verify.ok ? 'ok' : 'bad'}`}>
              {verify.ok
                ? t('system.auditOk', { n: String(verify.entries ?? 0) })
                : `${t('system.auditFail')} ${verify.error ?? ''}`}
            </p>
          )}
        </div>
        <UpdateCard />
      </div>

      <DoctorCard />
      <CostCard />
      <ContextCard />

      <div class="detail-block">
        <h2 class="block-title">{t('system.metrics')}</h2>
        {metricsError && <p class="gate-error">{metricsError}</p>}
        {!metricsError && (!metrics || metrics.length === 0) && <p class="dim">{t('system.noMetrics')}</p>}
        {metrics && metrics.length > 0 && (
          <table class="table">
            <thead>
              <tr>
                <th>{t('system.mTask')}</th>
                <th>{t('system.mDelegator')}</th>
                <th>{t('system.mExecutor')}</th>
                <th>{t('system.mSuccess')}</th>
                <th>{t('system.mLatency')}</th>
                <th>{t('system.mTokens')}</th>
                <th>{t('system.mTime')}</th>
              </tr>
            </thead>
            <tbody>
              {metrics.map((m) => (
                <MetricRow key={m.id} m={m} />
              ))}
            </tbody>
          </table>
        )}
      </div>

      <div class="detail-block">
        <h2 class="block-title">{t('system.auditLog')}</h2>
        {auditError && <p class="gate-error">{auditError}</p>}
        {!auditError && (!audit || audit.length === 0) && <p class="dim">{t('system.noAudit')}</p>}
        {audit && audit.length > 0 && (
          <table class="table">
            <thead>
              <tr>
                <th>{t('system.aTime')}</th>
                <th>{t('system.aWho')}</th>
                <th>{t('system.aWhat')}</th>
                <th>{t('system.aTarget')}</th>
                <th>{t('system.aResult')}</th>
              </tr>
            </thead>
            <tbody>
              {audit
                .slice()
                .reverse()
                .map((e, i) => (
                  <tr key={i}>
                    <td class="dim">{new Date(e.ts).toLocaleString()}</td>
                    <td class="mono dim">{e.who}</td>
                    <td>
                      <code>{e.what}</code>
                    </td>
                    <td class="mono dim">{e.target}</td>
                    <td>
                      <span class={`badge ${e.result === 'denied' || e.result === 'failed' ? 'red' : 'green'}`}>
                        {e.result}
                      </span>
                    </td>
                  </tr>
                ))}
            </tbody>
          </table>
        )}
      </div>
    </section>
  )
}

function UpdateCard() {
  const [status, setStatus] = useState<UpdateStatus | null>(null)
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    try {
      setStatus(await api.updateStatus())
    } catch {
      // The backend restarts right after apply; ignore the transient miss.
    }
  }, [])

  // Initial fetch once; then poll on a visibility-aware interval. The hook
  // skips ticks while the tab is hidden and de-duplicates overlapping
  // requests, so a background tab costs the backend nothing.
  useEffect(() => {
    void refresh()
  }, [refresh])
  useVisibleInterval(() => void refresh(), 2000)

  async function act(fn: () => Promise<UpdateStatus>) {
    setBusy(true)
    setActionError(null)
    try {
      setStatus(await fn())
    } catch (e) {
      setActionError(e instanceof Error ? e.message : String(e))
      void refresh()
    } finally {
      setBusy(false)
    }
  }

  if (!status) {
    return (
      <div class="card update-card">
        <span class="dim">{t('system.updateTitle')}</span>
        <p class="dim">{t('common.loading')}</p>
      </div>
    )
  }

  const stage = status.stage
  const error = actionError ?? (stage === 'error' ? status.error ?? '' : null)

  return (
    <div class="card update-card">
      <span class="dim">{t('system.updateTitle')}</span>

      {stage === 'checking' && <p class="dim">{t('system.updateChecking')}</p>}
      {stage === 'downloading' && <p class="dim">{t('system.updateDownloading')}</p>}
      {stage === 'applying' && <p class="dim">{t('system.updateApplying')}</p>}
      {stage === 'done' && <p class="test-result ok">{t('system.updateDone')}</p>}

      {stage === 'available' && (
        <div class="update-actions">
          <p class="update-note">{t('system.updateAvailable', { latest: status.latest ?? '' })}</p>
          <UpdateNotes notes={status.notes} />
          <button class="btn" disabled={busy} onClick={() => void act(() => api.downloadUpdate())}>
            {t('system.updateDownload')}
          </button>
        </div>
      )}

      {stage === 'staged' && (
        <div class="update-actions">
          <p class="update-note">{t('system.updateStaged', { latest: status.latest ?? '' })}</p>
          <UpdateNotes notes={status.notes} />
          {status.idle ? (
            <button class="btn" disabled={busy} onClick={() => void act(() => api.applyUpdate())}>
              {t('system.updateApply')}
            </button>
          ) : (
            <p class="dim">{t('system.updateWaiting')}</p>
          )}
          <button class="btn" disabled={busy} onClick={() => void act(() => api.cancelUpdate())}>
            {t('system.updateDiscard')}
          </button>
        </div>
      )}

      {(stage === 'idle' || stage === 'error') && (
        <div class="update-actions">
          {stage === 'idle' && <p class="dim">{t('system.updateUpToDate')}</p>}
          {error && (
            <p class="test-result bad">
              {t('system.updateError')} {error}
            </p>
          )}
          <button class="btn" disabled={busy} onClick={() => void act(() => api.checkUpdate())}>
            {t('system.updateCheck')}
          </button>
        </div>
      )}

      <p class="dim update-version">
        {t('system.updateCurrent')}: <span class="mono">{status.current}</span>
      </p>
    </div>
  )
}

/** The latest release's changelog digest — plain lines under a small heading,
 *  shown only when there is something to show. */
function UpdateNotes({ notes }: { notes?: string }) {
  if (!notes) return null
  return (
    <details class="update-notes">
      <summary class="dim">{t('system.updateNotesTitle')}</summary>
      <pre>{notes}</pre>
    </details>
  )
}

/** The `/doctor` self-check suite: each check renders its i18n key label with
 *  the k=v evidence pairs inline, failures first. */
function DoctorCard() {
  const [report, setReport] = useState<DoctorReport | null>(null)
  const [error, setError] = useState('')
  const [running, setRunning] = useState(false)

  const run = useCallback(async () => {
    setRunning(true)
    setError('')
    try {
      setReport(await api.doctor())
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setRunning(false)
    }
  }, [])
  useEffect(() => {
    void run()
  }, [run])

  const checks = report ? [...report.checks].sort((a, b) => Number(a.ok) - Number(b.ok)) : []

  return (
    <div class="detail-block">
      <div class="doctor-head">
        <h2 class="block-title">{t('system.doctor')}</h2>
        {report && (
          <span class={`badge ${report.problems === 0 ? 'green' : 'red'}`}>
            {report.problems === 0
              ? t('system.doctorOk')
              : t('system.doctorProblems', { n: report.problems })}
          </span>
        )}
        <button class="btn small" disabled={running} onClick={() => void run()}>
          {running ? t('system.doctorRunning') : t('system.doctorRerun')}
        </button>
      </div>
      {error && <p class="gate-error">{error}</p>}
      {!error && !report && <p class="dim">{t('common.loading')}</p>}
      {report && (
        <ul class="doctor-list">
          {checks.map((c, i) => (
            <li key={i} class={`doctor-check ${c.ok ? 'ok' : 'bad'}`}>
              <span class={`doctor-mark ${c.ok ? 'ok' : 'bad'}`} aria-hidden="true">
                {c.ok ? '✓' : '✗'}
              </span>
              <span class="doctor-label">{t(c.key, c.key)}</span>
              {c.pairs && c.pairs.length > 0 && (
                <span class="doctor-pairs dim mono">
                  {c.pairs.filter((_, i) => i % 2 === 1).join(' · ')}
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

/** The `/cost` rollup: total spend, calls, success rate, per-executor split. */
function CostCard() {
  const { data: cost, error } = useAsync(() => api.cost(), [])
  return (
    <div class="detail-block">
      <h2 class="block-title">{t('system.cost')}</h2>
      {error && <p class="gate-error">{error}</p>}
      {!error && !cost && <p class="dim">{t('common.loading')}</p>}
      {cost && cost.calls === 0 && <p class="dim">{t('system.noCost')}</p>}
      {cost && cost.calls > 0 && (
        <>
          <div class="cost-stats">
            <Stat label={t('system.costTotal')} value={`$${cost.total_cost_usd.toFixed(4)}`} />
            <Stat label={t('system.costTokens')} value={fmtNum(cost.total_tokens)} />
            <Stat label={t('system.costCalls')} value={String(cost.calls)} />
            <Stat label={t('system.costRate')} value={`${Math.round(cost.success_rate * 100)}%`} />
            {cost.since > 0 && (
              <Stat label={t('system.costSince')} value={new Date(cost.since * 1000).toLocaleDateString()} />
            )}
          </div>
          <table class="table">
            <thead>
              <tr>
                <th>{t('system.mExecutor')}</th>
                <th>{t('system.costCalls')}</th>
                <th>{t('system.mTokens')}</th>
                <th>{t('system.costUsd')}</th>
                <th>{t('system.mSuccess')}</th>
              </tr>
            </thead>
            <tbody>
              {cost.by_executor.map((e) => (
                <tr key={e.executor}>
                  <td class="mono">{e.executor}</td>
                  <td class="dim">{e.calls}</td>
                  <td class="dim">{fmtNum(e.tokens)}</td>
                  <td class="dim">${e.cost_usd.toFixed(4)}</td>
                  <td class="dim">
                    {e.calls > 0 ? `${Math.round((e.successes / e.calls) * 100)}%` : '—'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
    </div>
  )
}

/** The `/context` snapshot: what the next ask will run with. */
function ContextCard() {
  const { data: ctx, error } = useAsync(() => api.contextInfo(), [])
  if (error) return null
  if (!ctx) return null
  const rows: Array<[string, string]> = []
  if (ctx.node_name) rows.push([t('system.ctxNode'), ctx.node_name])
  if (ctx.model?.model) rows.push([t('system.ctxModel'), `${ctx.model.alias} · ${ctx.model.model}`])
  if (ctx.work_dir) rows.push([t('system.ctxWorkDir'), ctx.work_dir])
  if (ctx.project) rows.push([t('system.ctxProject'), ctx.project])
  if (ctx.project_work_dir) rows.push([t('system.ctxProjectDir'), ctx.project_work_dir])
  if (ctx.memory_files !== undefined) rows.push([t('system.ctxMemory'), String(ctx.memory_files)])
  rows.push([t('system.ctxCard'), ctx.has_card ? t('common.yes') : t('common.no')])
  return (
    <div class="detail-block">
      <h2 class="block-title">{t('system.context')}</h2>
      <p class="hint">{t('system.contextHelp')}</p>
      <dl class="ctx-grid">
        {rows.map(([k, v]) => (
          <div key={k} class="ctx-row">
            <dt class="dim">{k}</dt>
            <dd class="mono ctx-val">{v}</dd>
          </div>
        ))}
      </dl>
    </div>
  )
}

function Stat(props: { label: string; value: string }) {
  return (
    <div class="stat">
      <div class="stat-value">{props.value}</div>
      <div class="stat-label dim">{props.label}</div>
    </div>
  )
}

function fmtNum(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 10_000) return `${Math.round(n / 1000)}k`
  return n.toLocaleString()
}

function MetricRow({ m }: { m: DelegationMetric }) {
  return (
    <tr>
      <td class="mono dim">{m.task_id.slice(0, 8)}</td>
      <td class="mono dim">{m.delegator}</td>
      <td class="mono dim">{m.executor}</td>
      <td>
        <span class={`badge ${m.success ? 'green' : 'red'}`}>{m.success ? '✓' : '✗'}</span>
      </td>
      <td class="dim">{m.latency_ms} ms</td>
      <td class="dim">{m.tokens ?? '—'}</td>
      <td class="dim">{new Date(m.created_at).toLocaleString()}</td>
    </tr>
  )
}

export type { AuditEntry }

// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from 'preact/hooks'
import {
  api,
  ApiError,
  displayVersion,
  type AuditEntry,
  type DelegationMetric,
  type DoctorFixResult,
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
          <span class="version-num mono">{version ? displayVersion(version.version, version.codename) : '…'}</span>
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
  const [unavailable, setUnavailable] = useState(false)

  const refresh = useCallback(async () => {
    try {
      setStatus(await api.updateStatus())
      setUnavailable(false)
    } catch (e) {
      // The backend restarts right after apply — ignore the transient miss.
      // A 404 means this panel runs without the self-update endpoints (the
      // standalone sidecar disables them), so say so instead of spinning.
      if (e instanceof ApiError && e.status === 404) setUnavailable(true)
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
        <p class="dim">{unavailable ? t('system.updateUnavailable') : t('common.loading')}</p>
      </div>
    )
  }

  const stage = status.stage
  const error = actionError ?? (stage === 'error' ? status.error ?? '' : null)

  return (
    <div class="card update-card">
      <span class="dim">{t('system.updateTitle')}</span>

      {stage === 'checking' && <p class="dim">{t('system.updateChecking')}</p>}
      {stage === 'downloading' && <div class="update-actions">
        <p class="dim">{t('system.updateDownloading')} {((status.downloaded_bytes ?? 0) / 1048576).toFixed(1)} MiB
          {(status.total_bytes ?? 0) > 0 && ' / ' + ((status.total_bytes ?? 0) / 1048576).toFixed(1) + ' MiB'}
        </p>
        <button class="btn" disabled={busy} onClick={() => void act(() => api.cancelUpdate())}>{t('common.cancel')}</button>
      </div>}
      {stage === 'applying' && <p class="dim">{t('system.updateApplying')}</p>}
      {stage === 'done' && (
        <>
          <p class="test-result ok">{t('system.updateDone')}</p>
          <p class="dim">{t('system.updateDoneHint')}</p>
        </>
      )}

      {stage === 'available' && (
        <div class="update-actions">
          <p class="update-note">{t('system.updateAvailable', { latest: displayVersion(status.latest, status.latest_codename) })}</p>
          <UpdateNotes notes={status.notes} />
          <button class="btn" disabled={busy} onClick={() => void act(() => api.downloadUpdate())}>
            {t('system.updateDownload')}
          </button>
        </div>
      )}

      {stage === 'staged' && (
        <div class="update-actions">
          <p class="update-note">{t('system.updateStaged', { latest: displayVersion(status.latest, status.latest_codename) })}</p>
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
          {stage === 'error' && status.latest && (
            <button class="btn" disabled={busy} onClick={() => void act(() => api.downloadUpdate())}>{t('system.updateDownload')}</button>
          )}
          <button class="btn" disabled={busy} onClick={() => void act(() => api.checkUpdate())}>
            {t('system.updateCheck')}
          </button>
        </div>
      )}

      <p class="dim update-version">
        {t('system.updateCurrent')}: <span class="mono">{displayVersion(status.current, status.current_codename)}</span>
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

/** Flatten the wire's k,v pairs into the object t() interpolates. */
function pairParams(pairs?: string[]): Record<string, string> {
  const out: Record<string, string> = {}
  if (!pairs) return out
  for (let i = 0; i + 1 < pairs.length; i += 2) {
    const k = pairs[i]
    const v = pairs[i + 1]
    if (k !== undefined && v !== undefined) out[k] = v
  }
  return out
}

/** The `/doctor` self-check suite: each check renders its i18n key label with
 *  the k=v evidence pairs inline, failures first. A failure whose check
 *  carries a `fix` id gets a repair button; the one-click button runs every
 *  such repair in a single request. */
function DoctorCard() {
  const [report, setReport] = useState<DoctorReport | null>(null)
  const [error, setError] = useState('')
  const [running, setRunning] = useState(false)
  // Which repair is in flight — 'all' for the one-click button, otherwise the
  // check key. Repairs all touch shared state, so only one runs at a time.
  const [fixing, setFixing] = useState('')
  // Results keyed by check key, kept across the post-fix refresh so a row that
  // just turned green still explains what happened.
  const [results, setResults] = useState<Record<string, DoctorFixResult>>({})

  const run = useCallback(async (keepResults = false) => {
    setRunning(true)
    setError('')
    try {
      setReport(await api.doctor())
      if (!keepResults) setResults({})
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setRunning(false)
    }
  }, [])
  useEffect(() => {
    void run()
  }, [run])

  const fix = useCallback(
    async (keys: string[], exposure: string, tag: string) => {
      setFixing(tag)
      setError('')
      try {
        const out = await api.doctorFix(keys, exposure)
        setResults((prev) => {
          const next = { ...prev }
          for (const r of out) next[r.key] = r
          return next
        })
        // Re-read the suite so the list reflects the repair. Results survive
        // the refresh: a row that turned green should still say why.
        await run(true)
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
      } finally {
        setFixing('')
      }
    },
    [run],
  )

  const checks = report ? [...report.checks].sort((a, b) => Number(a.ok) - Number(b.ok)) : []
  // Two failing rows can share one repair (path.no + persist.no), so count
  // repairs rather than rows: the number is what the button will run.
  const fixable = new Set(checks.filter((c) => !c.ok && c.fix).map((c) => c.fix))
  const busy = running || fixing !== ''
  // A repair can make its row disappear (two PATH rows fold into one), and a
  // confirmation that vanishes with the row it confirms is no confirmation.
  const orphanResults = Object.values(results).filter((r) => !checks.some((c) => c.key === r.key))

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
        <div class="doctor-actions">
          {fixable.size > 0 && (
            <button
              class="btn small primary"
              disabled={busy}
              onClick={() => void fix([], 'loopback', 'all')}
            >
              {fixing === 'all'
                ? t('system.doctorFixing')
                : t('system.doctorFixAll', { n: fixable.size })}
            </button>
          )}
          <button class="btn small" disabled={busy} onClick={() => void run()}>
            {running ? t('system.doctorRunning') : t('system.doctorRerun')}
          </button>
        </div>
      </div>
      {error && <p class="gate-error">{error}</p>}
      {!error && !report && <p class="dim">{t('common.loading')}</p>}
      {orphanResults.length > 0 && (
        // A repair can make its row disappear (two PATH rows fold into one,
        // the exposure row turns into a different pass). Confirming above the
        // list keeps that outcome visible instead of burying it under a long
        // page of green rows.
        <ul class="doctor-fixes">
          {orphanResults.map((r) => (
            <li key={r.key} class={`doctor-fix ${r.ok ? 'ok' : 'bad'}`}>
              {t(r.msg, pairParams(r.pairs))}
              {r.restart ? ` · ${t('system.doctorFixRestart')}` : ''}
            </li>
          ))}
        </ul>
      )}
      {report && (
        <ul class="doctor-list">
          {checks.map((c, i) => {
            const res = results[c.key]
            // The exposure row carries two repairs, so the in-flight tag is
            // key+mode: the button the user pressed says "Fixing…", its
            // sibling just goes quiet.
            const busyLocal = fixing === `${c.key}:loopback`
            const busyLan = fixing === `${c.key}:lan`
            const rowBusy = fixing === c.key || busyLocal || busyLan
            return (
              <li key={`${c.key}-${i}`} class={`doctor-check ${c.ok ? 'ok' : 'bad'}`}>
                <span class={`doctor-mark ${c.ok ? 'ok' : 'bad'}`} aria-hidden="true">
                  {c.ok ? '✓' : '✗'}
                </span>
                <span class="doctor-label">{t(c.key, c.key)}</span>
                {c.pairs && c.pairs.length > 0 && (
                  <span class="doctor-pairs dim mono">
                    {c.pairs.filter((_, j) => j % 2 === 1).join(' · ')}
                  </span>
                )}
                {!c.ok && c.fix && (
                  <span class="doctor-row-actions">
                    {c.fix === 'exposure' ? (
                      // Declaring an exposure mode is a decision, not a
                      // default: keep the mesh on the LAN, or pull every
                      // socket back to this machine.
                      <>
                        <button
                          class="btn small"
                          disabled={busy}
                          onClick={() => void fix([c.key], 'loopback', `${c.key}:loopback`)}
                        >
                          {busyLocal ? t('system.doctorFixing') : t('system.doctorFixLocal')}
                        </button>
                        <button
                          class="btn small"
                          disabled={busy}
                          onClick={() => void fix([c.key], 'lan', `${c.key}:lan`)}
                        >
                          {busyLan ? t('system.doctorFixing') : t('system.doctorFixLan')}
                        </button>
                      </>
                    ) : (
                      <button
                        class="btn small"
                        disabled={busy}
                        onClick={() => void fix([c.key], '', c.key)}
                      >
                        {rowBusy ? t('system.doctorFixing') : t('system.doctorFix')}
                      </button>
                    )}
                  </span>
                )}
                {res && (
                  <span class={`doctor-fix ${res.ok ? 'ok' : 'bad'}`}>
                    {t(res.msg, pairParams(res.pairs))}
                    {res.restart ? ` · ${t('system.doctorFixRestart')}` : ''}
                  </span>
                )}
              </li>
            )
          })}
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

import { useState } from 'preact/hooks'
import { api, type AuditEntry, type DelegationMetric } from '../api/client'
import { useAsync, useChangeSignal, useLocaleRerender } from '../hooks'
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
      </div>

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

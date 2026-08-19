import { useState } from 'preact/hooks'
import { api } from '../api/client'
import { useAsync, useChangeSignal, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { ErrorState, PageHeader } from '../components/page'

export function NodesView() {
  useLocaleRerender()
  const change = useChangeSignal()
  const [tick, setTick] = useState(0)
  const { data: nodes, error } = useAsync(() => api.nodes(), [], change + tick)

  if (error)
    return (
      <ErrorState
        title={t('nav.nodes')}
        sub={t('nodes.subtitle')}
        error={error}
        onRetry={() => setTick((v) => v + 1)}
      />
    )

  return (
    <section>
      <PageHeader title={t('nav.nodes')} sub={t('nodes.subtitle')} />

      {nodes === null ? (
        <p class="dim">
          <span class="spinner spinner-inline" aria-hidden="true" />
          {t('common.loading')}
        </p>
      ) : nodes.length === 0 ? (
        <div class="card">{t('nodes.empty')}</div>
      ) : (
        <div class="node-grid">
          {nodes.map((n) => (
            <div key={n.id} class="card node-card">
              <div class="node-head">
                <span class="node-id mono">{n.id}</span>
                <span class={`badge ${n.status === 'online' ? 'green' : ''}`}>{n.status}</span>
              </div>
              {n.chip && <p class="dim">{n.chip}</p>}
              <p class="dim">
                {t('nodes.lastSeen')}:{' '}
                {n.last_seen === 'never' ? t('nodes.never') : new Date(n.last_seen).toLocaleString()}
              </p>
              {n.abilities.length > 0 && (
                <div class="node-abilities">
                  {n.abilities.slice(0, 8).map((a) => (
                    <span key={a} class="badge">
                      {a}
                    </span>
                  ))}
                  {n.abilities.length > 8 && (
                    <span class="badge">+{n.abilities.length - 8}</span>
                  )}
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </section>
  )
}

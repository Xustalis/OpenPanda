import { useEffect, useState } from 'preact/hooks'
import { api, type MemoryFiles } from '../api/client'
import { useAsync, useLocaleRerender } from '../hooks'
import { t } from '../i18n'

type Tab = 'user' | 'memory' | 'dreams'

/** The memory view — a read-only peek into what the agent remembers about
 *  you (USER.md), what it learned (MEMORY.md), and what it dreamed
 *  (DREAMS.md), plus the node's system clock: the agent has no senses of its
 *  own, so this page shows exactly the "now" its time.now tool reports. */
export function MemoryView() {
  useLocaleRerender()
  const [tick, setTick] = useState(0)
  const { data, error } = useAsync(() => api.memory(), [], tick)
  const [tab, setTab] = useState<Tab>('user')
  const [now, setNow] = useState(() => Date.now())

  // Live clock: anchor on the node's reported time, then tick locally so the
  // display stays useful between refreshes.
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [])

  if (error) {
    return (
      <section>
        <h1 class="page-title">{t('memory.title')}</h1>
        <p class="page-sub dim">{error}</p>
      </section>
    )
  }
  if (!data) {
    return (
      <section>
        <h1 class="page-title">{t('memory.title')}</h1>
        <p class="page-sub dim">{t('common.loading')}</p>
      </section>
    )
  }

  // Offset between the browser clock and the node clock, so the ticking
  // display shows node time, not local time.
  const nodeOffset = data.time ? new Date(data.time).getTime() - Date.now() : 0
  const clock = new Date(now + nodeOffset)
  const files: Record<Tab, string> = {
    user: data.user,
    memory: data.memory,
    dreams: data.dreams,
  }

  return (
    <section>
      <h1 class="page-title">{t('memory.title')}</h1>
      <p class="page-sub">{t('memory.subtitle')}</p>

      <div class="system-head">
        <div class="card version-card">
          <span class="dim">{t('memory.nodeTime')}</span>
          <span class="version-num mono" data-testid="node-clock">
            {clock.toLocaleString()}
          </span>
        </div>
        <div class="card version-card">
          <span class="dim">{t('memory.refreshedAt')}</span>
          <button class="btn" onClick={() => setTick((v) => v + 1)}>
            {t('memory.refresh')}
          </button>
        </div>
      </div>

      <div class="segmented memory-tabs" role="tablist">
        {(['user', 'memory', 'dreams'] as const).map((k) => (
          <button
            key={k}
            role="tab"
            aria-selected={tab === k}
            class={`seg${tab === k ? ' on' : ''}`}
            onClick={() => setTab(k)}
          >
            {t(`memory.${k}`)}
          </button>
        ))}
      </div>

      <div class="card memory-card">
        {files[tab] ? (
          <pre class="memory-content mono">{files[tab]}</pre>
        ) : (
          <p class="dim">{t('memory.empty')}</p>
        )}
      </div>
    </section>
  )
}

export type { MemoryFiles }

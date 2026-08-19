import { useState } from 'preact/hooks'
import { api } from '../api/client'
import { useAsync, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { ErrorState, PageHeader } from '../components/page'
import { toastError } from '../components/toast'

export function ProjectsView({ onOpenProject }: { onOpenProject(name: string): void }) {
  useLocaleRerender()
  const [tick, setTick] = useState(0)
  const { data, error } = useAsync(() => api.projects(), [], tick)
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)

  async function create(e: Event) {
    e.preventDefault()
    const trimmed = name.trim()
    if (!trimmed || busy) return
    setBusy(true)
    try {
      await api.createProject(trimmed)
      setName('')
    } catch (err) {
      toastError(err)
    } finally {
      setBusy(false)
    }
  }

  if (error)
    return (
      <ErrorState
        title={t('nav.projects')}
        sub={t('projects.subtitle')}
        error={error}
        onRetry={() => setTick((v) => v + 1)}
      />
    )

  return (
    <section>
      <PageHeader title={t('nav.projects')} sub={t('projects.subtitle')} />

      <form class="card project-create" onSubmit={create}>
        <input
          class="input"
          placeholder={t('projects.namePlaceholder')}
          value={name}
          onInput={(e) => setName((e.target as HTMLInputElement).value)}
          aria-label={t('projects.namePlaceholder')}
        />
        <button class="btn primary" type="submit" disabled={busy || !name.trim()}>
          {t('projects.create')}
        </button>
      </form>

      {data === null ? (
        <p class="dim">
          <span class="spinner spinner-inline" aria-hidden="true" />
          {t('common.loading')}
        </p>
      ) : data.projects.length === 0 ? (
        <div class="card">{t('projects.empty')}</div>
      ) : (
        <ul class="project-list">
          {data.projects.map((p) => (
            <li key={p}>
              <button class="card project-item" onClick={() => onOpenProject(p)}>
                <span class="project-name">{p}</span>
                <span class="dim">→</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

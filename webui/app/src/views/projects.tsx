import { useState } from 'preact/hooks'
import { api } from '../api/client'
import { useAsync, useLocaleRerender } from '../hooks'
import { t } from '../i18n'

export function ProjectsView({ onOpenProject }: { onOpenProject(name: string): void }) {
  useLocaleRerender()
  const { data, error } = useAsync(() => api.projects(), [])
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [formError, setFormError] = useState('')

  async function create(e: Event) {
    e.preventDefault()
    const trimmed = name.trim()
    if (!trimmed || busy) return
    setBusy(true)
    setFormError('')
    try {
      await api.createProject(trimmed)
      setName('')
    } catch (err) {
      setFormError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  if (error) return <p class="dim">{t('common.error')} ({error})</p>

  return (
    <section>
      <h1 class="page-title">{t('nav.projects')}</h1>
      <p class="page-sub">{t('projects.subtitle')}</p>

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
        {formError && <p class="gate-error">{formError}</p>}
      </form>

      {data === null ? (
        <p class="dim">{t('common.loading')}</p>
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

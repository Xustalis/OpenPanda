import { useEffect, useState } from 'preact/hooks'
import { api, type ProjectDetail } from '../api/client'
import { useAsync, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { ErrorState, PageHeader } from '../components/page'
import { toast, toastError } from '../components/toast'
import { confirmDialog } from '../components/confirm'

// The projects view. It used to be a name list with a create box, because a
// project was a memory file and a name was all there was to show. A project now
// has a work directory, a description and an entered/not-entered state — and the
// entered one governs where the next ask's work lands, in this console and in the
// CLI alike — so the view is a row per project with those three facts and the
// verbs that change them.
export function ProjectsView({ onOpenProject }: { onOpenProject(name: string): void }) {
  useLocaleRerender()
  const [tick, setTick] = useState(0)
  const reload = () => setTick((v) => v + 1)
  const { data, error } = useAsync(() => api.projects(), [], tick)
  const [name, setName] = useState('')
  const [dir, setDir] = useState('')
  const [desc, setDesc] = useState('')
  const [busy, setBusy] = useState(false)
  const [editing, setEditing] = useState<string | null>(null)

  async function run(fn: () => Promise<unknown>) {
    if (busy) return
    setBusy(true)
    try {
      await fn()
      reload()
    } catch (err) {
      toastError(err)
    } finally {
      setBusy(false)
    }
  }

  async function create(e: Event) {
    e.preventDefault()
    const trimmed = name.trim()
    if (!trimmed) return
    await run(async () => {
      await api.createProject({
        name: trimmed,
        work_dir: dir.trim() || undefined,
        description: desc.trim() || undefined,
      })
      setName('')
      setDir('')
      setDesc('')
      toast(t('projects.create') + ': ' + trimmed, 'success')
    })
  }

  if (error)
    return (
      <ErrorState
        title={t('nav.projects')}
        sub={t('projects.subtitle')}
        error={error}
        onRetry={reload}
      />
    )

  // Prefer the detailed rows; fall back to bare names so a node whose database
  // predates the projects table still lists something.
  const rows: ProjectDetail[] =
    data === null
      ? []
      : data.detail && data.detail.length > 0
        ? data.detail
        : data.projects.map((p) => ({
            name: p,
            created_at: '',
            updated_at: '',
            active: p === data.active,
            memory_entries: 0,
            memory_chars: 0,
          }))

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
        <input
          class="input"
          placeholder={t('projects.dirPlaceholder')}
          value={dir}
          onInput={(e) => setDir((e.target as HTMLInputElement).value)}
          aria-label={t('projects.dirPlaceholder')}
        />
        <input
          class="input"
          placeholder={t('projects.descPlaceholder')}
          value={desc}
          onInput={(e) => setDesc((e.target as HTMLInputElement).value)}
          aria-label={t('projects.descPlaceholder')}
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
      ) : rows.length === 0 ? (
        <div class="card">{t('projects.empty')}</div>
      ) : (
        <ul class="project-list">
          {rows.map((p) => (
            <li key={p.name}>
              <ProjectRow
                project={p}
                busy={busy}
                editing={editing === p.name}
                onEdit={() => setEditing(editing === p.name ? null : p.name)}
                onOpen={() => onOpenProject(p.name)}
                onEnter={() => onOpenProject(p.name)}
                onExit={() => run(() => api.exitProject())}
                onSave={(body) =>
                  run(async () => {
                    await api.patchProject(p.name, body)
                    setEditing(null)
                    toast(t('projects.save'), 'success')
                  })
                }
                onRemove={(keepMemory, deleteSessions) =>
                  run(async () => {
                    if (p.active) {
                      await api.exitProject().catch(() => {})
                    }
                    await api.deleteProject(p.name, keepMemory, deleteSessions ? 'delete' : 'keep')
                    toast(t('projects.remove'), 'success')
                  })
                }
              />
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function ProjectRow({
  project,
  busy,
  editing,
  onEdit,
  onOpen,
  onEnter,
  onExit,
  onSave,
  onRemove,
}: {
  project: ProjectDetail
  busy: boolean
  editing: boolean
  onEdit(): void
  onOpen(): void
  onEnter(): void
  onExit(): void
  onSave(body: { name?: string; work_dir?: string; description?: string }): void
  onRemove(keepMemory: boolean, deleteSessions: boolean): void
}) {
  const [name, setName] = useState(project.name)
  const [dir, setDir] = useState(project.work_dir ?? '')
  const [desc, setDesc] = useState(project.description ?? '')
  const [keepMemory, setKeepMemory] = useState(false)
  const [deleteSessions, setDeleteSessions] = useState(false)

  // Keep form fields synchronized whenever the project changes or edit toggles
  useEffect(() => {
    setName(project.name)
    setDir(project.work_dir ?? '')
    setDesc(project.description ?? '')
  }, [project.name, project.work_dir, project.description, editing])

  const handleDirectRemove = async () => {
    const ok = await confirmDialog({
      title: t('projects.confirmRemove').replace('{name}', project.name),
      message: `${project.name} (${project.work_dir || t('projects.noDir')}) - 本地目录与源码文件将完整保留。`,
      confirmLabel: t('projects.remove'),
      danger: true,
    })
    if (!ok) return
    onRemove(keepMemory, deleteSessions)
  }

  return (
    <div class={`card project-item${project.active ? ' project-active' : ''}`}>
      <div class="project-head">
        <button class="project-name-btn" onClick={onOpen} title={t('projects.enter')}>
          <span class="project-name">{project.name}</span>
        </button>
        {project.active && <span class="badge">{t('projects.current')}</span>}
        <span class="grow" />
        {project.active ? (
          <button class="btn" onClick={onExit} disabled={busy}>
            {t('projects.exit')}
          </button>
        ) : (
          <button class="btn primary" onClick={onEnter} disabled={busy}>
            {t('projects.enter')}
          </button>
        )}
        <button class="btn" onClick={onEdit} disabled={busy}>
          {editing ? t('common.cancel') : t('projects.rename')}
        </button>
        <button class="btn danger" onClick={handleDirectRemove} disabled={busy} title={t('projects.remove')}>
          {t('projects.remove')}
        </button>
      </div>

      <div class="project-meta dim">
        <span>{project.work_dir ? project.work_dir : t('projects.noDir')}</span>
        {project.description && <span> · {project.description}</span>}
        <span>
          {' · '}
          {t('projects.memory')
            .replace('{entries}', String(project.memory_entries))
            .replace('{chars}', String(project.memory_chars))}
        </span>
        {project.sessions !== undefined && (
          <span>
            {' · '}
            {t('projects.sessionsCount', { count: String(project.sessions) })}
          </span>
        )}
      </div>

      {editing && (
        <div class="project-edit">
          <input
            class="input"
            value={name}
            onInput={(e) => setName((e.target as HTMLInputElement).value)}
            aria-label={t('projects.namePlaceholder')}
          />
          <input
            class="input"
            placeholder={t('projects.dirPlaceholder')}
            value={dir}
            onInput={(e) => setDir((e.target as HTMLInputElement).value)}
            aria-label={t('projects.dirPlaceholder')}
          />
          <input
            class="input"
            placeholder={t('projects.descPlaceholder')}
            value={desc}
            onInput={(e) => setDesc((e.target as HTMLInputElement).value)}
            aria-label={t('projects.descPlaceholder')}
          />
          <div class="project-edit-actions">
            <button
              class="btn primary"
              disabled={busy || !name.trim()}
              onClick={() => onSave({ name: name.trim(), work_dir: dir.trim(), description: desc.trim() })}
            >
              {t('projects.save')}
            </button>
            <label class="check">
              <input
                type="checkbox"
                checked={keepMemory}
                onChange={(e) => setKeepMemory((e.target as HTMLInputElement).checked)}
              />
              {t('projects.keepMemory')}
            </label>
            {project.sessions !== undefined && project.sessions > 0 && (
              <label class="check">
                <input
                  type="checkbox"
                  checked={deleteSessions}
                  onChange={(e) => setDeleteSessions((e.target as HTMLInputElement).checked)}
                />
                {t('projects.deleteSessions')}
              </label>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

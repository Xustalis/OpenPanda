// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from 'preact/hooks'
import { api, type ApprovalScope, type ProjectDetail } from '../api/client'
import { useAsync, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { ErrorState, PageHeader } from '../components/page'
import { Icon } from '../components/icons'
import { toast, toastError } from '../components/toast'
import { confirmDialog } from '../components/confirm'
import { DirPicker } from '../components/dir-picker'
import { Select } from '../components/select'

// The projects view. It used to be a name list with a create box, because a
// project was a memory file and a name was all there was to show. A project now
// has a work directory, a description and an entered/not-entered state — and the
// entered one governs where the next ask's work lands, in this console and in the
// CLI alike — so the view is a row per project with those three facts and the
// verbs that change them.
export function ProjectsView({
  head = true,
  onOpenProject,
  onOpenQueue,
}: {
  /** Paint the page title/subtitle. False when 队列·任务·项目 hosts the board: the
   *  merged page owns the heading, and two titles for one board read as a
   *  duplicate. Everything below the heading is untouched either way. */
  head?: boolean
  onOpenProject(name: string): void
  onOpenQueue(project: string): void
}) {
  useLocaleRerender()
  const [tick, setTick] = useState(0)
  const reload = () => setTick((v) => v + 1)
  const { data, error } = useAsync(() => api.projects(), [], tick)
  const [name, setName] = useState('')
  const [dir, setDir] = useState('')
  const [desc, setDesc] = useState('')
  const [createOpen, setCreateOpen] = useState(false)
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
      setCreateOpen(false)
      toast(t('projects.create') + ': ' + trimmed, 'success')
    })
  }

  if (error)
    return (
      <ErrorState
        head={head}
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
      {head && <PageHeader title={t('nav.projects')} sub={t('projects.subtitle')} />}

      <div class="projects-toolbar">
        <span class="projects-total">
          <Icon name="folder" size={15} />
          {t('projects.total', { n: rows.length })}
          {data?.active && <span class="projects-active-name">· {data.active}</span>}
        </span>
        <button
          type="button"
          class={`btn${createOpen ? '' : ' primary'}`}
          onClick={() => setCreateOpen(!createOpen)}
        >
          <Icon name={createOpen ? 'x' : 'plus'} size={14} />
          {createOpen ? t('common.cancel') : t('projects.new')}
        </button>
      </div>

      {createOpen && (
        <form class="card project-create-card" onSubmit={create}>
          <div class="project-create-row dir-selection">
            <DirPicker
              value={dir}
              onChange={(selected) => setDir(selected)}
              onSuggestName={(suggested) => {
                if (!name.trim()) setName(suggested)
              }}
              placeholder={t('projects.dirPickerPlaceholder')}
            />
          </div>
          <div class="project-create-row details">
            <input
              class="input project-name-input"
              placeholder={t('projects.namePlaceholder')}
              value={name}
              onInput={(e) => setName((e.target as HTMLInputElement).value)}
              aria-label={t('projects.namePlaceholder')}
            />
            <input
              class="input project-desc-input"
              placeholder={t('projects.descPlaceholder')}
              value={desc}
              onInput={(e) => setDesc((e.target as HTMLInputElement).value)}
              aria-label={t('projects.descPlaceholder')}
            />
            <button class="btn primary" type="submit" disabled={busy || !name.trim()}>
              <Icon name="plus" size={14} />
              {t('projects.create')}
            </button>
          </div>
        </form>
      )}

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
                onOpenQueue={() => onOpenQueue(p.name)}
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
                onForgetApproval={() =>
                  run(async () => {
                    await api.clearProjectApproval(p.name)
                    toast(t('projects.approvalForgot'), 'success')
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

// Open-task states, minus review (which wants attention, not patience): a
// project row counts these as "in flight".
const OPEN_STATES = ['submitted', 'queued', 'dispatched', 'waiting_context', 'running']

function ProjectRow({
  project,
  busy,
  editing,
  onEdit,
  onOpen,
  onOpenQueue,
  onSave,
  onRemove,
  onForgetApproval,
}: {
  project: ProjectDetail
  busy: boolean
  editing: boolean
  onEdit(): void
  onOpen(): void
  onOpenQueue(): void
  onSave(body: {
    name?: string
    work_dir?: string
    description?: string
    approval_mode?: 'inherit' | 'never' | 'on-request' | 'always' | ''
    approval_scope?: ApprovalScope
  }): void
  onRemove(keepMemory: boolean, deleteSessions: boolean): void
  onForgetApproval(): void
}) {
  const [name, setName] = useState(project.name)
  const [dir, setDir] = useState(project.work_dir ?? '')
  const [desc, setDesc] = useState(project.description ?? '')
  const [approvalMode, setApprovalMode] = useState<'inherit' | 'never' | 'on-request' | 'always'>(
    project.approval_mode || 'inherit',
  )
  const [approvalScope, setApprovalScope] = useState<ApprovalScope>(project.approval_scope || 'session')
  const [keepMemory, setKeepMemory] = useState(false)
  const [deleteSessions, setDeleteSessions] = useState(false)

  // Keep form fields synchronized whenever the project changes or edit toggles
  useEffect(() => {
    setName(project.name)
    setDir(project.work_dir ?? '')
    setDesc(project.description ?? '')
    setApprovalMode(project.approval_mode || 'inherit')
    setApprovalScope(project.approval_scope || 'session')
  }, [project.name, project.work_dir, project.description, project.approval_mode, project.approval_scope, editing])

  const handleDirectRemove = async () => {
    const ok = await confirmDialog({
      title: t('projects.confirmRemove').replace('{name}', project.name),
      message: `${project.name} (${project.work_dir || t('projects.noDir')}) — ${t('projects.removeKeepNote')}`,
      confirmLabel: t('projects.remove'),
      danger: true,
    })
    if (!ok) return
    onRemove(keepMemory, deleteSessions)
  }

  return (
    <div class={`card project-item${project.active ? ' project-active' : ''}`}>
      <div class="project-head">
        <button class="project-name-btn" onClick={onOpen} title={t('projects.openChat')}>
          <span class="project-name-icon">
            <Icon name={project.active ? 'folder-open' : 'folder-closed'} size={18} />
          </span>
          <span class="project-name">{project.name}</span>
        </button>
        {project.active && <span class="badge green">{t('projects.current')}</span>}
        <span class="grow" />
        <button
          class="btn small ghost icon-only"
          onClick={onEdit}
          disabled={busy}
          title={editing ? t('common.cancel') : t('projects.edit')}
          aria-label={editing ? t('common.cancel') : t('projects.edit')}
        >
          <Icon name={editing ? 'x' : 'pencil'} size={14} />
        </button>
        <button
          class="btn small ghost danger icon-only"
          onClick={handleDirectRemove}
          disabled={busy}
          title={t('projects.remove')}
          aria-label={t('projects.remove')}
        >
          <Icon name="trash" size={14} />
        </button>
      </div>

      {project.description && <p class="project-description">{project.description}</p>}

      <div class="project-dir-tag" title={project.work_dir || t('projects.noDir')}>
        <Icon name="folder" size={13} />
        <span>{project.work_dir || t('projects.noDir')}</span>
        {project.work_dir && (
          <button
            type="button"
            class="copy-dir-btn"
            title={t('projects.copyDir')}
            aria-label={t('projects.copyDir')}
            onClick={(e) => {
              e.stopPropagation()
              navigator.clipboard.writeText(project.work_dir!)
              toast(t('projects.copiedDir'), 'info')
            }}
          >
            <Icon name="copy" size={12} />
          </button>
        )}
      </div>

      <div class="project-stats">
        {project.sessions !== undefined && (
          <span class="project-stat" title={t('projects.sessionsCount', { count: String(project.sessions) })}>
            <Icon name="message" size={13} />
            {t('projects.sessionsCount', { count: String(project.sessions) })}
          </span>
        )}
        <span class="project-stat">
          <Icon name="lightbulb" size={13} />
          {t('projects.memory')
            .replace('{entries}', String(project.memory_entries))
            .replace('{chars}', String(project.memory_chars))}
        </span>
        {project.tasks &&
          (() => {
            const open = OPEN_STATES.reduce((n, s) => n + (project.tasks![s] ?? 0), 0)
            const review = project.tasks['review'] ?? 0
            const failed = project.tasks['failed'] ?? 0
            const done = project.tasks['done'] ?? 0
            if (open + review + failed + done === 0) return null
            return (
              <button type="button" class="project-stat project-task-stat" onClick={onOpenQueue}>
                <Icon name="activity" size={13} />
                {[
                  open > 0 && t('projects.tasksOpen', { n: String(open) }),
                  review > 0 && t('projects.tasksReview', { n: String(review) }),
                  failed > 0 && t('projects.tasksFailed', { n: String(failed) }),
                  open + review + failed === 0 && done > 0 && t('projects.tasksDone', { n: String(done) }),
                ]
                  .filter(Boolean)
                  .join(' · ')}
              </button>
            )
          })()}
      </div>

      {(project.approval_mode || project.approval_decision) && (
        <div class="project-policy">
          {project.approval_mode && (
            <span>
              <Icon name="shield" size={12} />
              {t('projects.approvalModeShort')}: {t(`settings.approval.${project.approval_mode}`)}
            </span>
          )}
          {project.approval_decision && (
            <span>
              {t('projects.approvalRemembered', {
                decision: t(`approval.decision.${project.approval_decision}`),
              })}{' '}
              <button class="link-btn" type="button" disabled={busy} onClick={onForgetApproval}>
                {t('projects.approvalForget')}
              </button>
            </span>
          )}
        </div>
      )}

      <div class="project-card-actions">
        <button class="btn primary project-chat-btn" onClick={onOpen} disabled={busy}>
          <Icon name="message" size={14} />
          {t('projects.openChat')}
        </button>
        <button class="btn" onClick={onOpenQueue} disabled={busy}>
          <Icon name="list-checks" size={14} />
          {t('projects.viewTasks')}
        </button>
      </div>

      {editing && (
        <div class="project-edit">
          <input
            class="input"
            value={name}
            onInput={(e) => setName((e.target as HTMLInputElement).value)}
            aria-label={t('projects.namePlaceholder')}
          />
          <div class="project-edit-dir">
            <DirPicker
              value={dir}
              onChange={(p) => setDir(p)}
              placeholder={t('projects.dirPickerPlaceholder')}
            />
          </div>
          <input
            class="input"
            placeholder={t('projects.descPlaceholder')}
            value={desc}
            onInput={(e) => setDesc((e.target as HTMLInputElement).value)}
            aria-label={t('projects.descPlaceholder')}
          />
          {/* The project's own tier-2 policy: an explicit mode overrides the
              global approval.mode for everything in this project, and the
              scope is where its approval cards preselect their remember. */}
          <div class="project-edit-approval">
            <label class="dim approval-field">
              {t('projects.approvalMode')}
              <Select
                ariaLabel={t('projects.approvalMode')}
                value={approvalMode}
                options={[
                  { value: 'inherit', label: t('projects.approvalInherit') },
                  { value: 'never', label: t('settings.approval.never') },
                  { value: 'on-request', label: t('settings.approval.on-request') },
                  { value: 'always', label: t('settings.approval.always') },
                ]}
                onChange={(v) => setApprovalMode(v as typeof approvalMode)}
              />
            </label>
            <label class="dim approval-field">
              {t('projects.approvalScope')}
              <Select
                ariaLabel={t('projects.approvalScope')}
                value={approvalScope}
                options={[
                  { value: 'once', label: t('approval.scopeOnce') },
                  { value: 'session', label: t('approval.scopeSession') },
                  { value: 'project', label: t('approval.scopeProject') },
                ]}
                onChange={(v) => setApprovalScope(v as ApprovalScope)}
              />
            </label>
          </div>
          <div class="project-edit-actions">
            <button
              class="btn primary"
              disabled={busy || !name.trim()}
              onClick={() =>
                onSave({
                  name: name.trim(),
                  work_dir: dir.trim(),
                  description: desc.trim(),
                  approval_mode: approvalMode,
                  approval_scope: approvalScope,
                })
              }
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

import { useMemo, useState } from 'preact/hooks'
import { api, type Task } from '../api/client'
import { useAsync, useChangeSignal, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { StateBadge } from '../components/state-badge'

export function QueueView({ onOpen }: { onOpen(id: string): void }) {
  useLocaleRerender()
  const change = useChangeSignal()
  const [stateFilter, setStateFilter] = useState('')
  const [projectFilter, setProjectFilter] = useState('')

  const { data: tasks, error } = useAsync(
    () => api.tasks({ state: stateFilter || undefined, project: projectFilter || undefined }),
    [stateFilter, projectFilter],
    change,
  )

  const projects = useMemo(() => {
    const set = new Set((tasks ?? []).map((task) => task.project).filter(Boolean))
    return [...set].sort()
  }, [tasks])

  if (error) return <p class="dim">{t('common.error')} ({error})</p>

  return (
    <section>
      <h1 class="page-title">{t('nav.queue')}</h1>
      <p class="page-sub">{t('queue.subtitle')}</p>

      <div class="filters">
        <select class="input" value={stateFilter} onChange={(e) => setStateFilter((e.target as HTMLSelectElement).value)}>
          <option value="">{t('queue.allStates')}</option>
          {['submitted', 'queued', 'dispatched', 'waiting_context', 'running', 'review', 'done', 'failed', 'cancelled', 'expired'].map((s) => (
            <option key={s} value={s}>
              {t(`state.${s}`, s)}
            </option>
          ))}
        </select>
        <select class="input" value={projectFilter} onChange={(e) => setProjectFilter((e.target as HTMLSelectElement).value)}>
          <option value="">{t('queue.allProjects')}</option>
          {projects.map((p) => (
            <option key={p} value={p}>
              {p}
            </option>
          ))}
        </select>
      </div>

      {tasks === null ? (
        <p class="dim">{t('common.loading')}</p>
      ) : tasks.length === 0 ? (
        <div class="card">{t('queue.empty')}</div>
      ) : (
        <table class="table">
          <thead>
            <tr>
              <th>{t('queue.title')}</th>
              <th>{t('queue.project')}</th>
              <th>{t('queue.state')}</th>
              <th>{t('queue.owner')}</th>
              <th>{t('queue.updated')}</th>
            </tr>
          </thead>
          <tbody>
            {tasks.map((task) => (
              <TaskRow key={task.id} task={task} onOpen={onOpen} />
            ))}
          </tbody>
        </table>
      )}
    </section>
  )
}

function TaskRow({ task, onOpen }: { task: Task; onOpen(id: string): void }) {
  return (
    <tr onClick={() => onOpen(task.id)} class="row-link">
      <td class="cell-title">{task.title || task.id}</td>
      <td>{task.project || '—'}</td>
      <td>
        <StateBadge state={task.state} />
      </td>
      <td class="dim">{task.owner || '—'}</td>
      <td class="dim">
        <time>{task.updated_at ? new Date(task.updated_at).toLocaleString() : ''}</time>
      </td>
    </tr>
  )
}

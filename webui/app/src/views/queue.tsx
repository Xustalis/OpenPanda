import { useMemo, useState } from 'preact/hooks'
import { api, type Task } from '../api/client'
import { useAsync, useChangeSignal, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { StateBadge } from '../components/state-badge'

// The queue is a kanban board (design §11.2): the user never navigates a task
// tree — they see what is waiting, what is running, what needs their approval,
// and what just finished. Grouping is by state; dependencies stay a system
// concern and live in the task detail view.
const COLUMNS: { key: 'todo' | 'doing' | 'review' | 'done'; states: string[] }[] = [
  { key: 'todo', states: ['submitted', 'queued', 'dispatched', 'waiting_context'] },
  { key: 'doing', states: ['running'] },
  { key: 'review', states: ['review'] },
  { key: 'done', states: ['done', 'failed', 'cancelled', 'expired'] },
]

// Cap per column: recent history stays scannable without an endless wall of
// finished cards (design §11.2 shows "DONE · last 24h"; a count cap is the
// simpler equivalent for now).
const DONE_COLUMN_LIMIT = 20

export function QueueView({ onOpen }: { onOpen(id: string): void }) {
  useLocaleRerender()
  const change = useChangeSignal()
  const [projectFilter, setProjectFilter] = useState('')

  const { data: tasks, error } = useAsync(
    () => api.tasks({ project: projectFilter || undefined }),
    [projectFilter],
    change,
  )

  const projects = useMemo(() => {
    const set = new Set((tasks ?? []).map((task) => task.project).filter(Boolean))
    return [...set].sort()
  }, [tasks])

  if (error) return <p class="dim">{t('common.error')} ({error})</p>

  const byColumn = new Map<string, Task[]>()
  for (const col of COLUMNS) byColumn.set(col.key, [])
  for (const task of tasks ?? []) {
    for (const col of COLUMNS) {
      if (col.states.includes(task.state)) {
        byColumn.get(col.key)!.push(task)
        break
      }
    }
  }

  return (
    <section class="queue-section">
      <h1 class="page-title">{t('nav.queue')}</h1>
      <p class="page-sub">{t('queue.subtitle')}</p>

      <div class="filters">
        <select
          class="input"
          value={projectFilter}
          onChange={(e) => setProjectFilter((e.target as HTMLSelectElement).value)}
        >
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
      ) : (
        // The board always renders its four columns — an empty board should
        // still show the shape (todo → doing → review → done), not a blank
        // page with a hint.
        <div class="kanban">
          {COLUMNS.map((col) => (
            <KanbanColumn key={col.key} colKey={col.key} tasks={byColumn.get(col.key)!} onOpen={onOpen} />
          ))}
        </div>
      )}
    </section>
  )
}

function KanbanColumn({
  colKey,
  tasks,
  onOpen,
}: {
  colKey: 'todo' | 'doing' | 'review' | 'done'
  tasks: Task[]
  onOpen(id: string): void
}) {
  const shown = colKey === 'done' ? tasks.slice(0, DONE_COLUMN_LIMIT) : tasks
  const hidden = tasks.length - shown.length

  return (
    <div class={`kanban-col col-${colKey}`} data-testid={`kanban-${colKey}`}>
      <h2 class="kanban-head">
        {t(`queue.col.${colKey}`)}
        <span class="kanban-count">{tasks.length}</span>
      </h2>
      {shown.length === 0 ? (
        <p class="kanban-empty dim">{t('queue.colEmpty')}</p>
      ) : (
        shown.map((task) => <KanbanCard key={task.id} task={task} onOpen={onOpen} />)
      )}
      {hidden > 0 && (
        <p class="dim kanban-more">{t('queue.more', { n: String(hidden) })}</p>
      )}
    </div>
  )
}

function KanbanCard({ task, onOpen }: { task: Task; onOpen(id: string): void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  async function act(fn: () => Promise<unknown>) {
    if (busy) return
    setBusy(true)
    setError('')
    try {
      await fn()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  const isReview = task.state === 'review'
  const finished = ['done', 'failed', 'cancelled', 'expired'].includes(task.state)

  return (
    <div class={`kanban-card${finished ? ' finished' : ''}`} onClick={() => onOpen(task.id)}>
      <div class="kanban-card-top">
        <span class="kanban-title">{task.title || task.id}</span>
        <StateBadge state={task.state} />
      </div>
      <div class="kanban-meta dim">
        {task.project ? <span class="kanban-project">{task.project}</span> : <span>—</span>}
        {task.owner && <span class="kanban-owner">{task.owner}</span>}
        <span>{task.updated_at ? new Date(task.updated_at).toLocaleString() : ''}</span>
      </div>
      {isReview && (
        // Approval is the whole point of the review column (design §11.2):
        // act on the card itself, no detour into the detail page.
        <div class="kanban-actions" onClick={(e) => e.stopPropagation()}>
          <button class="btn primary small" disabled={busy} onClick={() => act(() => api.approve(task.id))}>
            {t('detail.approve')}
          </button>
          <button
            class="btn danger small"
            disabled={busy}
            onClick={() => act(() => api.reject(task.id, t('detail.rejectedViaWeb')))}
          >
            {t('detail.reject')}
          </button>
        </div>
      )}
      {error && <p class="gate-error kanban-error">{error}</p>}
    </div>
  )
}

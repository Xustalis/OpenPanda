import { useMemo, useState } from 'preact/hooks'
import { api, ApiError, type Task } from '../api/client'
import { useAsync, useChangeSignal, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { StateBadge } from '../components/state-badge'
import { ErrorState } from '../components/page'
import { confirmDialog } from '../components/confirm'
import { toast, toastError } from '../components/toast'

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

// States the server will delete (`core.Deletable`): not yet claimed or already
// finished. A moving task shows no × — cancel is its action, not delete.
const DELETABLE_STATES = new Set(['submitted', 'queued', 'done', 'failed', 'cancelled', 'expired'])

// Cap per column: recent history stays scannable without an endless wall of
// finished cards (design §11.2 shows "DONE · last 24h"; a count cap is the
// simpler equivalent for now).
const DONE_COLUMN_LIMIT = 8

// Priority wire labels → sort weight; mirrors the scheduler's policy order.
const PRIO_WEIGHT: Record<string, number> = { high: 0, normal: 1, low: 2 }
type Priority = NonNullable<Task['priority']>
const PRIO_CYCLE: Priority[] = ['high', 'normal', 'low']

/** Board ordering = the scheduler's policy: drag seq (0 = untouched, sorts
 *  last), then priority, then FIFO by creation. Applied per column. */
function sortTasks(tasks: Task[]): Task[] {
  return [...tasks].sort((a, b) => {
    const as = a.seq ?? 0
    const bs = b.seq ?? 0
    if (as > 0 || bs > 0) {
      if (as === 0) return 1
      if (bs === 0) return -1
      if (as !== bs) return as - bs
    }
    const ap = PRIO_WEIGHT[a.priority ?? 'normal'] ?? 1
    const bp = PRIO_WEIGHT[b.priority ?? 'normal'] ?? 1
    if (ap !== bp) return ap - bp
    return (a.created_at ?? '').localeCompare(b.created_at ?? '')
  })
}

export function QueueView({
  onOpen,
  onOpenSession,
}: {
  onOpen(id: string): void
  onOpenSession(id: string): void
}) {
  useLocaleRerender()
  const change = useChangeSignal()
  const [refresh, setRefresh] = useState(0)
  const [projectFilter, setProjectFilter] = useState('')
  const bump = () => setRefresh((v) => v + 1)

  const { data: tasks, error } = useAsync(
    () => api.tasks({ project: projectFilter || undefined }),
    [projectFilter],
    change + refresh,
  )

  const projects = useMemo(() => {
    const set = new Set((tasks ?? []).map((task) => task.project).filter(Boolean))
    return [...set].sort()
  }, [tasks])

  /** One-click board wipe: confirm first (it cancels running work), then
   *  DELETE /api/tasks and refresh from server truth. */
  const [busyClear, setBusyClear] = useState(false)
  async function clearQueue() {
    if (!tasks?.length || busyClear) return
    const ok = await confirmDialog({
      title: t('queue.clearConfirmTitle'),
      message: t('queue.clearConfirmMsg'),
      confirmLabel: t('queue.clear'),
      danger: true,
    })
    if (!ok) return
    setBusyClear(true)
    try {
      const res = await api.clearTasks()
      toast(t('queue.clearDone', { c: String(res.cancelled), d: String(res.deleted) }))
    } catch (e) {
      toastError(e)
    } finally {
      setBusyClear(false)
    }
    bump()
  }

  if (error)
    return (
      <ErrorState
        title={t('nav.queue')}
        sub={t('queue.subtitle')}
        error={error}
        onRetry={bump}
      />
    )

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

      <div class="queue-toolbar">
        <NewTaskForm projects={projects} onCreated={bump} />
        {(tasks?.length ?? 0) > 0 && (
          <button class="btn danger queue-clear" disabled={busyClear} onClick={() => void clearQueue()}>
            {t('queue.clear')}
          </button>
        )}
      </div>

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
            <KanbanColumn
              key={col.key}
              colKey={col.key}
              tasks={sortTasks(byColumn.get(col.key)!)}
              onOpen={onOpen}
              onOpenSession={onOpenSession}
              onMutated={bump}
            />
          ))}
        </div>
      )}
    </section>
  )
}

/** The board's "new task" form: title is the card, prompt doubles as the
 *  task intent and the linked session's first message. */
function NewTaskForm({ projects, onCreated }: { projects: string[]; onCreated(): void }) {
  const [open, setOpen] = useState(false)
  const [title, setTitle] = useState('')
  const [prompt, setPrompt] = useState('')
  const [priority, setPriority] = useState<Task['priority']>('normal')
  const [project, setProject] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  async function submit(e: Event) {
    e.preventDefault()
    if (busy || !title.trim()) return
    setBusy(true)
    setError('')
    try {
      await api.createTask({
        title: title.trim(),
        prompt: prompt.trim() || undefined,
        priority,
        project: project || undefined,
      })
      setTitle('')
      setPrompt('')
      setPriority('normal')
      setProject('')
      setOpen(false)
      onCreated()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  if (!open) {
    return (
      <button class="btn primary queue-new" onClick={() => setOpen(true)}>
        + {t('queue.new')}
      </button>
    )
  }
  return (
    <form class="card queue-form" onSubmit={submit}>
      <div class="field-row">
        <div class="field-group">
          <label for="nt-title">{t('queue.newTitle')}</label>
          <input
            id="nt-title"
            class="input"
            type="text"
            required
            value={title}
            onInput={(e) => setTitle((e.target as HTMLInputElement).value)}
          />
        </div>
        <div class="field-group">
          <label for="nt-priority">{t('queue.newPriority')}</label>
          <select
            id="nt-priority"
            class="input"
            value={priority}
            onChange={(e) => setPriority((e.target as HTMLSelectElement).value as Task['priority'])}
          >
            {PRIO_CYCLE.map((p) => (
              <option key={p} value={p}>
                {t(`queue.priority.${p}`)}
              </option>
            ))}
          </select>
        </div>
        <div class="field-group">
          <label for="nt-project">{t('queue.newProject')}</label>
          <select
            id="nt-project"
            class="input"
            value={project}
            onChange={(e) => setProject((e.target as HTMLSelectElement).value)}
          >
            <option value="">{t('queue.allProjects')}</option>
            {projects.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </select>
        </div>
      </div>
      <div class="field-group">
        <label for="nt-prompt">{t('queue.newPrompt')}</label>
        <textarea
          id="nt-prompt"
          class="input"
          rows={2}
          value={prompt}
          onInput={(e) => setPrompt((e.target as HTMLTextAreaElement).value)}
        />
      </div>
      {error && <p class="gate-error">{error}</p>}
      <div class="settings-actions">
        <button class="btn primary" type="submit" disabled={busy || !title.trim()}>
          {busy ? t('queue.creating') : t('queue.create')}
        </button>
        <button class="btn" type="button" onClick={() => setOpen(false)}>
          {t('common.cancel')}
        </button>
      </div>
    </form>
  )
}

// Drag-and-drop state shared by one column's cards: which task is being
// dragged and where the drop would land. Kept per column — columns reorder
// independently (design: 看板四列各自独立排序).
interface DragState {
  dragID: string
  overIndex: number
}

function KanbanColumn({
  colKey,
  tasks,
  onOpen,
  onOpenSession,
  onMutated,
}: {
  colKey: 'todo' | 'doing' | 'review' | 'done'
  tasks: Task[]
  onOpen(id: string): void
  onOpenSession(id: string): void
  onMutated(): void
}) {
  const [drag, setDrag] = useState<DragState | null>(null)
  const [expanded, setExpanded] = useState(false)
  const limit = colKey === 'done' && !expanded ? DONE_COLUMN_LIMIT : tasks.length
  const shown = colKey === 'done' ? tasks.slice(0, limit) : tasks
  const hidden = tasks.length - shown.length

  /** Drop: splice the dragged card out and reinsert at the hovered slot,
   *  then persist the full column order (seq 1..n server-side). */
  async function drop() {
    if (!drag) return
    const from = shown.findIndex((task) => task.id === drag.dragID)
    let to = drag.overIndex
    if (from === -1) {
      setDrag(null)
      return
    }
    if (from < to) to -= 1 // removing the dragged card shifts the target up
    const next = [...shown]
    const [moved] = next.splice(from, 1)
    if (!moved) {
      setDrag(null)
      return
    }
    next.splice(to, 0, moved)
    setDrag(null)
    const ids = next.map((task) => task.id)
    // Also keep never-shown done tasks out of the reorder payload: the API
    // rewrites seq for exactly the ids it receives.
    if (ids.length > 1) {
      try {
        await api.reorderTasks(ids)
        onMutated()
      } catch {
        onMutated() // refresh from server truth either way
      }
    }
  }

  return (
    <div
      class={`kanban-col col-${colKey}`}
      data-testid={`kanban-${colKey}`}
      onDragOver={(e) => e.preventDefault()}
      onDrop={(e) => {
        e.preventDefault()
        void drop()
      }}
    >
      <h2 class="kanban-head">
        {t(`queue.col.${colKey}`)}
        <span class="kanban-count">{tasks.length}</span>
      </h2>
      <div
        class={`kanban-list${
          colKey === 'done' && expanded ? ' kanban-list--dense' : ''
        }`}
      >
        {shown.length === 0 ? (
          <p class="kanban-empty dim">{t('queue.colEmpty')}</p>
        ) : (
          shown.map((task, i) => (
            <KanbanCard
              key={task.id}
              task={task}
              index={i}
              dropBefore={drag != null && drag.dragID !== task.id && drag.overIndex === i}
              onOpen={onOpen}
              onOpenSession={onOpenSession}
              onMutated={onMutated}
              onDragStart={(id) => setDrag({ dragID: id, overIndex: i })}
              onDragOver={(index) => setDrag((d) => (d ? { ...d, overIndex: index } : d))}
              onDragEnd={() => setDrag(null)}
            />
          ))
        )}
      </div>
      {(hidden > 0 || (expanded && shown.length > DONE_COLUMN_LIMIT)) && (
        <button
          class={`kanban-expand-btn${expanded ? ' is-open' : ''}`}
          onClick={() => setExpanded((v) => !v)}
        >
          <span class="kanban-expand-count">
            {expanded
              ? t('queue.collapse')
              : t('queue.more', { n: String(hidden) })}
          </span>
          <span class="kanban-expand-caret" aria-hidden="true" />
        </button>
      )}
      {drag && <div class="kanban-dropzone" onClick={() => void drop()}>{t('queue.dropHere')}</div>}
    </div>
  )
}

function KanbanCard({
  task,
  index,
  dropBefore,
  onOpen,
  onOpenSession,
  onMutated,
  onDragStart,
  onDragOver,
  onDragEnd,
}: {
  task: Task
  index: number
  dropBefore: boolean
  onOpen(id: string): void
  onOpenSession(id: string): void
  onMutated(): void
  onDragStart(id: string): void
  onDragOver(index: number): void
  onDragEnd(): void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  async function act(fn: () => Promise<unknown>) {
    if (busy) return
    setBusy(true)
    setError('')
    try {
      await fn()
      onMutated()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  /** Cycle high → normal → low → high; the badge shows the current level. */
  function cyclePriority() {
    const current: Priority = task.priority ?? 'normal'
    const next = PRIO_CYCLE[(PRIO_CYCLE.indexOf(current) + 1) % PRIO_CYCLE.length] ?? 'normal'
    void act(() => api.patchTask(task.id, next))
  }

  /** Delete the card's task (and subtree). The server refuses a moving task
   *  with 409 — surfaced as the localized "cancel first" hint, not raw JSON. */
  async function remove() {
    const ok = await confirmDialog({
      title: t('queue.deleteConfirmTitle'),
      message: t('queue.deleteConfirmMsg'),
      confirmLabel: t('queue.deleteTask'),
      danger: true,
    })
    if (!ok) return
    try {
      await api.deleteTask(task.id)
      onMutated()
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        setError(t('queue.deleteActive'))
        return
      }
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const isReview = task.state === 'review'
  const finished = ['done', 'failed', 'cancelled', 'expired'].includes(task.state)
  const deletable = DELETABLE_STATES.has(task.state)
  const open = () => (task.session_id ? onOpenSession(task.session_id) : onOpen(task.id))

  return (
    <div
      class={`kanban-card${finished ? ' finished' : ''}${dropBefore ? ' drop-before' : ''}`}
      role="button"
      tabIndex={0}
      draggable={!finished}
      onClick={open}
      onKeyDown={(e) => {
        // The card doubles as a button for the keyboard; nested controls
        // (priority badge, approve/reject) keep their own keys, so only act
        // when the card itself is the event target.
        if (e.target !== e.currentTarget) return
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          open()
        }
      }}
      onDragStart={(e) => {
        if (e.dataTransfer) {
          e.dataTransfer.effectAllowed = 'move'
          e.dataTransfer.setData('text/plain', task.id)
        }
        onDragStart(task.id)
      }}
      onDragEnd={onDragEnd}
      onDragOver={(e) => {
        e.preventDefault()
        // Pointer in the upper half → insert above this card, else below.
        const rect = (e.currentTarget as HTMLElement).getBoundingClientRect()
        const above = e.clientY < rect.top + rect.height / 2
        onDragOver(above ? index : index + 1)
      }}
    >
      <div class="kanban-card-top">
        <span class="kanban-title">{task.title || task.id}</span>
        {deletable && (
          <button
            class="kanban-del"
            title={t('queue.deleteTask')}
            aria-label={t('queue.deleteTask')}
            disabled={busy}
            onClick={(e) => {
              e.stopPropagation()
              void remove()
            }}
          >
            ×
          </button>
        )}
        <StateBadge state={task.state} />
      </div>
      <div class="kanban-meta dim">
        <button
          class={`prio-badge prio-${task.priority ?? 'normal'}${finished ? ' fixed' : ''}`}
          title={t('queue.priorityHint')}
          disabled={busy || finished}
          onClick={(e) => {
            e.stopPropagation()
            cyclePriority()
          }}
        >
          {t(`queue.priority.${task.priority ?? 'normal'}`)}
        </button>
        {task.project ? <span class="kanban-project">{task.project}</span> : <span>—</span>}
        {task.owner && <span class="kanban-owner">{task.owner}</span>}
        {task.session_id && (
          <span class="kanban-session" title={t('queue.openSession')}>
            💬
          </span>
        )}
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

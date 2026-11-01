// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { api, ApiError, isTaskStalled, type ApprovalScope, type NodeInfo, type Task } from '../api/client'
import { useAsync, useChangeSignal, useLocaleRerender, useTraceForTask, useVisibleInterval } from '../hooks'
import { t } from '../i18n'
import { isNodeConnected } from '../format/node-status'
import { ScopeSelect } from '../components/scope-select'
import { Select, type SelectOption } from '../components/select'
import { StateBadge } from '../components/state-badge'
import { ErrorState } from '../components/page'
import { Icon } from '../components/icons'
import { confirmDialog } from '../components/confirm'
import { useModalFocus } from '../components/modal-focus'
import { ContextMenu, type ContextMenuItem } from '../components/context-menu'
import { toast, toastError } from '../components/toast'
import { fmtElapsed, isActiveState, latestActivity } from '../components/task-activity'
import { PlansView } from './plans'

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
  mode = 'board',
  onModeChange,
  focusReview,
  project,
  onOpen,
  onOpenSession,
  onNewChat,
}: {
  /** Plans are a lens on this same scoped task list, not another page. */
  mode?: 'board' | 'plans'
  onModeChange?(mode: 'board' | 'plans'): void
  /** Deep-link filter (#/control?project=x) — seeds the board's project
   *  filter so a link from a project card lands on that project's work. */
  project?: string | null
  focusReview?: boolean
  onOpen(id: string): void
  onOpenSession(id: string): void
  onNewChat(project: string | null): void
}) {
  useLocaleRerender()
  const change = useChangeSignal()
  const [refresh, setRefresh] = useState(0)
  const [projectFilter, setProjectFilter] = useState(project ?? '')
  // Device filter: '' = every origin, 'local' = this machine, 'unknown' =
  // rows with no recorded origin, otherwise an origin node id (chain[0]).
  const [deviceFilter, setDeviceFilter] = useState('')
  // Re-seed when the route's project param changes — the view stays mounted
  // across hash changes, so the prop is the source of truth for deep links.
  useEffect(() => {
    if (project != null) setProjectFilter(project)
  }, [project])
  // The sidebar badge jumps here in focus-review mode. A device filter left
  // over from an earlier visit would hide every row while the badge still
  // counts > 0, so the board would open empty.
  useEffect(() => {
    if (focusReview) setDeviceFilter('')
  }, [focusReview])
  const bump = () => setRefresh((v) => v + 1)
  // A stalled task emits no SSE event — nothing changes — so the badge can
  // only appear on a local clock tick.
  const [, setStallTick] = useState(0)
  useVisibleInterval(() => setStallTick((v) => v + 1), 30_000)

  const { data: allTasks, error } = useAsync(
    () => api.tasks({ project: projectFilter || undefined }),
    [projectFilter],
    change + refresh,
  )

  // The fleet list maps an origin id to its display name and VM marker for
  // the device dropdown and the card chips. One fetch per mount — the board
  // itself never pays for it — and a failure degrades to raw ids.
  const { data: nodes } = useAsync<NodeInfo[]>(
    () => api.nodes().catch(() => [] as NodeInfo[]),
    [],
  )
  const nodeById = useMemo(() => {
    const m = new Map<string, NodeInfo>()
    for (const n of nodes ?? []) m.set(n.id, n)
    return m
  }, [nodes])
  /** Origin id → display label: the configured node name is the part before
   *  the last '@' in the runtime id, and VM-kind peers carry a marker — the
   *  "which of these is a virtual machine" question the filter exists for. */
  const deviceLabel = (id: string): string => {
    const i = id.lastIndexOf('@')
    const name = i > 0 ? id.slice(0, i) : id
    return nodeById.get(id)?.node_kind === 'vm' ? `${name} · ${t('queue.device.vm')}` : name
  }

  // The device dimension applied on top of the server's project filter —
  // client-side, because the board already holds every row and the census
  // below must see the unfiltered set.
  const tasks = useMemo(() => {
    const list = allTasks ?? []
    if (!deviceFilter) return list
    if (deviceFilter === 'local') return list.filter((task) => task.origin_local)
    if (deviceFilter === 'unknown')
      return list.filter((task) => !task.origin_local && !task.origin_node)
    return list.filter((task) => !task.origin_local && task.origin_node === deviceFilter)
  }, [allTasks, deviceFilter])

  // The composer must offer every project, including one with no tasks yet.
  // Task rows only supplement this list for legacy nodes without a projects
  // table; they are not the source of truth for what can be selected.
  const { data: projectList } = useAsync(() => api.projects().catch(() => null), [])
  const projects = useMemo(() => {
    const set = new Set(projectList?.projects ?? [])
    for (const task of allTasks ?? []) {
      if (task.project) set.add(task.project)
    }
    return [...set].sort((a, b) => a.localeCompare(b))
  }, [allTasks, projectList])

  // Origin census for the dropdown: one bucket for this machine, one per
  // foreign device — noisiest first, so the VM flooding the queue surfaces
  // at the top of the list with its count in the label.
  const devices = useMemo(() => {
    let local = 0
    const counts = new Map<string, number>()
    for (const task of allTasks ?? []) {
      if (task.origin_local) local++
      else {
        const key = task.origin_node ?? ''
        counts.set(key, (counts.get(key) ?? 0) + 1)
      }
    }
    const remote = [...counts.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    return { local, remote }
  }, [allTasks])

  // Sub-agent map: parent_id → number of spawned children. A card that
  // fanned out shows the count; a child card shows the ↳ marker, so the
  // board reads "the parent is orchestrating N sub-agents" instead of
  // rendering children as indistinguishable plain tasks.
  const subagents = useMemo(() => {
    const m = new Map<string, number>()
    for (const task of tasks) {
      if (task.parent_id) m.set(task.parent_id, (m.get(task.parent_id) ?? 0) + 1)
    }
    return m
  }, [tasks])

  /** One-click board wipe: confirm first (it cancels running work), then
   *  DELETE /api/tasks and refresh from server truth. Gated on the unfiltered
   *  set — a device filter must not hide the button that clears everything. */
  const [busyClear, setBusyClear] = useState(false)
  async function clearQueue() {
    if (!allTasks?.length || busyClear) return
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

  // A card's right-click menu. The board's corner control is the way into the
  // conversation now, so the per-task delete — still the only way to drop a
  // task that was queued by mistake — lives here, where a pointer user looks
  // for a row's less frequent actions. The menu is rendered at the board, not
  // inside the card: the card clips its children and lifts on hover, which
  // would crop and drag a `position: fixed` panel with it.
  const [menu, setMenu] = useState<{ task: Task; x: number; y: number } | null>(null)

  /** Delete one task (and its subtree). The server refuses a moving task with
   *  409 — surfaced as the localized "cancel first" hint, not raw JSON. */
  async function removeTask(task: Task) {
    const ok = await confirmDialog({
      title: t('queue.deleteConfirmTitle'),
      message: t('queue.deleteConfirmMsg'),
      confirmLabel: t('queue.deleteTask'),
      danger: true,
    })
    if (!ok) return
    try {
      await api.deleteTask(task.id)
      bump()
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        toast(t('queue.deleteActive'), 'error')
        return
      }
      toastError(e)
    }
  }

  function cardMenuItems(task: Task): ContextMenuItem[] {
    const items: ContextMenuItem[] = []
    if (task.session_id) {
      items.push({
        id: 'session',
        label: t('queue.openSession'),
        icon: 'message',
        onSelect: () => onOpenSession(task.session_id!),
      })
    }
    if (DELETABLE_STATES.has(task.state)) {
      items.push({
        id: 'delete',
        label: t('queue.deleteTask'),
        icon: 'x',
        danger: true,
        onSelect: () => void removeTask(task),
      })
    }
    return items
  }

  if (error)
    return (
      <ErrorState
        head={false}
        title={t('nav.queue')}
        sub={t('queue.subtitle')}
        error={error}
        onRetry={bump}
      />
    )

  const byColumn = new Map<string, Task[]>()
  for (const col of COLUMNS) byColumn.set(col.key, [])
  for (const task of tasks) {
    for (const col of COLUMNS) {
      if (col.states.includes(task.state)) {
        byColumn.get(col.key)!.push(task)
        break
      }
    }
  }

  // The parallel-execution strip: every task actually running right now,
  // with the lane it holds in the shared tree (◫ scoped paths, ⧉ isolated
  // private copy, ▣ the default whole-tree claim). Two or more rows IS the
  // parallel interface — the board otherwise only shows them scattered
  // across columns, which reads as "several tasks" rather than "N agents
  // running at once".
  const running = tasks.filter((task) => task.state === 'running')
  const reviewCount = tasks.filter((task) => task.state === 'review').length
  const plansFiltered = Boolean(projectFilter || deviceFilter)
  const visiblePlanIds = plansFiltered
    ? new Set(tasks.flatMap((task) => (task.plan_meta?.plan_id ? [task.plan_meta.plan_id] : [])))
    : undefined

  return (
    <section class="queue-section">
      {/* One quiet line over the board: the two scopes, the live counts, then
          the page's actions. It replaces a command bar (lens switch + three
          buttons) stacked over a boxed filter panel — two frames, a switch and
          five controls between the page name and the first column. The lens
          switch moved into the page head, the cross-surface routes became the
          icon buttons they always were, and what is left is a caption, not a
          deck. */}
      <div class="queue-bar">
        <div class="filters queue-filters">
          <Select
            ariaLabel={t('queue.filterProject')}
            value={projectFilter}
            options={[
              { value: '', label: t('queue.allProjects') },
              ...projects.map((p) => ({ value: p, label: p })),
            ]}
            onChange={setProjectFilter}
          />
          {(allTasks?.length ?? 0) > 0 && (
            <Select
              ariaLabel={t('queue.filterDevice')}
              value={deviceFilter}
              options={[
                { value: '', label: t('queue.allDevices'), hint: String(allTasks?.length ?? 0) },
                { value: 'local', label: t('queue.device.local'), hint: String(devices.local) },
                ...devices.remote.map(([id, n]) => ({
                  value: id || 'unknown',
                  label: id ? deviceLabel(id) : t('queue.device.unknown'),
                  hint: String(n),
                })),
              ]}
              onChange={setDeviceFilter}
            />
          )}
        </div>
        <div class="queue-bar-end">
          {/* Nothing on the board means nothing to count: an empty queue reads
              "0 进行中 · 0 审核中" otherwise, which is a row of noise about the
              absence of rows. */}
          {(allTasks?.length ?? 0) > 0 && (
            <div class="queue-census" aria-live="polite">
              <span class="doing">{running.length} {t('queue.col.doing')}</span>
              <span class={reviewCount > 0 ? 'review has-work' : 'review'}>
                {reviewCount} {t('queue.col.review')}
              </span>
            </div>
          )}
          <button
            type="button"
            class="icon-btn queue-chat"
            title={t('queue.chatHint')}
            aria-label={t('sessions.new')}
            onClick={() => onNewChat(projectFilter || null)}
          >
            <Icon name="square-pen" size={16} />
          </button>
          {mode === 'board' && (allTasks?.length ?? 0) > 0 && (
            <button
              type="button"
              class="icon-btn queue-clear"
              title={t('queue.clear')}
              aria-label={t('queue.clear')}
              disabled={busyClear}
              onClick={() => void clearQueue()}
            >
              <Icon name="trash" size={16} />
            </button>
          )}
          <NewTaskForm
            projects={projects}
            defaultProject={projectFilter}
            onCreated={() => {
              bump()
              if (mode === 'plans') onModeChange?.('board')
            }}
          />
        </div>
      </div>

      {running.length > 0 && (
        <div class={`parallel-strip${running.length > 1 ? ' active' : ''}`}>
          <span class="parallel-count">
            {running.length > 1
              ? t('queue.parallelStrip.n', { n: String(running.length) })
              : t('queue.parallelStrip.one')}
          </span>
          {running.map((task) => (
            <button
              key={task.id}
              type="button"
              class="parallel-lane"
              onClick={() => onOpen(task.id)}
              title={task.title}
            >
              <span class="parallel-lane-mark">
                {task.isolate ? '⧉' : task.scope_paths && task.scope_paths.length > 0 ? '◫' : '▣'}
              </span>
              <span class="parallel-lane-title">{task.title || task.id}</span>
              {task.isolate ? (
                <span class="parallel-lane-scope">{t('queue.parallelStrip.isolated')}</span>
              ) : task.scope_paths && task.scope_paths.length > 0 ? (
                <span class="parallel-lane-scope">{task.scope_paths.join(', ')}</span>
              ) : null}
            </button>
          ))}
        </div>
      )}

      {mode === 'plans' ? (
        <PlansView head={false} planIds={visiblePlanIds} filtered={plansFiltered} />
      ) : (
        <>
          {/* A device bucket can be empty by definition (this machine submitted
              nothing; everything on the board arrived from a peer). Say so
              instead of leaving four blank columns to be misread as "loading". */}
          {deviceFilter && allTasks !== null && tasks.length === 0 && (
            <p class="dim queue-device-empty">{t('queue.device.none')}</p>
          )}

          {allTasks === null ? (
            <p class="dim">{t('common.loading')}</p>
          ) : (
            // The board always renders its four columns — an empty board should
            // still show the shape (todo → doing → review → done), not a blank
            // page with a hint. In focus-review mode (#/control?review=1, reached
            // via the nav badge) it collapses to the approval column alone.
            <div class={`kanban${focusReview ? ' focus-review' : ''}`}>
              {(focusReview ? COLUMNS.filter((c) => c.key === 'review') : COLUMNS).map((col) => (
                <KanbanColumn
                  key={col.key}
                  colKey={col.key}
                  tasks={sortTasks(byColumn.get(col.key)!)}
                  subagents={subagents}
                  deviceLabel={deviceLabel}
                  onOpen={onOpen}
                  onOpenSession={onOpenSession}
                  onMutated={bump}
                  onCardMenu={(task, x, y) => setMenu({ task, x, y })}
                />
              ))}
            </div>
          )}
          {focusReview && (
            <p class="dim queue-focus-note">
              <a href="#/control">{t('queue.showBoard')}</a>
            </p>
          )}
        </>
      )}
      {menu && (
        <ContextMenu
          x={menu.x}
          y={menu.y}
          title={menu.task.title || menu.task.id}
          sub={t(`state.${menu.task.state}`, menu.task.state)}
          items={cardMenuItems(menu.task)}
          onClose={() => setMenu(null)}
        />
      )}
    </section>
  )
}

/** The board's "new task" composer: title is the card, prompt doubles as the
 *  task intent and the linked session's first message.
 *
 *  It opens as a modal, not as a card inside the header row. Inline, a
 *  four-field form claimed the full width of the toolbar and pushed the four
 *  columns down the page every time someone reached for "new task" — the board
 *  reflowed mid-scan for a form that is not part of the board. As a dialog the
 *  trigger stays one button on the header line and the form floats over the
 *  board, with the console's usual modal contract: focus lands in the title
 *  field, Tab stays inside, Esc / backdrop / ✕ dismiss without losing the
 *  draft. */
function NewTaskForm({
  projects,
  defaultProject,
  onCreated,
}: {
  projects: string[]
  defaultProject: string
  onCreated(): void
}) {
  const [open, setOpen] = useState(false)
  const [title, setTitle] = useState('')
  const [prompt, setPrompt] = useState('')
  const [priority, setPriority] = useState<Task['priority']>('normal')
  const [project, setProject] = useState('')
  const [node, setNode] = useState('')
  // Same-tree parallelism: 'default' claims the whole project; 'paths' claims
  // the typed directories so disjoint siblings can run alongside; 'isolate'
  // runs in a private copy that merges back. 'workdir' is the other axis — a
  // directory of its own, which is how business domains stay apart: it holds
  // only that directory's lock and runs on this node. The first three are
  // hidden until a project is set (nothing to scope without one); 'workdir'
  // needs no project.
  const [parallel, setParallel] = useState<'default' | 'paths' | 'isolate' | 'workdir'>('default')
  const [pathsText, setPathsText] = useState('')
  const [workdirText, setWorkdirText] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // The environment picker lists the live fleet; loaded lazily when the form
  // opens so the board itself never pays for it.
  const { data: nodes } = useAsync<NodeInfo[]>(
    () => (open ? api.nodes().catch(() => [] as NodeInfo[]) : Promise.resolve([])),
    [open],
  )
  const box = useRef<HTMLFormElement>(null)
  const titleInput = useRef<HTMLInputElement>(null)
  const trigger = useRef<HTMLButtonElement>(null)
  const wasOpen = useRef(false)
  // Tab stays inside the dialog while it is open.
  useModalFocus(box, open)
  // …but the trap lands on the ✕ first. A create form starts at the title:
  // the one field the submit button is gated on.
  useEffect(() => {
    if (open) titleInput.current?.focus()
  }, [open])
  // The trap also promises to hand focus back to the opener, and cannot here:
  // the trigger is swapped out for the dialog, so the element it captured is
  // detached by the time the dialog closes. Remember the round trip and focus
  // the re-mounted trigger ourselves.
  useEffect(() => {
    if (open) {
      wasOpen.current = true
      return
    }
    if (!wasOpen.current) return
    wasOpen.current = false
    trigger.current?.focus()
  }, [open])

  /** Dismiss without submitting. A draft survives — an accidental Esc must not
   *  cost the paragraph the user just typed — but a create already in flight
   *  stays put: closing then would report "nothing happened" for a task that
   *  is being created. */
  function close() {
    if (busy) return
    setError('')
    setOpen(false)
  }

  async function submit(e: Event) {
    e.preventDefault()
    if (busy || !title.trim()) return
    // The directory IS the mode: sending none would run the task in the
    // project's own tree while the form still reads "Own directory", which is
    // the one thing a pin exists to rule out.
    if (parallel === 'workdir' && !workdirText.trim()) {
      setError(t('queue.parallel.workdirRequired'))
      return
    }
    setBusy(true)
    setError('')
    try {
      const scopePaths = parallel === 'paths'
        ? pathsText.split(',').map((p) => p.trim()).filter(Boolean)
        : undefined
      const res = await api.createTask({
        title: title.trim(),
        prompt: prompt.trim() || undefined,
        priority,
        project: project || undefined,
        node: node || undefined,
        scope_paths: scopePaths,
        isolate: parallel === 'isolate' ? true : undefined,
        workdir: parallel === 'workdir' ? workdirText.trim() || undefined : undefined,
      })
      // A queued task nobody can drain must not read as dispatched — surface
      // the engine's advisory before clearing the composer.
      if (res.warning) toast(res.warning)
      setTitle('')
      setPrompt('')
      setPriority('normal')
      setProject('')
      setNode('')
      setParallel('default')
      setPathsText('')
      setWorkdirText('')
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
      <button
        ref={trigger}
        class="btn primary queue-new"
        onClick={() => {
          // A fresh draft inherits the board scope; an existing draft keeps
          // the project the user explicitly picked before dismissing it.
          if (!title.trim() && !prompt.trim()) setProject(defaultProject)
          setOpen(true)
        }}
      >
        <Icon name="plus" size={14} />
        {t('queue.new')}
      </button>
    )
  }
  // The two lane modes describe a project's own tree, so they only exist once
  // one is chosen — the same condition the `<option>`s carried.
  const parallelOptions: SelectOption[] = [
    { value: 'default', label: t('queue.parallel.default') },
    ...(project
      ? [
          { value: 'paths', label: t('queue.parallel.paths') },
          { value: 'isolate', label: t('queue.parallel.isolate') },
        ]
      : []),
    { value: 'workdir', label: t('queue.parallel.workdir') },
  ]
  return (
    <div
      class="modal-backdrop"
      onClick={(e) => {
        // Only a click on the backdrop itself dismisses: a drag that ends
        // outside a field must not throw the form away.
        if (e.target === e.currentTarget) close()
      }}
    >
      <form
        ref={box}
        class="modal queue-modal"
        role="dialog"
        aria-modal="true"
        aria-label={t('queue.new')}
        onSubmit={submit}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.stopPropagation()
            close()
          }
        }}
      >
        <div class="queue-modal-head">
          <h2 class="modal-title">{t('queue.new')}</h2>
          <button
            type="button"
            class="btn-icon"
            onClick={close}
            title={t('common.close')}
            aria-label={t('common.close')}
          >
            <Icon name="x" size={16} />
          </button>
        </div>
        {/* The title gets its own line: it is the card's headline and the
            only field submit is gated on, so it should not share a row with
            three selects that all clamp their labels. */}
        <div class="field-group">
          <label for="nt-title">{t('queue.newTitle')}</label>
          <input
            ref={titleInput}
            id="nt-title"
            class="input"
            type="text"
            required
            value={title}
            onInput={(e) => setTitle((e.target as HTMLInputElement).value)}
          />
        </div>
        <div class="field-row">
          <div class="field-group">
            <label for="nt-priority">{t('queue.newPriority')}</label>
            <Select
              id="nt-priority"
              ariaLabel={t('queue.newPriority')}
              value={priority ?? 'normal'}
              options={PRIO_CYCLE.map((p) => ({ value: p, label: t(`queue.priority.${p}`) }))}
              onChange={(v) => setPriority(v as Task['priority'])}
            />
          </div>
          <div class="field-group">
            <label for="nt-project">{t('queue.newProject')}</label>
            <Select
              id="nt-project"
              ariaLabel={t('queue.newProject')}
              value={project}
              options={[
                { value: '', label: t('queue.allProjects') },
                ...projects.map((p) => ({ value: p, label: p })),
              ]}
              onChange={(next) => {
                setProject(next)
                // The lane modes describe the project's own tree, so clearing
                // the project must not leave one selected: the server rejects
                // it and the option is no longer on screen to explain why.
                if (!next && (parallel === 'paths' || parallel === 'isolate')) setParallel('default')
              }}
            />
          </div>
          <div class="field-group">
            <label for="nt-node">{t('queue.newNode')}</label>
            <Select
              id="nt-node"
              ariaLabel={t('queue.newNode')}
              value={node}
              options={[
                { value: '', label: t('queue.nodeAuto') },
                ...(nodes ?? [])
                  .filter((n) => isNodeConnected(n.status))
                  .map((n) => ({
                    value: n.id,
                    label: n.name || n.id,
                    ...(n.is_local ? { hint: t('nodes.self') } : {}),
                  })),
              ]}
              onChange={setNode}
            />
          </div>
        </div>
        <div class="field-group">
          <label for="nt-prompt">{t('queue.newPrompt')}</label>
          <textarea
            id="nt-prompt"
            class="input"
            rows={3}
            value={prompt}
            onInput={(e) => setPrompt((e.target as HTMLTextAreaElement).value)}
          />
        </div>
        {/* A pinned directory needs no project, so the row shows even without
            one — the project-scoped modes are the only options that require it. */}
        <div class="field-row">
          <div class="field-group">
            <label for="nt-parallel">{t('queue.parallelMode')}</label>
            <Select
              id="nt-parallel"
              ariaLabel={t('queue.parallelMode')}
              value={parallel}
              options={parallelOptions}
              onChange={(v) => setParallel(v as typeof parallel)}
            />
          </div>
          {parallel === 'paths' && project && (
            <div class="field-group">
              <label for="nt-paths">{t('queue.parallel.pathsLabel')}</label>
              <input
                id="nt-paths"
                class="input"
                type="text"
                placeholder={t('queue.parallel.pathsPlaceholder')}
                value={pathsText}
                onInput={(e) => setPathsText((e.target as HTMLInputElement).value)}
              />
            </div>
          )}
          {parallel === 'workdir' && (
            <div class="field-group">
              <label for="nt-workdir">{t('queue.parallel.workdirLabel')}</label>
              <input
                id="nt-workdir"
                class="input"
                type="text"
                placeholder={t('queue.parallel.workdirPlaceholder')}
                value={workdirText}
                onInput={(e) => setWorkdirText((e.target as HTMLInputElement).value)}
              />
            </div>
          )}
        </div>
        {parallel === 'workdir' && <p class="hint">{t('queue.parallel.workdirHint')}</p>}
        {error && <p class="gate-error">{error}</p>}
        {/* Cancel then create, the order every other dialog in the console
            uses: the destructive/committing button sits at the far edge. */}
        <div class="settings-actions">
          <button class="btn" type="button" onClick={close} disabled={busy}>
            {t('common.cancel')}
          </button>
          <button class="btn primary" type="submit" disabled={busy || !title.trim()}>
            {busy ? t('queue.creating') : t('queue.create')}
          </button>
        </div>
      </form>
    </div>
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
  subagents,
  deviceLabel,
  onOpen,
  onOpenSession,
  onMutated,
  onCardMenu,
}: {
  colKey: 'todo' | 'doing' | 'review' | 'done'
  tasks: Task[]
  subagents: Map<string, number>
  /** Origin id → display label, from the board's fleet list. */
  deviceLabel(id: string): string
  onOpen(id: string): void
  onOpenSession(id: string): void
  onMutated(): void
  /** Right-click on a card, at viewport coordinates — the board owns the menu
   *  so it is not clipped by the card it belongs to. */
  onCardMenu(task: Task, x: number, y: number): void
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
      data-col={colKey}
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
        data-empty={shown.length === 0}
      >
        {/* An empty column is just its own tinted panel — no "nothing here"
            placeholder. The board is already four labelled lanes, so a dashed
            box inside an empty lane said the same thing twice and, stacked
            under the column's own surface, read as two nested layers. */}
        {shown.map((task, i) => (
          <KanbanCard
            key={task.id}
            task={task}
            index={i}
            childCount={subagents.get(task.id) ?? 0}
            dropBefore={drag != null && drag.dragID !== task.id && drag.overIndex === i}
            deviceLabel={deviceLabel}
            onOpen={onOpen}
            onOpenSession={onOpenSession}
            onMutated={onMutated}
            onCardMenu={onCardMenu}
            onDragStart={(id) => setDrag({ dragID: id, overIndex: i })}
            onDragOver={(index) => setDrag((d) => (d ? { ...d, overIndex: index } : d))}
            onDragEnd={() => setDrag(null)}
          />
        ))}
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
      {drag && (
        <button type="button" class="kanban-dropzone" onClick={() => void drop()}>
          {t('queue.dropHere')}
        </button>
      )}
    </div>
  )
}

function KanbanCard({
  task,
  index,
  childCount,
  dropBefore,
  deviceLabel,
  onOpen,
  onOpenSession,
  onMutated,
  onCardMenu,
  onDragStart,
  onDragOver,
  onDragEnd,
}: {
  task: Task
  index: number
  /** Number of spawned sub-agent children (0 = leaf task). */
  childCount: number
  dropBefore: boolean
  /** Origin id → display label, from the board's fleet list. */
  deviceLabel(id: string): string
  onOpen(id: string): void
  onOpenSession(id: string): void
  onMutated(): void
  onCardMenu(task: Task, x: number, y: number): void
  onDragStart(id: string): void
  onDragOver(index: number): void
  onDragEnd(): void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // Where a review answer is remembered: "once" is the safe default — a
  // session or project remember writes a standing gate answer, which should
  // always be a deliberate pick, not an accident of a default.
  const [scope, setScope] = useState<ApprovalScope>('once')

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

  const isReview = task.state === 'review'
  const finished = ['done', 'failed', 'cancelled', 'expired'].includes(task.state)
  // A card opens the task, never the chat behind it. The board answers "what is
  // this node doing" — clicking through to a conversation answered a different
  // question and left the reader scrolling a transcript to find out whether the
  // run worked. The detail page owns that answer, and the conversation is one
  // deliberate click further: the arrow in the card's corner, or 打开会话 on the
  // detail page.
  const open = () => onOpen(task.id)
  const elapsed = fmtElapsed(task.updated_at)

  return (
    <div
      class={`kanban-card${finished ? ' finished' : ''}${dropBefore ? ' drop-before' : ''}${task.session_id ? ' has-go' : ''}`}
      data-state={task.state}
      role="button"
      tabIndex={0}
      draggable={!finished}
      onClick={open}
      onContextMenu={(e) => {
        // Delete (and the conversation, for a pointer user) live in the menu:
        // the corner belongs to the arrow, so the row's rarer actions moved to
        // the place a right-click already promises.
        e.preventDefault()
        onCardMenu(task, e.clientX, e.clientY)
      }}
      onKeyDown={(e) => {
        // The keyboard's context menu key (and Shift+F10) opens the same menu
        // the pointer gets — delete must not become pointer-only.
        if (e.key === 'ContextMenu' || (e.key === 'F10' && e.shiftKey)) {
          e.preventDefault()
          const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
          onCardMenu(task, r.left + Math.min(r.width, 120), r.top + 24)
          return
        }
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
        <span class="kanban-title">
          {task.parent_id && (
            <span class="kanban-subagent" title={t('queue.subagent')} aria-label={t('queue.subagent')}>
              ↳{task.spawn_depth != null && task.spawn_depth > 1 ? task.spawn_depth : ''}
            </span>
          )}
          {task.isolate && (
            <span class="kanban-subagent" title={t('queue.isolate')} aria-label={t('queue.isolate')}>
              ⧉
            </span>
          )}
          {!task.isolate && task.scope_paths && task.scope_paths.length > 0 && (
            <span class="kanban-subagent" title={t('queue.scoped') + ': ' + task.scope_paths.join(', ')} aria-label={t('queue.scoped')}>
              ◫
            </span>
          )}
          {task.title || task.id}
        </span>
        <StateBadge state={task.state} />
      </div>
      {/* The prompt behind the title — the TUI shows `intent:` as its own
          line, so a card whose title is a terse label still says what it
          actually does. Suppressed when it would repeat the title. */}
      {task.intent && task.intent !== task.title && (
        <div class="kanban-intent dim">{task.intent}</div>
      )}
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
        {task.project && <span class="kanban-project">{task.project}</span>}
        {task.owner && <span class="kanban-owner">@{task.owner}</span>}
        {/* Foreign origin: the device this task CAME FROM (chain[0]). Local
            rows stay unmarked — the default reading of an unmarked card is
            "mine" — so a board flooded by VM submissions shows exactly the
            foreign chips that explain it. */}
        {!task.origin_local && (
          <span
            class="kanban-owner kanban-origin"
            title={task.origin_node ? `${t('queue.originHint')}: ${task.origin_node}` : t('queue.device.unknown')}
          >
            <Icon name="devices" size={11} />
            {task.origin_node ? deviceLabel(task.origin_node) : t('queue.device.unknown')}
          </span>
        )}
        {(task.delegation_chain?.length ?? 0) > 0 && (
          <span
            class="kanban-owner"
            title={t('queue.delegated', { n: String(task.delegation_chain!.length) })}
          >
            <Icon name="network" size={11} /> {task.delegation_chain!.length}
          </span>
        )}
        {childCount > 0 && (
          <span class="kanban-owner" title={t('queue.children', { n: String(childCount) })}>
            <Icon name="bot" size={11} /> {childCount}
          </span>
        )}
        {elapsed && <span>{t('time.ago', { d: elapsed })}</span>}
        {isTaskStalled(task) && (
          // The card is stuck: no consumer, a dead executor, or no monitor.
          // Saying so on the board is the "no silent stall" rule for the web.
          <span class="badge yellow" title={t('task.stalledHint')}>
            {t('task.stalled')}
          </span>
        )}
      </div>
      {isActiveState(task.state) && <LiveLine task={task} />}
      {isReview && (
        // Approval is the whole point of the review column (design §11.2):
        // act on the card itself, no detour into the detail page. The
        // disposition line says what approve will DO — accept finished work,
        // resume a parked run, or refuse until the input changes — instead of
        // a bare "awaiting approval" that used to cover all three.
        <div class="kanban-actions" onClick={(e) => e.stopPropagation()}>
          {task.approval_disposition && (
            <span
              class="dim review-kind"
              title={
                task.approval_disposition === 'needs_changed_input'
                  ? t('detail.needsChangedInputHint')
                  : undefined
              }
            >
              {t(`detail.reviewKind.${task.approval_disposition}`)}
            </span>
          )}
          <ScopeSelect value={scope} onChange={setScope} disabled={busy} />
          <button
            class="btn primary small"
            disabled={busy || task.approval_disposition === 'needs_changed_input'}
            title={
              task.approval_disposition === 'needs_changed_input'
                ? t('detail.needsChangedInputHint')
                : undefined
            }
            onClick={() => act(() => api.approve(task.id, scope))}
          >
            {t('detail.approve')}
          </button>
          <button
            class="btn danger small"
            disabled={busy}
            onClick={() => act(() => api.reject(task.id, t('detail.rejectedViaWeb'), scope))}
          >
            {t('detail.reject')}
          </button>
        </div>
      )}
      {error && <p class="gate-error kanban-error">{error}</p>}
      {/* The card's edge, at half height: step into the conversation this task
          is running in. It replaced the × that used to sit in the corner — on
          a board whose cards are read to find out *what happened*, the
          frequent move is "go look at the transcript", and the destructive one
          now lives behind the card's right-click menu where it cannot be hit
          by accident. Only a task that already has a session gets the arrow: a
          queued row with no transcript yet would have nowhere to go, and a
          control that does nothing is worse than no control. */}
      {task.session_id && (
        <button
          type="button"
          class="kanban-go"
          title={t('queue.openSession')}
          aria-label={t('queue.openSession')}
          onClick={(e) => {
            e.stopPropagation()
            onOpenSession(task.session_id!)
          }}
        >
          <Icon name="arrow-right" size={14} />
        </button>
      )}
    </div>
  )
}

/** One-line "what is it doing right now" strip under an active card — the
 *  TUI's rolling log, compressed. The card seeds from the task's last_event
 *  stamp (attached by listTasks) so a mid-flight task reads real activity on
 *  first paint; SSE frames then keep it moving. Nothing fetches: the shared
 *  trace bus is already open. */
function LiveLine({ task }: { task: Task }) {
  const traces = useTraceForTask(task.id, task.last_event ? [task.last_event] : [])
  const line = latestActivity(traces, task)
  if (!line) return null
  return (
    <div class="kanban-live" role="status">
      <span class={`live-dot${task.state === 'running' ? ' beating' : ''}`} aria-hidden="true" />
      <span class="kanban-live-text">{line}</span>
    </div>
  )
}

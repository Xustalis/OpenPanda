// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'preact/hooks'
import { api, type Task } from '../api/client'
import { useAsync, useChangeSignal } from '../hooks'
import { t } from '../i18n'
import { Markdown } from '../md/render'
import { navigate } from '../nav'
import { layerAboveOwnsEscape } from './card-dismiss'
import { confirmDialog } from './confirm'
import { Icon } from './icons'
import { InfoSection, SpecCard, TimelineSection, parseResult, text } from './task-sections'
import { StateBadge } from './state-badge'
import { TaskTrace } from './task-trace'
import { bindStripDrag } from './strip-drag'
import { fmtElapsed, isActiveState } from './task-activity'
import { toast, toastError } from './toast'

/** The tasks this conversation started — the card behind the header's task
 *  button.
 *
 *  Same shell as the node card beside it: ghost icon button, card hanging off
 *  the bar, the button toggles it and Escape closes it, and the pane reserves a
 *  lane on the right while it is up. The two answer the same kind of question
 *  about a turn — the node card says *where* it runs, this one says *what it
 *  became* — and both open in place so a running turn can be watched without
 *  leaving the transcript. Neither closes on an outside click: reading the
 *  thread under the card is the point, not a dismissal.
 *
 *  Three strips: work that is moving (进行中), work parked on the human
 *  (待审核), and the archive (已完成 — done, failed and cancelled all landed
 *  somewhere, and none of them wants a decision). The card is tall enough for
 *  all three to be on screen at once, so the state of the thread is one glance
 *  rather than one scroll. A row's own actions (取消 / 执行) sit on the task,
 *  not behind a detail trip.
 *
 *  The card is a task list first. It used to carry a DecisionOrbit rail above
 *  the strips, always pointed at the newest task whether or not that was the
 *  task you were reading; the rail said "how is this turn going" before you had
 *  asked. It is gone from there. Picking a task now opens the trace in a card
 *  of its own, to the *left* of this one: the list says what the thread
 *  produced, the card beside it says how that one task was classified, routed
 *  and run. Two cards, one selection — the trace never describes a task the
 *  list is not pointing at. A message's 查看任务 link arrives the same way a
 *  chip does: it opens this card with that task already picked, because the
 *  answer is two cards away and a page jump used to cost the reader the
 *  transcript's scroll, its draft and its place in the conversation.
 *
 *  The trace is a sibling rather than a section inside this card because it is
 *  read in a different direction: the list is scanned top-down (and a strip
 *  that outgrows its four tiles continues downwards), the trace is a
 *  three-step column. Stacked in one card, the
 *  steps either pushed the strips down or had to live in a scroller inside a
 *  scroller. Side by side each keeps its own height and its own scroll, and the
 *  8px gap is the whole relationship. Narrow panes wrap the trace back under
 *  the list — it never floats over the transcript on a phone.
 *
 *  The card reads top-down in the order the questions come: what has this
 *  thread produced (strips), what is in this one task (detail). Every section
 *  keeps its full height whether or not it has rows in it (see
 *  `.orch-sec-slot`), so the three strips read as one table and the card does
 *  not jump when a task arrives or leaves.
 *
 *  A task opens *under the strips*, in the same card, and the strips stay where
 *  they are: the detail is a reading pane, not a page. Trading the rows for a
 *  detail view meant a back arrow for every look, and the whole point of
 *  reading tasks from here is that nothing has to be left behind. Clicking the
 *  same task again (or the × on the detail) puts both panes away. */
export function OrchestrateCard(props: {
  /** The thread whose tasks are shown; null before a new chat is saved. */
  sessionId?: string | null
  /** The other scope this card can answer for: a work directory. 工作空间
   *  dispatches into a directory rather than into a thread — it never shows a
   *  transcript, so "what has this directory produced" is the only question
   *  its header can usefully answer. Ignored when `sessionId` is set, so the
   *  chat's card keeps its exact behaviour. */
  project?: string
  /** Controlled by the pane: this card and the node card hang off the same
   *  corner, so opening one has to close the other. */
  open: boolean
  onOpenChange(open: boolean): void
  /** Node-directory facts for the trace's Step 2: how many machines can take
   *  this work decides whether it lists candidates or offers the "add a second
   *  device" CTA. Optional so the card still renders before the fleet loads. */
  onlineNodeCount?: number
  selfNodeId?: string
  /** A task the transcript asked to see: the message's 查看任务 opens this
   *  card with that task picked instead of leaving for the task page, so
   *  reading a run never costs the conversation's scroll, draft or context.
   *  A fresh object per request — that is what lets the same link re-open the
   *  pane after it was closed. */
  focusTask?: { id: string } | null
}) {
  const [picked, setPicked] = useState<string | null>(null)
  // Local nudge after an action: the shared change feed usually arrives a beat
  // later, and the row the user just cancelled should not sit there looking
  // alive while they wait for it.
  const [bump, setBump] = useState(0)
  const [busy, setBusy] = useState('')
  const tick = useChangeSignal()
  const box = useRef<HTMLDivElement>(null)

  // Fetch only while the card is up: closed, there is nothing to paint, and
  // the header button carries no count that would need the data early.
  const { data, error } = useAsync(
    () => {
      if (!props.open) return Promise.resolve([] as Task[])
      if (props.sessionId) return api.tasks({ session: props.sessionId })
      // Directory scope: one read for every task filed under the project the
      // picked work dir resolves to, whatever state it is in.
      if (props.project) return api.tasks({ project: props.project })
      return Promise.resolve([] as Task[])
    },
    [props.open, props.sessionId, props.project],
    tick + bump,
  )
  const tasks = data ?? []
  const doing = tasks.filter((x) => isActiveState(x.state))
  const review = tasks.filter((x) => x.state === 'review')
  // Everything that is neither moving nor parked: done, failed, cancelled. One
  // strip, newest first — the archive is read top-down, not triaged.
  const finished = tasks
    .filter((x) => !isActiveState(x.state) && x.state !== 'review')
    .sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at))

  // The newest task the thread produced: what the trace shows before anything
  // is picked, so opening the card answers "how is this turn going" without a
  // click. One selection drives everything under the strips — the trace and
  // the detail pane always describe the same task, which is what keeps the two
  // from fighting over the phrase "the task on screen".
  const newest = tasks.reduce<Task | null>(
    (a, b) => (!a || Date.parse(b.updated_at) >= Date.parse(a.updated_at) ? b : a),
    null,
  )
  const focusedId = picked ?? newest?.id ?? null

  // The full row (events, result, disposition, traces) for the focused task.
  // The list row paints immediately; this fills in behind it.
  const detail = useAsync(
    () => (focusedId ? api.task(focusedId) : Promise.resolve(null)),
    [focusedId],
    tick + bump,
  )
  const focused = focusedId
    ? detail.data && detail.data.id === focusedId
      ? detail.data
      : tasks.find((x) => x.id === focusedId) ?? null
    : null

  // Reopening starts at the top of the card: nothing picked — not with
  // whatever was open last time.
  useEffect(() => {
    if (props.open) return
    setPicked(null)
  }, [props.open])

  // A link in the transcript asks for one task by name: pick it as the chip
  // would. The request object is new on every click, so a second click on the
  // same link still re-opens a pane the user closed.
  useEffect(() => {
    if (!props.focusTask) return
    setPicked(props.focusTask.id)
  }, [props.focusTask])

  // Escape puts the detail away first, so one key undoes one step. It is the
  // card's only way out besides the header trigger: an outside click used to
  // close it too, which meant every click on a message — to select a line,
  // copy a path, follow a link — folded the card mid-read. The card is a
  // reading companion, not a menu; it stays until the button is pressed again.
  useEffect(() => {
    if (!props.open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || layerAboveOwnsEscape()) return
      e.stopPropagation()
      if (picked) setPicked(null)
      else props.onOpenChange(false)
    }
    document.addEventListener('keydown', onKey, true)
    return () => {
      document.removeEventListener('keydown', onKey, true)
    }
  }, [props.open, picked])

  /** Cancel or approve one task from the row that shows it. Both are
   *  destructive-ish, so cancel asks first (the run is killed); approve does
   *  not (it is the action the review row exists for). */
  async function act(task: Task, kind: 'cancel' | 'run') {
    if (kind === 'cancel') {
      const ok = await confirmDialog({
        title: t('detail.cancelConfirmTitle'),
        message: t('detail.cancelConfirmMsg'),
        confirmLabel: t('detail.cancelTask'),
        danger: true,
      })
      if (!ok) return
    }
    setBusy(`${task.id}:${kind}`)
    try {
      if (kind === 'cancel') {
        await api.cancel(task.id)
        toast(t('detail.cancelledToast'), 'success')
      } else {
        await api.approve(task.id)
        toast(t('detail.approvedToast'), 'success')
      }
      setBump((v) => v + 1)
    } catch (e) {
      toastError(e)
    } finally {
      setBusy('')
    }
  }

  const chipProps = (task: Task) => ({
    task,
    busy: busy.startsWith(`${task.id}:`),
    picked: picked === task.id,
    // Toggling on the same chip closes the pane: with the strips still up, the
    // chip is both the way in and the way out.
    onOpen: () => {
      setPicked((cur) => (cur === task.id ? null : task.id))
    },
    onCancel: () => void act(task, 'cancel'),
    onRun: () => void act(task, 'run'),
  })

  return (
    <div class="orch-picker" ref={box}>
      <button
        type="button"
        class={`icon-btn tip-down orch-pick-btn${props.open ? ' open' : ''}`}
        /* The tip is the control's name; the card behind it is the answer. */
        data-tip={props.open ? undefined : t('sessions.orchestrate')}
        aria-label={t('sessions.orchestrate')}
        aria-haspopup="dialog"
        aria-expanded={props.open}
        onClick={() => props.onOpenChange(!props.open)}
      >
        <Icon name="route" size={19} />
      </button>
      {props.open && (
        /* One row of cards hanging off the bar: the picked task on the left,
           the list on the right. The row is the positioning context — each card
           keeps its own height and scroll, and the gap between them is the only
           thing tying the two together. */
        <div class="orch-flyout">
          <div
            class="orch-panel"
            role="dialog"
            /* The visible label is the card's name; "任务" alone would say less
               than the heading two pixels under it. */
            aria-label={props.sessionId ? t('orch.title') : t('orch.projectTitle')}
          >
            {/* The strips, and nothing else: this card is the list. The picked
                task — its brief, its result and how it was routed — is read in
                the card beside it, so nothing here grows when a chip is
                clicked. */}
            <div class="orch-body">
              <p class="orch-panel-label">
                {props.sessionId ? t('orch.title') : t('orch.projectTitle')}
              </p>
              {error ? (
                <p class="orch-sec-empty">{t('common.loadFail')}</p>
              ) : !props.sessionId && !props.project ? (
                <p class="orch-sec-empty">{t('orch.noSession')}</p>
              ) : (
                <>
                  <OrchRow
                    title={t('orch.doing')}
                    tasks={doing}
                    empty={t('orch.emptyDoing')}
                    chipProps={chipProps}
                  />
                  <OrchRow
                    title={t('orch.review')}
                    tasks={review}
                    empty={t('orch.emptyReview')}
                    chipProps={chipProps}
                  />
                  <OrchRow
                    title={t('orch.done')}
                    tasks={finished}
                    empty={t('orch.emptyDone')}
                    chipProps={chipProps}
                  />
                </>
              )}
            </div>
          </div>
          {/* Second in the DOM, first on screen: the row is `row-reverse`, so
              the picked task sits to the left of the list and a narrow pane
              wraps it *under* the list rather than off the left edge. */}
          {picked && focused && (
            <OrchTask
              /* Keyed by task: switching tasks remounts the card, so its scroll
                 starts at the top and the orbit starts expanded for this task
                 rather than inheriting the previous one's state. */
              key={`task-${focused.id}`}
              task={focused}
              busy={busy.startsWith(`${focused.id}:`)}
              onlineNodeCount={props.onlineNodeCount}
              selfNodeId={props.selfNodeId}
              onClose={() => setPicked(null)}
              onCancel={() => void act(focused, 'cancel')}
              onRun={() => void act(focused, 'run')}
            />
          )}
        </div>
      )}
    </div>
  )
}

/** One strip: a label with its count, then the tasks in a two-column grid —
 *  four tiles in view (two rows of two), the rest one wheel-notch below. An
 *  empty strip keeps a tile-sized slot rather than collapsing to a line of
 *  text: the three strips are read as one table, and a section that shrank to
 *  its placeholder made the card jump every time a task arrived or left.
 *
 *  The grid scrolls itself, not the card: a thread can hold twenty finished
 *  tasks and none of them should push 待审核 off the bottom of the popover. The
 *  tiles are tall enough for a three-line title, so the four on screen say what
 *  they are; the count on the label says how many are below the fold, and the
 *  wheel and a press-drag both move the grid (see `strip-drag.ts`). */
function OrchRow(props: {
  title: string
  tasks: Task[]
  empty: string
  chipProps(task: Task): {
    task: Task
    busy: boolean
    picked: boolean
    onOpen(): void
    onCancel(): void
    onRun(): void
  }
}) {
  // Bound to the element's own lifetime, not to the task count: the strip
  // only exists in the filled branch, and rebinding on every list change
  // would tear the listeners down mid-gesture — a task arriving over SSE
  // during a drag could then drop the press and let the release click
  // through to the chip.
  const [strip, setStrip] = useState<HTMLDivElement | null>(null)
  useEffect(() => {
    if (!strip) return
    // The grid runs downwards: two tiles wide, four in view.
    return bindStripDrag(strip, 'y')
  }, [strip])
  return (
    <section class="orch-sec">
      <p class="orch-sec-head">
        <span>{props.title}</span>
        <span class="orch-sec-count">{props.tasks.length}</span>
      </p>
      {props.tasks.length === 0 ? (
        <p class="orch-sec-slot">{props.empty}</p>
      ) : (
        <div class="orch-grid" ref={setStrip}>
          {props.tasks.map((task) => (
            <OrchChip key={task.id} {...props.chipProps(task)} />
          ))}
        </div>
      )}
    </section>
  )
}

/** A task as it appears in a row: title, state, age, and its one action.
 *  The open target is a real button (not the whole tile) so the action button
 *  beside it is not a control nested inside another control. */
function OrchChip(props: {
  task: Task
  busy: boolean
  picked: boolean
  onOpen(): void
  onCancel(): void
  onRun(): void
}) {
  const active = isActiveState(props.task.state)
  const blocked = props.task.approval_disposition === 'needs_changed_input'
  return (
    <div class="orch-chip" data-state={props.task.state} data-picked={props.picked || undefined}>
      <button
        type="button"
        class="orch-chip-open"
        aria-expanded={props.picked}
        aria-controls="orch-detail"
        onClick={props.onOpen}
      >
        <span class="orch-chip-title">{props.task.title || props.task.id}</span>
        <span class="orch-chip-meta">
          <StateBadge state={props.task.state} />
          <span class="orch-chip-age">{fmtElapsed(props.task.updated_at)}</span>
        </span>
      </button>
      <div class="orch-chip-acts">
        {active && (
          <button
            type="button"
            class="orch-act cancel"
            disabled={props.busy}
            onClick={props.onCancel}
            title={t('detail.cancelTask')}
          >
            {t('orch.cancel')}
          </button>
        )}
        {props.task.state === 'review' && (
          <button
            type="button"
            class="orch-act run"
            disabled={props.busy || blocked}
            onClick={props.onRun}
            title={blocked ? t('detail.needsChangedInputHint') : t('detail.approve')}
          >
            {t('orch.run')}
          </button>
        )}
      </div>
    </div>
  )
}

/** The picked task, in the card to the left of the list — the task detail page
 *  in 420px: how it was classified, routed and run (the same 执行链路 strip the
 *  page draws), what came back, what it was asked to do (任务简报), its ids and
 *  its log.
 *
 *  The card used to be a second, thinner view of the same task: its own orbit
 *  (numbered steps, a brand bar, an action rail) and a JSON blob where the
 *  brief should be. Reading a task from the chat therefore meant either
 *  settling for less than the page said, or leaving the conversation for the
 *  page. It now mounts the page's own sections — `TaskTrace`, `SpecCard`,
 *  `InfoSection`, `TimelineSection` — so the two surfaces cannot drift: the
 *  page lays them out at 980px, the card stacks them at 420px, and the only
 *  thing that changes between them is the width. The message box and the
 *  delegation tree stay on the page; a task that needs those is a task worth
 *  opening in full, and the footer still goes there.
 *
 *  The sections are buttons now, not a stack. Five sections, each with its own
 *  fold, made a card you had to scroll to reach the fourth one — and every one
 *  of them was folded by default anyway, so the card was mostly five titles
 *  and a scrollbar. One row of buttons under the identity line, one panel they
 *  all share: pressing a button opens that section in the panel, pressing it
 *  again closes it, pressing another swaps what the panel shows. The panel is
 *  the fold, so the sections are mounted unfolded (`bare`) and a section that
 *  has nothing to say gets no button at all. The card opens with its first
 *  section already in the panel — the trace, for any run that has one — so a
 *  click on a tile lands on the run's route instead of five shut lids. */
function OrchTask(props: {
  task: Task
  busy: boolean
  onlineNodeCount?: number
  selfNodeId?: string
  onClose(): void
  onCancel(): void
  onRun(): void
}) {
  const task = props.task
  const active = isActiveState(task.state)
  const review = task.state === 'review'
  const blocked = task.approval_disposition === 'needs_changed_input'
  // Which button's section the shared panel is showing. The card opens on the
  // first section this task can fill — 执行链路 whenever there is one — because
  // "what is this run doing" is the question that made the reader press the
  // tile; a task with no trace opens on whatever it does have, and the pill
  // above the panel still puts it away.
  const tabs = orchTabs(task)
  const [tab, setTab] = useState<OrchTabKey | null>(() => tabs[0]?.key ?? null)
  // A section can disappear between two paints — a result arrives, the log is
  // emptied by a revert — and a panel left open on a section that no longer
  // exists would show a blank card with a pressed button above it.
  const openTab = tabs.find((x) => x.key === tab) ?? null

  return (
    <aside class="orch-orch-card" id="orch-detail" role="region" aria-label={t('orch.detail')}>
      <div class="orch-detail-head">
        <p class="orch-detail-title">{task.title || task.id}</p>
        <button
          type="button"
          class="icon-btn orch-detail-close"
          aria-label={t('orch.closeDetail')}
          title={t('orch.closeDetail')}
          onClick={props.onClose}
        >
          <Icon name="x" size={14} />
        </button>
      </div>
      <div class="orch-detail-meta">
        <StateBadge state={task.state} />
        {task.project && <span class="orch-detail-tag">{task.project}</span>}
        {task.owner && <span class="orch-detail-tag">@{task.owner}</span>}
        <span class="orch-detail-age">{t('time.ago', { d: fmtElapsed(task.updated_at) })}</span>
      </div>
      {review && task.approval_disposition && (
        <p class={`orch-detail-note${blocked ? ' blocked' : ''}`}>
          {t(`detail.reviewKind.${task.approval_disposition}`)}
        </p>
      )}

      {/* The five sections as one control: what the run was routed through,
          what it produced, what it was asked to do, what it is, and how it got
          there. `aria-expanded` on each button, because each one both opens
          and closes the panel under it. */}
      <nav class="orch-tabs" aria-label={t('orch.detail')}>
        {tabs.map((x) => (
          <button
            key={x.key}
            type="button"
            class={'orch-tab' + (openTab?.key === x.key ? ' is-on' : '')}
            aria-expanded={openTab?.key === x.key}
            aria-controls="orch-tab-panel"
            onClick={() => setTab((cur) => (cur === x.key ? null : x.key))}
          >
            {x.label}
          </button>
        ))}
      </nav>

      {openTab && (
        <div
          class="orch-tab-panel"
          id="orch-tab-panel"
          role="region"
          aria-label={openTab.label}
        >
          {openTab.key === 'trace' && <TaskTrace task={task} bare />}
          {openTab.key === 'result' && <ResultBlock text={resultText(task.result ?? '')} />}
          {openTab.key === 'brief' && (
            <SpecCard spec={task.spec} fallback={taskBrief(task)} bare />
          )}
          {openTab.key === 'info' && <InfoSection task={task} bare />}
          {openTab.key === 'timeline' && (
            <TimelineSection
              events={task.events ?? []}
              suppressResult={Boolean(task.result)}
              bare
            />
          )}
        </div>
      )}

      {/* The card mirrors the page, but not all of it: the message box, the
          delegation tree and the sub-agent list are decisions to make about a
          run, and a 420px flyout is the wrong place to make them. The way out
          is therefore always here, not only for a task that still has actions
          — reading it in full should not require closing the card first. */}
      <div class="orch-detail-foot">
        {active && (
          <button class="btn danger small" disabled={props.busy} onClick={props.onCancel}>
            {t('orch.cancel')}
          </button>
        )}
        {review && (
          <button
            class="btn primary small"
            disabled={props.busy || blocked}
            title={blocked ? t('detail.needsChangedInputHint') : undefined}
            onClick={props.onRun}
          >
            {t('orch.run')}
          </button>
        )}
        <button
          type="button"
          class="orch-detail-open"
          onClick={() => navigate({ view: 'detail', id: task.id })}
        >
          {t('orbit.actions.openTask')} →
        </button>
      </div>
    </aside>
  )
}

type OrchTabKey = 'trace' | 'result' | 'brief' | 'info' | 'timeline'

/** The card's five sections, in the order they are read — and only the ones
 *  this task can actually fill. A 执行结果 button over a task that produced no
 *  output opens an empty panel, which reads as a broken card rather than as
 *  "this run said nothing"; 任务信息 is the one section every task has. The log
 *  is offered only when there are events, for the same reason. */
function orchTabs(task: Task): Array<{ key: OrchTabKey; label: string }> {
  const out: Array<{ key: OrchTabKey; label: string }> = []
  if (hasTrace(task)) out.push({ key: 'trace', label: t('detail.trace') })
  if (resultText(task.result ?? '').trim()) out.push({ key: 'result', label: t('orch.result') })
  if (hasBrief(task)) out.push({ key: 'brief', label: t('detail.spec.title') })
  out.push({ key: 'info', label: t('detail.info') })
  if ((task.events?.length ?? 0) > 0) out.push({ key: 'timeline', label: t('detail.timeline') })
  return out
}

/** Whether `SpecCard` has anything to draw — the same test it makes for itself
 *  before it renders, asked from outside so the card can decide whether the
 *  button exists at all. */
function hasBrief(task: Task): boolean {
  if (taskBrief(task).trim()) return true
  const s = task.spec ? parseResult(task.spec) : null
  if (!s) return false
  const constraints = Array.isArray(s.constraints) ? s.constraints : []
  const agents = Array.isArray(s.allow_agents) ? s.allow_agents : []
  return (
    constraints.length > 0 ||
    agents.length > 0 ||
    Boolean(text(s.scope)) ||
    Boolean(text(s.success_definition))
  )
}

/** The run's output, rendered the way the task page renders it — markdown, not
 *  a mono dump of the envelope. It used to clamp at 220px behind its own fold;
 *  the button above the panel is the fold now, and the card scrolls, so the
 *  output is shown whole. */
function ResultBlock({ text }: { text: string }) {
  return (
    <div class="orch-detail-result">
      <Markdown text={text} />
    </div>
  )
}

/** What came back, in words.
 *
 *  A result blob is `{"ok":true,"exit_code":0,"stdout":"…","files_changed":[…]}`,
 *  and printing it raw put a wall of escaped JSON where the answer belongs.
 *  The card shows what the agent actually wrote; the envelope's bookkeeping —
 *  exit code, changed files, tokens — lives on the task page, which has a
 *  result view built for it. */
function resultText(raw: string): string {
  try {
    const v: unknown = JSON.parse(raw)
    if (!v || typeof v !== 'object' || Array.isArray(v)) return raw
    const r = v as Record<string, unknown>
    const prose = [r.stdout, r.stderr].filter(
      (x): x is string => typeof x === 'string' && x.trim() !== '',
    )
    if (prose.length) return prose.join('\n\n')
    if (typeof r.failed === 'string' && r.failed.trim()) return r.failed
  } catch {
    // A plain-text result is already the answer.
  }
  return raw
}

/** Whether there is an orchestration story to tell for this task. A task that
 *  was dispatched straight to an agent — or is still being created — has no
 *  classification, no route decision and no execution trace, and the strip
 *  would draw a single "执行" step under a title that says nothing. */
function hasTrace(task: Task): boolean {
  return Boolean(task.plan_meta) || (task.traces?.length ?? 0) > 0 || Boolean(task.last_event)
}

/** What the task was asked to do, in words.
 *
 *  `spec` is the dispatcher's machine envelope — `allow_agents`,
 *  `constraints`, `success_definition`, `user_locale` — and printing it raw
 *  turned 任务说明 into a two-thousand-character JSON wall that filled the whole
 *  360px card. `intent` is the sentence the user actually typed, and when it
 *  exists it *is* the brief. A task without one falls back to the envelope's
 *  prose fields only. */
function taskBrief(task: Task): string {
  const intent = (task.intent ?? '').trim()
  if (intent) {
    // The first line is the title the card already shows above this section.
    const lines = intent.split('\n')
    const title = (task.title ?? '').trim()
    if (title && (lines[0] ?? '').trim() === title) lines.shift()
    const body = lines.join('\n').trim()
    return body || intent
  }
  const spec = (task.spec ?? '').trim()
  if (!spec) return ''
  try {
    const v = JSON.parse(spec) as Record<string, unknown>
    const prose = ['target', 'goal', 'description', 'prompt', 'success_definition']
      .map((k) => (typeof v[k] === 'string' ? String(v[k]).trim() : ''))
      .filter(Boolean)
    if (prose.length) return prose.join('\n\n')
  } catch {
    // A plain-text spec is already the brief.
  }
  return spec
}

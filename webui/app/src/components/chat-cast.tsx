// SPDX-License-Identifier: AGPL-3.0-or-later

/** The faces and the cast list a group-chat transcript needs.
 *
 *  Three pieces, all of them shared by the two places a transcript is drawn
 *  (the thread and the summary row):
 *
 *    - `ChatAvatar` — the face at the head of a row. The panda wears the pet
 *      sprite the composer uses, so the console's own mascot is the one voice
 *      that is always there; an employee wears its official mark. Your own row
 *      carries no face at all — it is the only speaker the layout already
 *      identifies on its own.
 *    - `TaskCast` / `PlanCast` — what the panda handed out: the employee and
 *      the stage for a single task, the stage list for a plan. This is the
 *      orchestration half of the panda's message, read from the task row
 *      itself (the trace is the only place that names the CLI that ran it).
 *    - `TaskProgress` — the employee's own row while the job is still running:
 *      "Claude Code · 执行中 · 最近在改 HelloWorld.java". Once the task
 *      settles this row goes away and the employee's report turn takes over,
 *      because the transcript already has their closing paragraph.
 */

import { useEffect, useState } from 'preact/hooks'
import { api, type PlanDetail, type Task, type TraceEvent } from '../api/client'
import { useAsync, useChangeSignal, useTaskEvents, useTraceForTask } from '../hooks'
import { formatDuration } from '../format/time'
import { isActiveState } from './task-activity'
import { clock, liveStepOf, mergeLive, type LiveStep } from './task-live'
import { AgentFace } from './team-picker'
import { castPending, dispatchAgent, prettyAgent, type Speaker } from './chat-speaker'
import { Icon } from './icons'
import { t } from '../i18n'

/** One shared empty list: `task.traces` is absent until the row has started,
 *  and a fresh `[]` on every render would re-run the merge effect each time. */
const EMPTY_TRACES: TraceEvent[] = []

/** The face at the head of a chat row. Your own row has none: the side of the
 *  column already says who is talking, and a face there was only ever a
 *  second, redundant "you". */
export function ChatAvatar({ speaker }: { speaker: Speaker }) {
  if (speaker.who === 'agent') {
    return (
      <span class="msg-ava is-agent">
        <AgentFace name={speaker.agent} label={prettyAgent(speaker.agent)} />
      </span>
    )
  }
  if (speaker.who === 'user') return null
  return (
    <span class="msg-ava is-panda" aria-hidden="true">
      {/* The same mascot the composer's pet is drawn from — cropped to the
          standing frame and keyed to transparency, so it sits on the page in
          either theme instead of on a white tile. The `?v=` is a cache key:
          the service worker serves static assets cache-first, so a replaced
          image needs a new URL. */}
      <img src="/panda-avatar.png?v=1" alt="" width={30} height={30} />
    </span>
  )
}

/** One line of the panda's orchestration: who got the job, which stage, and
 *  where it stands. Rendered inside the panda's own bubble, above the task
 *  card — the card is the paperwork, this is the sentence. */
export function TaskCast({ task }: { task: Task }) {
  // The employee is named by a trace, not by the row: `exec_agent_start` does
  // not move the task's state, so the change signal (which fingerprints
  // id:state:updated) never fires for it. Reading `task.traces` alone left a
  // row that had already started saying "picking an employee" until something
  // unrelated re-fetched the task — the trace stream is the one that lands.
  const traces = useTraceForTask(task.id, task.traces ?? EMPTY_TRACES)
  const agent = dispatchAgent({ traces, agent: task.agent, requires: task.requires })
  const pending = agent ? '' : castPending(task.state)
  const stage = task.plan_meta?.stage_id
  const count = task.plan_meta?.stage_count
  return (
    <div class="msg-cast">
      {agent ? (
        <span class="msg-cast-agent">
          <AgentFace name={agent} label={prettyAgent(agent)} mini />
          {prettyAgent(agent)}
        </span>
      ) : pending ? (
        // A placeholder, not a name: it wears the quiet weight so it cannot
        // read as one (see `castPending`).
        <span class="msg-cast-agent is-pending">
          <span class="msg-cast-bot" aria-hidden="true">
            <Icon name="bot" size={11} />
          </span>
          {pending === 'picking' ? t('sessions.castPicking') : t('sessions.castPending')}
        </span>
      ) : null}
      {stage && (
        <span class="msg-cast-meta mono">
          {count && count > 1
            ? t('sessions.castStageOf', { stage, n: String(count) })
            : t('sessions.castStage', { stage })}
        </span>
      )}
      <StateChip state={task.state} />
    </div>
  )
}

/** A plan's stages, as the panda laid them out. */
export function PlanCast({ plan }: { plan: PlanDetail }) {
  if (plan.stages.length === 0) return null
  return (
    <ul class="msg-cast-list">
      {plan.stages.map((s) => (
        <li key={s.stage} class="msg-cast-row">
          <span class="msg-cast-stage mono">{s.stage}</span>
          <span class="msg-cast-title">{s.title || s.stage}</span>
          <StateChip state={s.state} />
        </li>
      ))}
    </ul>
  )
}

/** The state word with its own dot — the same colour language the report head
 *  uses, one size down. */
export function StateChip({ state }: { state: string }) {
  return (
    <span class={`msg-cast-state is-${state}`}>
      <span class="msg-report-dot" aria-hidden="true" />
      {t(`state.${state}`)}
    </span>
  )
}

/** The employee's row while the job is still running.
 *
 *  Before this, a dispatched task left the thread silent for as long as it
 *  took — the duration said 26 minutes and there was nothing in between. The
 *  row reads the task's newest event and says it in the employee's own voice,
 *  with their face, so the wait has a speaker. It disappears the moment the
 *  task settles: the report turn below is the same person, saying more.
 *
 *  The row is fed by the session's task list (one small row per task, polled
 *  on every change frame), so it must not depend on a trace timeline that
 *  list does not carry: the adapter the task is running under names the
 *  employee first, and traces — when the caller has them — refine it. Until
 *  either is known the row wears the panda's face: the panda is the one who
 *  handed the job out, and a row that appears only once the agent starts is a
 *  row the user stares at nothing for. */
export function TaskProgress({ task }: { task: Task }) {
  const change = useChangeSignal()
  const history = useTaskEvents(task.id, change, 40)
  const traces = useTraceForTask(task.id, task.traces ?? EMPTY_TRACES)
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const tick = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(tick)
  }, [])
  if (!isActiveState(task.state)) return null

  const agent = dispatchAgent({ traces, agent: task.agent, requires: task.requires })
  const steps: LiveStep[] = []
  for (const ev of mergeLive(history.events, traces)) {
    const step = liveStepOf(ev)
    if (!step) continue
    const prev = steps[steps.length - 1]
    if (prev && prev.label === step.label && prev.summary === step.summary) continue
    if (prev?.quiet && step.quiet) {
      // One silence line, refreshed with the newest duration — not one row
      // per heartbeat.
      steps[steps.length - 1] = step
      continue
    }
    steps.push(step)
  }
  const recent = steps.slice(-6)
  const started = Date.parse(task.created_at)
  const elapsed = Number.isFinite(started) ? Math.max(0, now - started) : 0
  const lastTs = steps.length ? steps[steps.length - 1]!.ts * 1000 : Date.parse(task.updated_at)
  const idle = Number.isFinite(lastTs) ? Math.max(0, now - lastTs) : 0
  const sup = task.supervision
  const speaker: Speaker = agent
    ? { who: 'agent', agent, role: 'report' }
    : { who: 'panda', agent: '', role: 'report' }

  return (
    <div class="msg agent is-progress task-live" data-task-id={task.id}>
      <ChatAvatar speaker={speaker} />
      <div class="msg-body">
        <div class="msg-head">
          <span class="msg-name">
            {agent ? prettyAgent(agent) : t('sessions.speakerPanda')}
          </span>
          <span class="msg-cast-state is-active">
            <span class="msg-report-dot" aria-hidden="true" />
            {t('sessions.castWorking')}
          </span>
          <span class="msg-dur">{t('sessions.liveElapsed', { d: formatDuration(elapsed) })}</span>
          {sup && sup.budget > 0 && (
            <span class="msg-cast-meta mono">
              {t('sessions.liveRound', { round: String(sup.round), budget: String(sup.budget) })}
            </span>
          )}
        </div>
        <div class="msg-bubble is-progress task-live-body">
          {recent.length > 0 ? (
            <ol class="task-live-timeline" aria-label={t('sessions.liveRecent')}>
              {recent.map((step, i) => (
                <li
                  key={step.key}
                  class={`task-live-step tone-${step.tone}${i === recent.length - 1 ? ' is-current' : ''}`}
                >
                  <span class="task-live-time">{clock(step.ts)}</span>
                  <span class="task-live-dot" aria-hidden="true" />
                  <span class="task-live-label">{step.label}</span>
                  {step.summary && (
                    <span class="task-live-summary" title={step.summary}>
                      {step.summary}
                    </span>
                  )}
                </li>
              ))}
            </ol>
          ) : (
            <p class="msg-progress-line">{t('sessions.liveWaiting')}</p>
          )}
          <div class="task-live-foot">
            <a class="task-live-open" href={`#/task/${encodeURIComponent(task.id)}`}>
              {t('sessions.liveOpen')} →
            </a>
            {idle > 30_000 && (
              <span class="task-live-idle">{t('sessions.liveIdle', { d: formatDuration(idle) })}</span>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

/** The detailed process folded under a finished task's report.
 *
 *  A running task narrates itself in `TaskProgress`; once it settles that row
 *  disappears and the report above says what happened. The process itself is
 *  still valuable — which commands ran, what failed, where the agent turned —
 *  but it must not push the conversation down. So it lives in a closed
 *  `<details>`: the summary is one line of facts, and opening it replays the
 *  same event stream the live row showed, with fuller text. */
export function TaskProcess({ taskId }: { taskId: string }) {
  const task = useTaskBrief(taskId)
  const [open, setOpen] = useState(false)
  if (!task) return null
  const files = resultFiles(task)
  const created = Date.parse(task.created_at)
  const updated = Date.parse(task.updated_at)
  const duration = Number.isFinite(created) && Number.isFinite(updated) ? Math.max(0, updated - created) : 0
  return (
    <details
      class="task-run"
      onToggle={(e) => setOpen((e.currentTarget as HTMLDetailsElement).open)}
    >
      <summary class="task-run-head">
        <StateChip state={task.state} />
        <span class="task-run-title">{t('sessions.processTitle')}</span>
        <span class="task-run-meta">
          {duration > 0 ? formatDuration(duration) : ''}
          {duration > 0 && files > 0 ? ' · ' : ''}
          {files > 0 ? t('sessions.processFiles', { n: String(files) }) : ''}
        </span>
        <span class="task-run-caret" aria-hidden="true">▾</span>
      </summary>
      {open && <TaskProcessBody taskId={taskId} task={task} files={files} />}
    </details>
  )
}

/** The process body is a component of its own so a closed fold costs one
 *  small task read, not a 200-event timeline per report in the thread. */
function TaskProcessBody({ taskId, task, files }: { taskId: string; task: Task; files: number }) {
  const change = useChangeSignal()
  const { events } = useTaskEvents(taskId, change, 200)
  const steps: LiveStep[] = []
  for (const ev of mergeLive(events, EMPTY_TRACES)) {
    const step = liveStepOf(ev, true)
    if (!step) continue
    const prev = steps[steps.length - 1]
    if (prev && prev.label === step.label && prev.summary === step.summary) continue
    steps.push(step)
  }
  const shown = steps.slice(-120)
  return (
    <div class="task-run-body">
      {(task.verification || files > 0) && (
        <div class="task-run-facts">
          {task.verification && (
            <span>{t('sessions.processVerification', { status: task.verification })}</span>
          )}
          {files > 0 && <span>{t('sessions.processFiles', { n: String(files) })}</span>}
        </div>
      )}
      {shown.length === 0 ? (
        <p class="task-run-empty">{t('sessions.processEmpty')}</p>
      ) : (
        <ol class="task-run-timeline">
          {shown.map((step) => (
            <li key={step.key} class={`task-run-step tone-${step.tone}`}>
              <span class="task-run-time">{clock(step.ts)}</span>
              <div class="task-run-main">
                <span class="task-run-label">{step.label}</span>
                {step.summary && <div class="task-run-text">{step.summary}</div>}
              </div>
            </li>
          ))}
        </ol>
      )}
      <a class="task-run-open" href={`#/task/${encodeURIComponent(taskId)}`}>
        {t('sessions.liveOpen')} →
      </a>
    </div>
  )
}

/** How many files the task's own result says it changed. A result without the
 *  field (a native command, a failed route) counts as zero, not as unknown. */
function resultFiles(task: Task): number {
  if (!task.result) return 0
  try {
    const parsed = JSON.parse(task.result) as { files_changed?: unknown }
    return Array.isArray(parsed.files_changed) ? parsed.files_changed.length : 0
  } catch {
    return 0
  }
}

/** Loads the task behind a dispatch turn. The transcript only stores the id,
 *  so the cast list and the bubble read the row itself — and they read it
 *  again on every change frame, which is what keeps the state current. It is
 *  the brief read: a bubble never renders the event timeline, and the full
 *  projection would drag that timeline down once per second per bubble. */
export function useTaskBrief(taskId?: string): Task | null {
  const change = useChangeSignal()
  const { data } = useAsync<Task | null>(
    () => (taskId ? api.taskBrief(taskId).catch(() => null) : Promise.resolve(null)),
    [taskId],
    change,
  )
  return data
}

/** The plan behind a plan turn, for the same reason. */
export function usePlanBrief(planId?: string): PlanDetail | null {
  const change = useChangeSignal()
  const { data } = useAsync<PlanDetail | null>(
    () => (planId ? api.plan(planId).catch(() => null) : Promise.resolve(null)),
    [planId],
    change,
  )
  return data
}

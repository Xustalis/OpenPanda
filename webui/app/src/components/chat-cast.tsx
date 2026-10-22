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

import { api, type PlanDetail, type Task, type TraceEvent } from '../api/client'
import { useAsync, useChangeSignal } from '../hooks'
import { isActiveState } from './task-activity'
import { formatTaskEvent } from './event-parser'
import { AgentFace } from './team-picker'
import { castAgent, prettyAgent, type Speaker } from './chat-speaker'
import { Icon } from './icons'
import { t } from '../i18n'

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
  const agent = castAgent(task.traces)
  const stage = task.plan_meta?.stage_id
  const count = task.plan_meta?.stage_count
  return (
    <div class="msg-cast">
      {agent ? (
        <span class="msg-cast-agent">
          <AgentFace name={agent} label={prettyAgent(agent)} mini />
          {prettyAgent(agent)}
        </span>
      ) : (
        <span class="msg-cast-agent">
          <span class="msg-cast-bot" aria-hidden="true">
            <Icon name="bot" size={11} />
          </span>
          {t('sessions.castPicking')}
        </span>
      )}
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
 *  task settles: the report turn below is the same person, saying more. */
export function TaskProgress({ task }: { task: Task }) {
  const agent = castAgent(task.traces)
  if (!agent || !isActiveState(task.state)) return null
  const line = activityLine(task.last_event)
  return (
    <div class="msg agent is-progress">
      <ChatAvatar speaker={{ who: 'agent', agent, role: 'report' }} />
      <div class="msg-body">
        <div class="msg-head">
          <span class="msg-name">{prettyAgent(agent)}</span>
          <span class="msg-cast-state is-active">
            <span class="msg-report-dot" aria-hidden="true" />
            {t('sessions.castWorking')}
          </span>
          <span class="msg-dur">
            <span class="spinner" aria-hidden="true" />
          </span>
        </div>
        <div class="msg-bubble is-progress">
          <p class="msg-progress-line">{line || t('sessions.castWorkingHint')}</p>
        </div>
      </div>
    </div>
  )
}

/** One trace row, said as a line of prose. `formatTaskEvent` already owns the
 *  vocabulary (it is what the timeline prints), so the progress row cannot
 *  drift away from the task page. */
function activityLine(ev?: TraceEvent): string {
  if (!ev) return ''
  let raw = ''
  try {
    raw = ev.data === undefined ? '' : JSON.stringify(ev.data)
  } catch {
    raw = ''
  }
  const { label, summary } = formatTaskEvent(String(ev.type), raw)
  if (!summary) return label
  const clipped = summary.length > 90 ? `${summary.slice(0, 90)}…` : summary
  return `${label} · ${clipped}`
}

/** Loads the task behind a dispatch turn. The transcript only stores the id,
 *  so the cast list and the live progress row read the row itself — and they
 *  read it again on every change frame, which is what keeps the progress line
 *  moving. */
export function useTaskBrief(taskId?: string): Task | null {
  const change = useChangeSignal()
  const { data } = useAsync<Task | null>(
    () => (taskId ? api.task(taskId).catch(() => null) : Promise.resolve(null)),
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

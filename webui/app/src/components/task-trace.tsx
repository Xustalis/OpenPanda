// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ComponentChildren } from 'preact'
import { useMemo, useState } from 'preact/hooks'
import type { ScoredCandidate, Task, TraceEvent } from '../api/client'
import { locale, t } from '../i18n'
import { orbitPhaseFromTraces, useTraceForTask } from '../hooks'
import { AgentTag } from './agent-tag'
import { Icon } from './icons'
import { latestActivity } from './task-activity'

// —---------------------------------------------------------------------------
// TaskTrace — the task detail page's execution strip.
//
// The detail page used to carry the full 大总管 card: three stacked steps, each
// with its own grey plate, a full-width progress bar and an action rail. On a
// one-shot task that is ~300px of chrome for two facts ("classified as task",
// "routed to this machine") that were already implied by the task existing.
//
// This is the same trace read as one line: [step] ─ [step] ─ [step], with the
// facts that actually vary (node, agent, rounds) attached to their step, and
// everything else — candidates, plan goal, supervisor note — one click down.
// Steps that never happened are not drawn at all: a direct dispatch has no
// classification and no routing decision, so it shows "dispatch → execute"
// instead of two rows that say "skipped".
// ---------------------------------------------------------------------------

type StepState = 'done' | 'active' | 'failed' | 'idle'

interface Step {
  key: string
  label: string
  fact?: string
  /** Set when the fact is an agent's name — it is drawn as the agent chip
   *  rather than as grey text, so the one identity on the strip reads first. */
  agent?: string
  state: StepState
  /** Tooltip when the fact is clipped. */
  hint?: string
}

/** Everything the strip and its detail panel read off one task. Split out of
 *  the component because the detail page now draws the two halves in different
 *  places — the strip as a card's always-visible lead, the cells inside that
 *  card's fold — and both must read the same derivation. */
export interface TraceModel {
  steps: Step[]
  round: string
  progress: number
  live: string
  running: boolean
  hasDetail: boolean
  classify: ClassifyData | null
  route: RouteData | null
  exec: ExecData | null
  supervision: SupervisionData | null
  classifyEv: TraceEvent | null
  routeEv: TraceEvent | null
  execEv: TraceEvent | null
  execLabel: string
}

export function useTaskTrace(task: Task): TraceModel {
  // Same seeding as the orbit: the task's newest event may be the only row
  // that says what a mid-flight run is doing on first paint.
  const seed = useMemo(() => {
    const arr = [...(task.traces ?? [])]
    if (task.last_event && !arr.some((e) => e.id === task.last_event!.id)) arr.push(task.last_event)
    return arr
  }, [task.traces, task.last_event])
  const traces = useTraceForTask(task.id, seed)

  const classifyEv = useMemo(() => firstEvent(traces, 'classify_result'), [traces])
  const routeEv = useMemo(() => firstEvent(traces, 'route_decision'), [traces])
  const execEv = useMemo(() => firstEvent(traces, 'exec_agent_start'), [traces])
  const classify = (classifyEv?.data ?? null) as ClassifyData | null
  const route = (routeEv?.data ?? null) as RouteData | null
  const exec = (execEv?.data ?? null) as ExecData | null
  const supervision = useMemo(
    () => lastData<SupervisionData>(traces, 'supervision_round'),
    [traces],
  )

  const state = task.state
  const running = state === 'running' || state === 'dispatched' || state === 'waiting_context'
  const queued = state === 'queued' || state === 'submitted'
  const failed = state === 'failed' || state === 'cancelled' || state === 'expired'
  const settled = state === 'done' || state === 'review'

  const live = useMemo(() => (running ? latestActivity(traces, task) : ''), [running, traces, task])

  // The step name follows the run: "执行中" over a finished task was the same
  // lie the orbit used to tell.
  const execLabel = settled || failed ? t('orbit.step.execEnded') : t('orbit.step.exec')

  // "任务" as the classification fact said nothing: every row on this page is
  // already a task, so the strip printed the word twice — once in the page and
  // once as the classifier's verdict. Other kinds (answer, tool, plan) still
  // carry their fact because they actually distinguish the run.
  const classifyText = useMemo(() => (classify ? classifyFact(classify) : undefined), [classify])

  const steps = useMemo<Step[]>(() => {
    const out: Step[] = []
    if (classify) {
      out.push({
        key: 'classify',
        label: t('orbit.step.classify'),
        fact: classifyText,
        state: 'done',
        hint: classify?.plan_goal ?? classify?.note ?? classify?.label,
      })
    }
    if (route) {
      out.push({
        key: 'route',
        label: t('orbit.step.route'),
        fact: routedName(route),
        state: 'done',
      })
    }
    if (!classify && !route) {
      // Direct dispatch: no classifier ran and no candidate was scored. Say
      // where it went rather than drawing two "skipped" rows.
      out.push({
        key: 'dispatch',
        label: t('detail.trace.dispatch'),
        fact: exec?.node_name || t('orbit.route.localAttr'),
        state: 'done',
      })
    }
    out.push({
      key: 'exec',
      label: execLabel,
      fact: exec?.agent || (queued ? t('detail.trace.waiting') : undefined),
      agent: exec?.agent,
      state: failed ? 'failed' : settled ? 'done' : running ? 'active' : 'idle',
    })
    return out
  }, [classify, classifyText, route, exec, settled, failed, running, queued, execLabel])

  const round =
    supervision?.round !== undefined && supervision?.budget !== undefined
      ? t('orbit.exec.round', { round: String(supervision.round), budget: String(supervision.budget) })
      : ''

  const progress = useMemo(
    () => (settled ? 100 : running ? progressOf(traces, task.state, supervision) : 0),
    [settled, running, traces, task.state, supervision],
  )

  const hasDetail = Boolean(
    route?.candidates?.length ||
      classify?.plan_goal ||
      classify?.note ||
      supervision?.judge_summary ||
      supervision?.verdict_status,
  )

  return {
    steps,
    round,
    progress,
    live,
    running,
    hasDetail,
    classify,
    route,
    exec,
    supervision,
    classifyEv,
    routeEv,
    execEv,
    execLabel,
  }
}

/** The strip itself: [step] ─ [step] ─ [step], the facts that vary attached to
 *  their step, the live status on the right. This is the half that earns its
 *  place above a fold — the cells below it are reference material, which is
 *  why the detail page keeps the strip in a card's lead and the cells inside
 *  that card's fold. */
export function TraceStrip({
  model,
  label,
  tail,
}: {
  model: TraceModel
  /** Left-hand label. Omitted when the host panel already names the section. */
  label?: string
  /** Extra controls after the live status (the standalone strip's chevron). */
  tail?: ComponentChildren
}) {
  const { steps, live, round, running, progress } = model
  return (
    <>
      <div class="tt-row">
        {label && <span class="tt-label">{label}</span>}

        <ol class="tt-steps">
          {steps.map((s, i) => (
            <li key={s.key} class={`tt-step is-${s.state}`}>
              {i > 0 && <span class="tt-link" aria-hidden="true" />}
              <span class="tt-dot" aria-hidden="true">
                {s.state === 'done' ? (
                  <Icon name="check" size={8} />
                ) : s.state === 'failed' ? (
                  <Icon name="x" size={8} />
                ) : null}
              </span>
              <span class="tt-name">{s.label}</span>
              {s.agent ? (
                <AgentTag name={s.agent} />
              ) : (
                s.fact && (
                  <span class="tt-fact" title={s.hint ?? s.fact}>
                    {s.fact}
                  </span>
                )
              )}
            </li>
          ))}
        </ol>

        <span class="tt-tail">
          {live && (
            <span class="tt-live" role="status">
              <span class="live-dot beating" aria-hidden="true" />
              <span class="tt-live-text">{live}</span>
            </span>
          )}
          {round && <span class="tt-meta">{round}</span>}
          {running && <span class="tt-meta tt-pct">{progress}%</span>}
          {tail}
        </span>
      </div>

      {running && (
        <span
          class="tt-progress"
          role="progressbar"
          aria-valuenow={progress}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-label={t('orbit.summary.progress', { pct: progress })}
        >
          <span class="tt-progress-fill" style={`--prog:${progress}%`} />
        </span>
      )}
    </>
  )
}

/** The cells behind the strip — one per step, every fact the trace carries. */
export function TraceDetail({ model }: { model: TraceModel }) {
  const { classify, route, exec, supervision, classifyEv, routeEv, execEv, execLabel } = model
  const classifyText = classify ? classifyFact(classify) : undefined
  return (
    <div class="tt-detail">
      {/* One cell per step, and every fact the trace actually carries: the
          panel is where a reader comes to ask "which machine, which model,
          with what permission" — a cell that answered with one word left
          them opening the raw log instead. */}
      <TraceCell label={t('orbit.step.classify')} at={classifyEv?.ts}>
        {classify ? (
          <>
            {classifyText && <span class="tt-lead">{classifyText}</span>}
            <KV k={t('detail.trace.goal')} v={classify.plan_goal} />
            <KV k={t('detail.trace.note')} v={classify.note ?? classify.label} />
            <KV k={t('detail.trace.stages')} v={stagesOf(classify)} />
            {!classifyText &&
              !classify.plan_goal &&
              !classify.note &&
              !classify.label &&
              !stagesOf(classify) && <span class="dim">—</span>}
          </>
        ) : (
          <span class="dim">{t('detail.trace.dispatch')}</span>
        )}
      </TraceCell>

      {/* No route decision means no routing happened — the task went straight
          to an agent. The cell used to be drawn anyway, headed "route" over
          the word "dispatch", which read as a step that ran and decided
          nothing. */}
      {route && (
        <TraceCell label={t('orbit.step.route')} at={routeEv?.ts}>
          {route.candidates?.length ? (
            route.candidates.map((c) => {
              const chosen = routedId(route) === c.node_id
              const score = c.score_breakdown?.total ?? c.total_score
              return (
                <span key={c.node_id} class={chosen ? 'tt-cand is-chosen' : 'tt-cand'}>
                  <span>{c.node_name ?? c.node_id}</span>
                  {typeof score === 'number' && <span class="dim">{score.toFixed(2)}★</span>}
                </span>
              )
            })
          ) : (
            <span class="dim">{routedName(route) ?? t('orbit.route.declined')}</span>
          )}
          <KV k={t('detail.trace.decision')} v={route.action} />
        </TraceCell>
      )}

      <TraceCell label={execLabel} at={execEv?.ts}>
        {exec?.agent && <AgentTag name={exec.agent} />}
        <KV k={t('detail.model')} v={exec?.model} mono />
        <KV k={t('detail.adapter')} v={exec?.adapter} mono />
        <KV k={t('detail.node')} v={exec?.node_name ? `@${exec.node_name}` : ''} mono />
        <KV k={t('detail.trace.tier')} v={tierOf(exec)} />
        <KV k={t('detail.trace.access')} v={accessOf(exec)} />
        <KV k={t('detail.workdir')} v={exec?.workdir} mono />
        {/* The round is on the strip's own top line (`.tt-tail`), beside the
            live status — repeating it down here put the same "轮次 1/5" twice
            in one card, one under the other. */}
        {supervision?.verdict_status && (
          <span class="dim">
            {t('detail.result.verdict', { verdict: supervision.verdict_status })}
          </span>
        )}
        {supervision?.judge_summary && (
          <span class="dim tt-judge">{supervision.judge_summary}</span>
        )}
      </TraceCell>
    </div>
  )
}

/** The strip as one self-contained block, with its own fold — what the chat
 *  card mounts. `bare` is the card's variant: no label, no chevron, detail
 *  always open, because the card's own panel is already the fold. */
export function TaskTrace({
  task,
  defaultOpen,
  bare,
}: {
  task: Task
  defaultOpen?: boolean
  bare?: boolean
}) {
  const model = useTaskTrace(task)
  const [unfolded, setUnfolded] = useState(Boolean(defaultOpen))
  const open = bare || unfolded
  return (
    <section class="tt" aria-label={t('detail.trace')}>
      <TraceStrip
        model={model}
        label={bare ? undefined : t('detail.trace')}
        tail={
          model.hasDetail && !bare ? (
            <button
              type="button"
              class={'tt-toggle' + (open ? ' is-open' : '')}
              aria-expanded={open}
              aria-label={t('detail.trace.detail')}
              title={t('detail.trace.detail')}
              onClick={() => setUnfolded((v) => !v)}
            >
              <Icon name="chevron-down" size={14} />
            </button>
          ) : undefined
        }
      />
      {open && model.hasDetail && <TraceDetail model={model} />}
    </section>
  )
}

function TraceCell({
  label,
  at,
  children,
}: {
  label: string
  /** When this step happened, on the cell's label row. A chain of steps with
   *  no clock on it cannot answer "how long did routing take". */
  at?: number
  children: ComponentChildren
}) {
  return (
    <div class="tt-cell">
      <span class="tt-cell-label">
        {label}
        {at ? <span class="tt-cell-at">{clock(at)}</span> : null}
      </span>
      {children}
    </div>
  )
}

/** One labelled fact inside a cell: label in the muted tier, value beside it.
 *  Renders nothing when there is no value — a row of dashes is not a fact. */
function KV({ k, v, mono }: { k: string; v?: string; mono?: boolean }) {
  if (!v) return null
  return (
    <span class="tt-kv">
      <span class="tt-k">{k}</span>
      <span class={'tt-v' + (mono ? ' mono' : '')} title={v}>
        {v}
      </span>
    </span>
  )
}

/** Clock time of a trace stamp, in the reader's locale. The date is left out
 *  on purpose: every step of one task happens inside one sitting. */
function clock(ts: number): string {
  const d = new Date(ts * 1000)
  if (Number.isNaN(d.getTime())) return ''
  try {
    return d.toLocaleTimeString(locale(), {
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    })
  } catch {
    return ''
  }
}

/** "1 · agent" — the scheduler tier plus the plan kind it implies. Both come
 *  from the same decision and read as one fact. */
function tierOf(exec: ExecData | null): string {
  const parts: string[] = []
  if (typeof exec?.tier === 'number') parts.push(String(exec.tier))
  if (exec?.plan_kind) parts.push(exec.plan_kind)
  return parts.join(' · ')
}

/** Whether the run was allowed to act, and whether it was confined. Two flags
 *  that read as one line: "已授权 · 未受限" answers "could this have touched
 *  anything" at a glance. */
function accessOf(exec: ExecData | null): string {
  if (!exec) return ''
  const parts: string[] = []
  if (typeof exec.authorized === 'boolean') {
    parts.push(exec.authorized ? t('detail.trace.authorized') : t('detail.trace.unauthorized'))
  }
  if (typeof exec.restricted === 'boolean') {
    parts.push(exec.restricted ? t('detail.trace.restricted') : t('detail.trace.unrestricted'))
  }
  return parts.join(' · ')
}

/** Stage count, when the classifier reported one. */
function stagesOf(c: ClassifyData): string {
  const n = c.stages_count ?? c.stage_count
  return typeof n === 'number' && n > 0 ? String(n) : ''
}

// —---------------------------------------------------------------------------
// Data shapes — the trace payloads this strip reads. Kept local and narrow:
// the orbit has its own copies for the chat card.
// ---------------------------------------------------------------------------

interface ClassifyData {
  kind?: string
  class?: string
  note?: string
  label?: string
  plan_goal?: string
  stages_count?: number
  stage_count?: number
  tool_count?: number
  tools?: unknown[]
}

interface RouteData {
  action?: string
  /** The wire key the scheduler emits (submit.go); `target`/`chosen` stay as
   *  fallbacks for traces written before the name settled. */
  target_node?: string
  target?: string
  chosen?: string
  candidates?: ScoredCandidate[]
}

/** The exec_agent_start payload (submit.go / the adapter's own start frame).
 *  Everything here was already on the wire; the panel used to read only the
 *  first two fields of it. */
interface ExecData {
  agent?: string
  node_name?: string
  model?: string
  adapter?: string
  workdir?: string
  /** Scheduler tier: 1 = local-direct, 2 = routed, 3 = plan. */
  tier?: number
  /** "agent" | "plan" — what shape the run was given. */
  plan_kind?: string
  authorized?: boolean
  restricted?: boolean
}

interface SupervisionData {
  round?: number
  budget?: number
  verdict_status?: string
  judge_summary?: string
}

function classifyFact(c: ClassifyData): string | undefined {
  switch (c.kind ?? c.class) {
    case 'answer':
      return t('orbit.classify.answer')
    case 'tool':
    case 'tool_call': {
      const n = c.tool_count ?? (Array.isArray(c.tools) ? c.tools.length : 0)
      return n > 0 ? t('orbit.classify.toolN', { n }) : t('orbit.classify.tool')
    }
    case 'task':
      // Deliberately blank — see the classifyText note above.
      return undefined
    case 'plan':
      return t('orbit.classify.plan', { n: c.stages_count ?? c.stage_count ?? 1 })
    default:
      return undefined
  }
}

/** Coarse completion for the strip's hairline bar: the phase ramp the orbit
 *  uses, with the supervision round as partial credit inside execution. */
function progressOf(
  traces: TraceEvent[],
  state: string | undefined,
  sup: SupervisionData | null,
): number {
  const phase = orbitPhaseFromTraces(traces, state)
  if (phase === null) return 0
  if (phase >= 3) return 100
  let p = [10, 35, 60][phase] ?? 0
  if (phase === 2 && sup?.round !== undefined && sup?.budget !== undefined) {
    const budget = Math.max(1, sup.budget)
    p = Math.min(98, p + (sup.round / budget) * (100 - p) * 0.9)
  }
  return Math.max(0, Math.min(100, Math.round(p)))
}

/** The node id the route picked, or undefined when the decision was a
 *  decline or carried no target. */
function routedId(route: RouteData): string | undefined {
  return route.target_node ?? route.target ?? route.chosen
}

/** The picked node as a display name: `target_node` is an id, and the
 *  candidate list maps it to the name the operator chose. A decline resolves
 *  to undefined rather than to the top candidate — naming one anyway would
 *  claim a route that never happened. */
function routedName(route: RouteData): string | undefined {
  const id = routedId(route)
  if (id) return route.candidates?.find((c) => c.node_id === id)?.node_name ?? id
  if (route.action === 'decline') return undefined
  return route.candidates?.[0]?.node_name ?? route.candidates?.[0]?.node_id
}

/** The first frame of a kind — the whole row, not just its payload: the cell
 *  header stamps the step with `ts`, which the data alone does not carry. */
function firstEvent(traces: TraceEvent[], type: TraceEvent['type']): TraceEvent | null {
  for (const e of traces) if (e?.type === type) return e
  return null
}

function lastData<T>(traces: TraceEvent[], type: TraceEvent['type']): T | null {
  for (let i = traces.length - 1; i >= 0; i--) {
    const e = traces[i]
    if (e?.type === type) return (e.data ?? null) as T | null
  }
  return null
}

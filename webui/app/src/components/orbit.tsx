import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { t } from '../i18n'
import {
  PlanMeta,
  ScoredCandidate,
  Task,
  TraceEvent,
} from '../api/client'
import { orbitPhaseFromTraces, useTraceForTask } from '../hooks'

// —---------------------------------------------------------------------------
// DecisionOrbit (§4.1.1 参考基准强约束实现)
//
// 参考形态：
//   ┌─ 🛰️【大总管正在调度】点击展开 ▼ ───────────────────────────────┐
//   │ Step 1: 意图分类 → Plan(3段)                                │
//   │ Step 2: 路由匹配                                              │
//   │   · develop 阶段 → MacBook (本机, 有 codex) 得分 0.93★      │
//   │   · train 阶段 → GPU-Server (显存 24G ✓) 得分 0.88          │
//   │   · report 阶段 → OrangePi (空闲) 得分 0.81                 │
//   │ Step 3: 执行中… ████████░░ 82%  [dev✓ trn⚠ rpt·]            │
//   └───────────────────────────────────────────────────────────────┘
//
// 折叠态：
//   🛰️【大总管】已分类为 Plan(3段) · 已派 2 节点 · 进度 82%  ▾
//
// 每一项结构要素严格按设计文档 §4.1.1 的表格规格实现，不得简化或省略。
// ---------------------------------------------------------------------------
export interface DecisionOrbitProps {
  task?: Task
  onlineNodeCount?: number
  traces?: TraceEvent[]
  selfNodeId?: string
  onAddDevice?: () => void
  onOpenQueue?: () => void
  onOpenPlan?: (planId?: string) => void
  onOpenTrail?: (taskId?: string) => void
  defaultOpen?: boolean
}

export default function DecisionOrbit(props: DecisionOrbitProps) {
  const {
    task,
    onlineNodeCount,
    traces: tracesProp,
    selfNodeId,
    onAddDevice,
    onOpenQueue,
    onOpenPlan,
    onOpenTrail,
  } = props

  const liveTraces = useTraceForTask(task?.id, task?.traces ?? [])
  const traces = tracesProp ?? liveTraces

  const phase = orbitPhaseFromTraces(traces, task?.state)
  const [open, setOpen] = useState<boolean>(() =>
    props.defaultOpen ?? (traces?.length ?? 0) > 0,
  )

  const singleNode = typeof onlineNodeCount === 'number' && onlineNodeCount <= 1

  // --- Slot data ------------------------------------------------------------
  const classify = useMemo<ClassifySlot | null>(
    () => firstTypedData(traces, 'classify_result'),
    [traces],
  )
  const route = useMemo<RouteSlot | null>(
    () => firstTypedData(traces, 'route_decision'),
    [traces],
  )
  const execStart = useMemo<ExecSlot | null>(
    () => firstTypedData(traces, 'exec_agent_start'),
    [traces],
  )
  const supervision = useMemo<SupervisionSlot | null>(
    () => lastTypedData(traces, 'supervision_round'),
    [traces],
  )
  const planStage = useMemo<StageSlot | null>(
    () => lastTypedData(traces, 'plan_stage_changed'),
    [traces],
  )
  const tier2 = useMemo<Tier2Slot | null>(
    () => lastTypedData(traces, 'tier2_triggered'),
    [traces],
  )
  const planMeta = task?.plan_meta

  // --- Summary row text (collapsed bar) ------------------------------------
  // 分类摘要（§4.1.1 折叠态规格）
  const classifyLine = useMemo(() => {
    if (!classify) {
      // 任何 trace 都没有就不渲染摘要文案（纯品牌占位）
      return null
    }
    switch (classify.kind ?? classify.class) {
      case 'answer':
        return t('orbit.classify.answer')
      case 'tool_call':
      case 'tool': {
        const n = classify.tool_count ?? (Array.isArray(classify.tools) ? classify.tools.length : 0)
        const key = n > 0 ? 'orbit.classify.toolN' : 'orbit.classify.tool'
        return n > 0 ? t(key, { n }) : t('orbit.classify.tool')
      }
      case 'task':
        return t('orbit.classify.task')
      case 'plan': {
        const n = classify.stages_count ?? classify.stage_count ?? 1
        return t('orbit.classify.plan', { n })
      }
      default:
        return null
    }
  }, [classify])

  const routeSummary = useMemo(() => {
    const target = route?.target ?? route?.chosen
    if (!target && !route?.candidates?.length) return null
    const cands = route?.candidates?.length ?? 0
    // §4.1.1 折叠态：「已派 2 节点」
    if (cands === 0) return null
    if (singleNode) {
      return t('orbit.summary.singleRouted')
    }
    return t('orbit.summary.routedNodes', { n: cands })
  }, [route, singleNode])

  const progress = useOverallProgress({
    planMeta,
    planStage,
    supervision,
    phase,
    task,
  })
  const hasReview = tier2?.parked_in_review || task?.state === 'review'

  // --- Expanded Step data ---------------------------------------------------
  // Step-2 路由行：当这是一个 plan stage 任务，逐阶段组 rows；否则单任务一行。
  const routeRows = useMemo<RouteRow[]>(() => buildRouteRows({
    classify,
    route,
    planMeta,
    selfNodeId,
    singleNode,
  }), [classify, route, planMeta, selfNodeId, singleNode])

  // Step-3 执行徽章 [dev✓ trn⚠ rpt·]
  const stageBadges = useMemo<StageBadge[]>(() => buildStageBadges({
    classify,
    planMeta,
    planStage,
    task,
  }), [classify, planMeta, planStage, task])

  // --- Fade-in placeholders per-new-trace-type ------------------------------
  const seenRef = useRef<Set<string>>(new Set())
  const [flashTypes, setFlashTypes] = useState<Record<string, number>>({})
  useEffect(() => {
    const fresh: string[] = []
    for (const ev of traces ?? []) {
      if (!seenRef.current.has(ev.type)) {
        seenRef.current.add(ev.type)
        fresh.push(ev.type)
      }
    }
    if (fresh.length) {
      const next: Record<string, number> = {}
      for (const t of fresh) next[t] = Date.now()
      setFlashTypes((prev) => ({ ...prev, ...next }))
    }
  }, [traces])

  return (
    <div
      class="orbit"
      role="region"
      aria-label={t('orbit.brandScheduling')}
    >
      {/* — Collapsed summary row: 🛰️ 【大总管】 摘要1 · 摘要2 · 进度  ▾ — */}
      <div class="orbit-head">
        <button
          type="button"
          class="orbit-head-btn"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
        >
          <span class="orbit-brand" aria-hidden>🛰️</span>
          <span class="orbit-brand-title">
            {classify ? t('orbit.brandTitle') : t('orbit.brandShort')}
          </span>

          <span class="orbit-summaries">
            {classifyLine && <span class="orbit-sum-pill">{classifyLine}</span>}
            {routeSummary && <span class="orbit-sum-pill">{routeSummary}</span>}
            {progress > 0 && (
              <span class="orbit-sum-pill">
                {t('orbit.summary.progress', { pct: progress })}
              </span>
            )}
            {hasReview && (
              <span class="orbit-sum-pill pill-review" role="status">
                ⚠ {t('orbit.summary.pendingReview')}
              </span>
            )}
          </span>

          <span class="orbit-toggle-ico" aria-hidden>
            {open ? '▴' : '▾'}
          </span>
        </button>
      </div>

      {/* — Expanded Steps 1/2/3 + ActionRail — */}
      {open && (
        <div class="orbit-body">
          <OrbitStep
            no={1}
            title={t('orbit.step.classify')}
            flashTs={flashTypes.classify_result}
          >
            <ClassifyLine classify={classify} />
          </OrbitStep>

          <OrbitStep
            no={2}
            title={t('orbit.step.route')}
            flashTs={flashTypes.route_decision}
          >
            {singleNode ? (
              <div class="orbit-single">
                <div class="line1">{t('orbit.singleNode.line1')}</div>
                <div class="line2">{t('orbit.singleNode.line2')}</div>
                {onAddDevice && (
                  <button
                    type="button"
                    class="btn btn-link orbit-cta"
                    onClick={onAddDevice}
                  >
                    {t('orbit.singleNode.cta')}
                  </button>
                )}
              </div>
            ) : (
              <RouteBlock
                rows={routeRows}
                selfNodeId={selfNodeId}
                chosen={route?.target ?? route?.chosen}
              />
            )}
          </OrbitStep>

          <OrbitStep
            no={3}
            title={t('orbit.step.exec')}
            flashTs={flashTypes.exec_agent_start ?? flashTypes.plan_stage_changed}
          >
            <ExecLine
              progress={progress}
              badges={stageBadges}
              supervision={supervision}
              exec={execStart}
              phase={phase}
            />
          </OrbitStep>

          {/* ActionRail — only for task/plan messages */}
          {task?.id && (
            <ActionRail
              task={task}
              onOpenQueue={onOpenQueue}
              onOpenPlan={onOpenPlan}
              onOpenTrail={onOpenTrail}
            />
          )}
        </div>
      )}
    </div>
  )
}

// —---------------------------------------------------------------------------
// Sub-components
// —---------------------------------------------------------------------------

function OrbitStep(props: {
  no: 1 | 2 | 3
  title: string
  flashTs?: number
  children: preact.ComponentChildren
}) {
  const [loading, setLoading] = useState(Boolean(props.flashTs))
  useEffect(() => {
    if (!props.flashTs) return
    setLoading(true)
    const id = setTimeout(() => setLoading(false), 220)
    return () => clearTimeout(id)
  }, [props.flashTs])
  return (
    <div class={'orbit-step' + (loading ? ' flash' : '')} aria-busy={loading}>
      <div class="orbit-step-head">
        <span class="orbit-step-no">Step {props.no}</span>
        <span class="orbit-step-title">:</span>
        <span class="orbit-step-title">{props.title}</span>
        {loading && <span class="orbit-step-skel">▫</span>}
      </div>
      <div class="orbit-step-body">{props.children}</div>
    </div>
  )
}

function ClassifyLine({ classify }: { classify: ClassifySlot | null }) {
  if (!classify) {
    return <div class="muted">{t('orbit.declined.classify')}</div>
  }
  const kind = classify.kind ?? classify.class
  switch (kind) {
    case 'plan': {
      const n = classify.stages_count ?? classify.stage_count ?? 1
      const goal = classify.plan_goal ?? classify.note ?? classify.label ?? ''
      return (
        <div class="orbit-classify-row">
          <span class="orbit-classify-kind">→</span>
          <span class="pill-plan pill">
            {t('orbit.classify.plan', { n })}
          </span>
          {goal && <span class="muted goal">· {goal}</span>}
        </div>
      )
    }
    case 'task': {
      const label = classify.note ?? classify.label ?? t('orbit.classify.task')
      return (
        <div class="orbit-classify-row">
          <span class="orbit-classify-kind">→</span>
          <span class="pill-task pill">{t('orbit.classify.task')}</span>
          <span class="muted goal">· {label}</span>
        </div>
      )
    }
    case 'tool':
    case 'tool_call': {
      const n = classify.tool_count ?? (Array.isArray(classify.tools) ? classify.tools.length : 0)
      const label = n > 0
        ? t('orbit.classify.toolN', { n })
        : t('orbit.classify.tool')
      return (
        <div class="orbit-classify-row">
          <span class="orbit-classify-kind">→</span>
          <span class="pill-tool pill">{label}</span>
        </div>
      )
    }
    case 'answer':
      return (
        <div class="orbit-classify-row">
          <span class="orbit-classify-kind">→</span>
          <span class="pill-answer pill">{t('orbit.classify.answer')}</span>
        </div>
      )
    default:
      return <div class="muted">—</div>
  }
}

type RouteRow = {
  /** Human readable stage name; empty string for a non-plan task. */
  stageLabel: string
  /** Candidate(s) actually chosen for this stage. */
  chosen?: string
  /** §4.1.1 "（本机，有 codex）" / "（显存 24G ✓）" / "（空闲）". */
  note?: string
  /** Score star rendering. */
  score?: number
}

function RouteBlock(props: {
  rows: RouteRow[]
  selfNodeId?: string
  chosen?: string
}) {
  const { rows, selfNodeId, chosen } = props
  if (!rows.length) {
    return <div class="muted">{t('orbit.route.declined')}</div>
  }
  return (
    <ul class="orbit-route-list" role="list">
      {rows.map((r, i) => (
        <li
          key={i}
          class={
            'orbit-route-line' +
            ((r.chosen && (r.chosen === chosen || r.chosen === selfNodeId)) ? ' chosen' : '')
          }
        >
          <span class="bullet">·</span>
          {r.stageLabel && (
            <span class="stage-name">{r.stageLabel}{t('orbit.route.stageSep')}</span>
          )}
          <span class="node">
            {r.note
              ? t('orbit.route.nodeWithNote', { note: r.note })
              : t('orbit.route.nodePlain', { name: r.chosen ?? '—' })}
          </span>
          {typeof r.score === 'number' && (
            <span class="score">
              {t('orbit.route.scoreFmt', { score: r.score.toFixed(2) })}
            </span>
          )}
        </li>
      ))}
    </ul>
  )
}

type StageBadge = {
  label: string
  state: 'done' | 'active' | 'idle' | 'failed'
}

function ExecLine(props: {
  progress: number
  badges: StageBadge[]
  supervision: SupervisionSlot | null
  exec: ExecSlot | null
  phase: number | null
}) {
  const { progress, badges, supervision, exec } = props
  return (
    <div class="orbit-exec">
      <div class="orbit-progress" aria-label={t('orbit.summary.progress', { pct: progress })}>
        <div class="orbit-progress-fill" style={`--prog:${progress}%`} />
        <span class="orbit-progress-label">{progress}%</span>
      </div>
      {badges.length > 0 && (
        <div class="orbit-stage-badges" aria-label={t('orbit.exec.stages', { stages: badges.length })}>
          [
          {badges.map((b, i) => (
            <span
              key={i}
              class={
                'badge-stage' +
                (b.state === 'done' ? ' done' : '') +
                (b.state === 'active' ? ' active' : '') +
                (b.state === 'failed' ? ' failed' : '')
              }
            >
              <span class="label">{b.label}</span>
              <span class="sym" aria-hidden>
                {b.state === 'done' ? '✓' : b.state === 'active' ? '⚠' : b.state === 'failed' ? '✗' : '·'}
              </span>
            </span>
          ))}
          ]
        </div>
      )}
      <div class="orbit-exec-meta muted">
        {exec?.agent && (
          <span>{t('orbit.exec.agent', { name: exec.agent })}</span>
        )}
        {supervision?.round !== undefined && supervision?.budget !== undefined && (
          <span>{t('orbit.exec.round', { round: String(supervision.round), budget: String(supervision.budget) })}</span>
        )}
        {exec?.node_name && <span>@ {exec.node_name}</span>}
      </div>
    </div>
  )
}

function ActionRail(props: {
  task: Task
  onOpenQueue?: () => void
  onOpenPlan?: (planId?: string) => void
  onOpenTrail?: (taskId?: string) => void
}) {
  const { task, onOpenQueue, onOpenPlan, onOpenTrail } = props
  const isPlan = Boolean(task?.plan_meta?.plan_id)
  return (
    <div class="orbit-action-rail" role="toolbar" aria-label={t('orbit.actions')}>
      <button type="button" class="orbit-action-btn" onClick={() => onOpenQueue?.()}>
        {t('orbit.actions.openQueue')} →
      </button>
      {isPlan && (
        <button type="button" class="orbit-action-btn" onClick={() => onOpenPlan?.(task.plan_meta?.plan_id)}>
          {t('orbit.actions.openPlan')} →
        </button>
      )}
      <button type="button" class="orbit-action-btn" onClick={() => onOpenTrail?.(task.id)}>
        {t('orbit.actions.openTrail')} →
      </button>
    </div>
  )
}

// —---------------------------------------------------------------------------
// Helpers: shape the slot data.
// —---------------------------------------------------------------------------

interface ClassifySlot {
  kind?: 'answer' | 'tool_call' | 'tool' | 'task' | 'plan' | string
  /** Legacy/compat field before we aligned on `kind` (design doc §3.1.1). */
  class?: string
  note?: string
  label?: string
  plan_goal?: string
  stages_count?: number
  stage_count?: number
  tool_count?: number
  tools?: unknown[]
}
interface RouteSlot {
  action?: 'local' | 'forward' | 'decline' | string
  chosen?: string
  target?: string
  reason?: string
  candidates?: ScoredCandidate[]
}
interface ExecSlot {
  node_id?: string
  node_name?: string
  agent?: string
  adapter?: string
  stage_id?: string
  plan_id?: string
}
interface SupervisionSlot {
  round?: number
  budget?: number
  verdict_status?: string
  judge_summary?: string
  note?: string
}
interface StageSlot {
  plan_id?: string
  stage_id?: string
  stage_title?: string
  /** Legacy name kept for pre-alignment traces. */
  stage_label?: string
  stage_count?: number
  transition?: 'unlocked' | 'started' | 'completed' | string
  needs_satisfied?: string[]
  artifact_hash?: string
}
interface Tier2Slot {
  operations?: Array<{ op?: string; target?: string; risk?: 'high' | 'medium' | 'low' | string }>
  parked_in_review?: boolean
}

function buildRouteRows(x: {
  classify: ClassifySlot | null
  route: RouteSlot | null
  planMeta?: PlanMeta
  selfNodeId?: string
  singleNode: boolean
}): RouteRow[] {
  const cands = x.route?.candidates ?? []
  const labels = x.planMeta?.stage_labels ?? []
  const chosenTarget = x.route?.target ?? x.route?.chosen
  const isPlan = (x.classify?.kind ?? x.classify?.class) === 'plan'

  // Plan: one row per stage label. If a stage label matches the chosen
  // candidate's stage, attribute it here. Without a per-stage route event
  // (the single route_decision was emitted by the whole-plan submit entry),
  // we stage-label the chosen candidate's row and attribute the top other
  // scores to the remaining labels in order.
  if (isPlan && labels.length > 0) {
    const sorted = [...cands].sort((a, b) => (b.breakdown?.total ?? 0) - (a.breakdown?.total ?? 0))
    const rows: RouteRow[] = labels.map((label, i) => {
      const isActiveStage = x.planMeta?.stage_id === label
      const cand =
        (isActiveStage
          ? sorted.find((c) => c.node_id === chosenTarget)
          : undefined) ??
        sorted[i] ??
        sorted[0]
      if (!cand) {
        return { stageLabel: label, chosen: chosenTarget, note: '', score: undefined }
      }
      const note = cand.node_id === x.selfNodeId
        ? t('orbit.route.localAttr')
        : t('orbit.route.remoteAttr')
      return {
        stageLabel: label,
        chosen: cand.node_name ?? cand.node_id,
        note,
        score: cand.breakdown?.total,
      }
    })
    return rows
  }

  // Non-plan task: single row — the chosen candidate only.
  if (!cands.length) return []
  const sorted = [...cands].sort((a, b) => (b.breakdown?.total ?? 0) - (a.breakdown?.total ?? 0))
  const chosen = cands.find((c) => c.node_id === chosenTarget) ?? sorted[0]
  if (!chosen) return []
  const note = chosen.node_id === x.selfNodeId
    ? t('orbit.route.localAttr')
    : t('orbit.route.remoteAttr')
  return [{
    stageLabel: '',
    chosen: chosen.node_name ?? chosen.node_id,
    note,
    score: chosen.breakdown?.total,
  }]
}

function buildStageBadges(x: {
  classify: ClassifySlot | null
  planMeta?: PlanMeta
  planStage: StageSlot | null
  task?: Task
}): StageBadge[] {
  const labels = x.planMeta?.stage_labels ?? []
  const isPlan = (x.classify?.kind ?? x.classify?.class) === 'plan'

  if (isPlan && labels.length > 0) {
    // Decide each label's state: done if any plan_stage_changed transition=completed
    // matches it; active = current plan_meta.stage_id OR latest transition=unlocked/started match.
    const completedLabel = x.planStage?.transition === 'completed'
      ? x.planStage?.stage_id ?? x.planStage?.stage_title
      : null
    const activeLabel = x.planMeta?.stage_id
      ?? (x.planStage?.transition === 'unlocked' || x.planStage?.transition === 'started'
        ? x.planStage.stage_id
        : undefined)
    const terminated = x.task?.state === 'done' || x.task?.state === 'review'
    return labels.map((lab, idx) => {
      let state: StageBadge['state'] = 'idle'
      if (lab === completedLabel) state = 'done'
      else if (lab === activeLabel) state = 'active'
      // Ordering fallback (plan stages advance in the labels order):
      if (state === 'idle') {
        const activeIdx = labels.indexOf(String(activeLabel ?? ''))
        const doneIdx = labels.indexOf(String(completedLabel ?? ''))
        const frontier = Math.max(doneIdx, activeIdx === -1 ? -1 : activeIdx - 1)
        if (idx <= frontier && labels[idx] !== activeLabel) state = 'done'
        if (labels[idx] === activeLabel) state = 'active'
        if (terminated && state === 'idle' && activeIdx === -1) {
          state = x.task?.state === 'done' ? 'done' : 'idle'
        }
      }
      return { label: shortStageLabel(lab, 3), state }
    })
  }

  // Non-plan: single badge representing the task's execution state.
  const taskState = x.task?.state ?? ''
  let s: StageBadge['state'] = 'idle'
  switch (taskState) {
    case 'queued':
      s = 'idle'
      break
    case 'doing':
      s = 'active'
      break
    case 'review':
    case 'done':
      s = 'done'
      break
    case 'failed':
    case 'cancelled':
      s = 'failed'
      break
  }
  return [{ label: t('orbit.exec.oneShot'), state: s }]
}

function shortStageLabel(label: string, maxLen: number): string {
  if (!label) return '—'
  if (label.length <= maxLen) return label
  // English first 3 consonants, Chinese full string (3 chars is 1 ideograph — keep it).
  // Cheap heuristic: keep first maxLen letters if ASCII, else first ideographs.
  if (/^[\x00-\x7F]+$/.test(label)) {
    return label.slice(0, maxLen)
  }
  return label.slice(0, Math.ceil(maxLen / 2))
}

function useOverallProgress(x: {
  planMeta?: PlanMeta
  planStage: StageSlot | null
  supervision: SupervisionSlot | null
  phase: number | null
  task?: Task
}): number {
  const n = x.planMeta?.stage_count ?? x.planStage?.stage_count ?? 0
  if (n > 0) {
    const labels = x.planMeta?.stage_labels ?? []
    const active = labels.indexOf(String(x.planMeta?.stage_id ?? x.planStage?.stage_id ?? ''))
    const completedTransition = x.planStage?.transition === 'completed'
      ? labels.indexOf(String(x.planStage.stage_id ?? x.planStage.stage_label ?? ''))
      : -1
    const frontier = Math.max(active, completedTransition)
    if (frontier < 0) return 0
    // Stages strictly before the frontier are finished; the frontier stage
    // itself earns full credit when it just transitioned to completed, else
    // partial credit from the exec phase / supervision round.
    const finishedBefore = frontier
    const frontierCredit = completedTransition === frontier
      ? 1
      : x.task?.state === 'done'
        ? 1
        : partialFromPhaseAndRound(x.phase, x.supervision)
    return clampPct((finishedBefore + frontierCredit) / n)
  }
  return clampPct(partialFromPhaseAndRound(x.phase, x.supervision))
}

function partialFromPhaseAndRound(
  phase: number | null,
  sup: SupervisionSlot | null,
): number {
  // phase: 0=classify, 1=route, 2=exec, 3=done. Map to a 0..1 curve.
  if (phase === null || phase < 0) return 0
  if (phase >= 3) return 1
  const base = [0.1, 0.35, 0.6][phase] ?? 0
  if (phase === 2 && sup?.round !== undefined && sup.budget !== undefined) {
    // Within exec, add round progress capped — budget/rounds saturate toward 1.
    const r = Math.max(1, sup.budget)
    return Math.min(0.98, base + (sup.round / r) * (1 - base) * 0.9)
  }
  return base
}

function clampPct(n: number): number {
  const v = Math.round(n * 100)
  if (Number.isNaN(v)) return 0
  return Math.max(0, Math.min(100, v))
}

function firstTypedData<T>(traces: TraceEvent[] | undefined, type: TraceEvent['type']): T | null {
  if (!traces) return null
  for (const e of traces) {
    if (e?.type === type) return (e.data ?? null) as T | null
  }
  return null
}

function lastTypedData<T>(traces: TraceEvent[] | undefined, type: TraceEvent['type']): T | null {
  if (!traces) return null
  for (let i = traces.length - 1; i >= 0; i--) {
    const e = traces[i]
    if (e?.type === type) return (e.data ?? null) as T | null
  }
  return null
}

export { DecisionOrbit as DecisionOrbit_ }

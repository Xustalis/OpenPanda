import { useState } from 'preact/hooks'
import { api, type PlanStage, type PlanSummary } from '../api/client'
import { useAsync, useChangeSignal, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { navigate } from '../nav'

/** The plan board — web parity with `/plans`. A plan is the set of tasks
 *  sharing a plan_id; the board summarizes each pipeline (stages, progress,
 *  failures) and drills into the stage list with artifacts and owners. */
export function PlansView() {
  useLocaleRerender()
  const change = useChangeSignal()
  const { data: plans, error } = useAsync(() => api.plans(), [], change)
  const [openId, setOpenId] = useState<string | null>(null)

  return (
    <section>
      <h1 class="page-title">{t('plans.title')}</h1>
      <p class="page-sub">{t('plans.subtitle')}</p>

      {error && <p class="gate-error">{error}</p>}
      {!error && !plans && <p class="dim">{t('common.loading')}</p>}
      {plans && plans.length === 0 && <p class="dim empty-hint">{t('plans.empty')}</p>}

      <div class="plan-list">
        {plans?.map((p) => (
          <PlanCard
            key={p.plan_id}
            plan={p}
            open={openId === p.plan_id}
            onToggle={() => setOpenId(openId === p.plan_id ? null : p.plan_id)}
          />
        ))}
      </div>
    </section>
  )
}

function PlanCard(props: { plan: PlanSummary; open: boolean; onToggle: () => void }) {
  const { plan: p } = props
  const pct = p.stages > 0 ? Math.round((p.done / p.stages) * 100) : 0
  return (
    <div class={`card plan-card${props.open ? ' open' : ''}`}>
      <button class="plan-card-head" onClick={props.onToggle} aria-expanded={props.open}>
        <div class="plan-card-main">
          <div class="plan-goal">{p.goal || p.plan_id}</div>
          <div class="plan-meta dim">
            <span class="mono">{p.plan_id.slice(0, 12)}</span>
            {' · '}
            {t('plans.stageCount', { n: p.stages })}
            {' · '}
            {fmtAgo(p.updated_at)}
          </div>
        </div>
        <div class="plan-stats">
          {p.running > 0 && <span class="badge blue">{p.running} {t('plans.running')}</span>}
          {p.review > 0 && <span class="badge yellow">{p.review} {t('plans.review')}</span>}
          {p.failed > 0 && <span class="badge red">{p.failed} {t('plans.failed')}</span>}
          <span class="badge green">{p.done}/{p.stages}</span>
        </div>
        <div class="plan-progress" aria-hidden="true">
          <div class="plan-progress-fill" style={{ width: `${pct}%` }} />
        </div>
      </button>
      {props.open && <PlanStages id={p.plan_id} />}
    </div>
  )
}

function PlanStages(props: { id: string }) {
  const { data: detail, error } = useAsync(() => api.plan(props.id), [props.id])
  if (error) return <p class="gate-error plan-stage-err">{error}</p>
  if (!detail) return <p class="dim plan-stage-err">{t('common.loading')}</p>
  return (
    <ol class="plan-stages">
      {detail.stages.map((s) => (
        <StageRow key={s.stage} stage={s} />
      ))}
    </ol>
  )
}

function StageRow(props: { stage: PlanStage }) {
  const s = props.stage
  return (
    <li class={`plan-stage state-${s.state}`}>
      <div class="plan-stage-head">
        <span class={`badge state-${s.state}`}>{t(`state.${s.state}`, s.state)}</span>
        <span class="stage-name">{s.title || s.stage}</span>
        {s.owner && <span class="dim mono stage-owner">{s.owner}</span>}
        <button
          class="btn small ghost"
          onClick={() => navigate({ view: 'detail', id: s.task_id })}
        >
          {t('plans.openTask')}
        </button>
      </div>
      {(s.needs?.length || s.output_artifact || s.inputs?.length) && (
        <div class="plan-stage-detail dim">
          {s.needs && s.needs.length > 0 && (
            <span>
              {t('plans.needs')}: <span class="mono">{s.needs.join(', ')}</span>
            </span>
          )}
          {s.inputs && s.inputs.length > 0 && (
            <span>
              {t('plans.inputs')}:{' '}
              <span class="mono">
                {s.inputs.map((i) => `${i.stage ?? '?'}:${i.hash.slice(0, 8)}`).join(', ')}
              </span>
            </span>
          )}
          {s.output_artifact && (
            <span>
              {t('plans.output')}: <span class="mono">{s.output_artifact.slice(0, 12)}</span>
            </span>
          )}
        </div>
      )}
    </li>
  )
}

function fmtAgo(ts: number): string {
  if (!ts) return ''
  const s = Math.max(0, Math.floor(Date.now() / 1000 - ts))
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m`
  if (s < 86400) return `${Math.floor(s / 3600)}h`
  return `${Math.floor(s / 86400)}d`
}

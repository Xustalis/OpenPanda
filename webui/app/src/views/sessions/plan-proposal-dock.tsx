// SPDX-License-Identifier: AGPL-3.0-or-later

import { Icon } from '../../components/icons'
import { t } from '../../i18n'
import { proposalSummary } from './plan-proposal'

/** The proposal's home is the side dock, but the one thing waiting for a
 *  decision has to be visible from the transcript — after the reply, before
 *  the box, the same shelf the clarify card uses.
 *
 *  Two doors, because "look at it" and "act on it" are two intentions:
 *  clicking the card reads the plan in the dock; the button opens the same
 *  pane with the note field focused, which is where both "change this" and
 *  "start" live. The old label ("查看并实施") collapsed both into one click and
 *  read as "you must implement to read". */
export function PlanProposalDock({ version, text, onOpen, onReview }: {
  version: number
  text: string
  onOpen(): void
  onReview(): void
}) {
  const summary = proposalSummary(text)
  return (
    <div class="plan-dock">
      <section class="plan-dock-card">
        <button type="button" class="plan-dock-open" title={t('sessions.plan.view')} onClick={onOpen}>
          <span class="plan-dock-icon" aria-hidden="true"><Icon name="lightbulb" size={17} /></span>
          <span class="plan-dock-copy">
            <strong class="plan-dock-title">{t('sessions.plan.proposal')} · {version}</strong>
            <span class="plan-dock-summary">{summary || t('sessions.plan.ready')}</span>
          </span>
        </button>
        <button type="button" class="btn primary plan-dock-review" onClick={onReview}>
          {t('sessions.plan.review')}
        </button>
      </section>
    </div>
  )
}

/** What a running implementation is doing, in the reader's language. The
 *  server sends a stable token; an unknown one (a phase added later) falls
 *  back to "the model is thinking" rather than printing the raw token. */
const PLAN_PHASES: Record<string, string> = {
  pending: 'sessions.plan.phase.pending',
  running: 'sessions.plan.phase.members',
  dispatch: 'sessions.plan.phase.dispatch',
  plan: 'sessions.plan.phase.plan',
  route: 'sessions.plan.phase.route',
  exec: 'sessions.plan.phase.exec',
  tool: 'sessions.plan.phase.tool',
  judge: 'sessions.plan.phase.judge',
  wait: 'sessions.plan.phase.wait',
  retry: 'sessions.plan.phase.retry',
  offline: 'sessions.plan.phase.offline',
  clarify: 'sessions.plan.phase.clarify',
  answer: 'sessions.plan.phase.answer',
  model: 'sessions.plan.phase.model',
}

export function PlanRunningDock({ phase, detail, onStop, onTask }: {
  phase: string
  detail?: string
  onTask?: () => void
  onStop?: () => void
}) {
  const key = PLAN_PHASES[phase] ?? 'sessions.plan.phase.model'
  return (
    <div class="plan-dock">
      <section class="plan-dock-card plan-dock-live" aria-live="polite">
        <span class="plan-dock-icon" aria-hidden="true"><span class="plan-dock-pulse" /></span>
        <span class="plan-dock-copy">
          <strong class="plan-dock-title">{t('sessions.plan.running')}</strong>
          <span class="plan-dock-summary">
            {t(key)}{detail ? <span class="plan-dock-detail mono"> · {detail}</span> : null}
          </span>
        </span>
        {onTask && <button type="button" class="btn ghost" onClick={onTask}>{t('sessions.taskLog')}</button>}
        {onStop && <button type="button" class="btn ghost plan-dock-review" onClick={onStop}>
          {t('sessions.stop')}
        </button>}
      </section>
    </div>
  )
}

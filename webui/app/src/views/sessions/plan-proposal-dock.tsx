// SPDX-License-Identifier: AGPL-3.0-or-later

import { Icon } from '../../components/icons'
import { t } from '../../i18n'
import { proposalSummary } from './plan-proposal'

/** The proposal's confirmation used to ride inside the composer's box, under
 *  the send row — a button in the input's frame reads as part of the input,
 *  and the one thing waiting for a decision sat where the next message is
 *  typed. It stands on the lane above the composer now, the same shelf the
 *  clarify card uses, so "something is waiting for you" always appears in the
 *  same place: after the reply, before the box. The dialog still owns the
 *  full text and the confirmation; this bar only opens it. */
export function PlanProposalDock({ version, text, onReview }: {
  version: number
  text: string
  onReview(): void
}) {
  const summary = proposalSummary(text)
  return (
    <div class="plan-dock">
      <section class="plan-dock-card" aria-label={t('sessions.plan.proposal')}>
        <span class="plan-dock-icon" aria-hidden="true"><Icon name="lightbulb" size={17} /></span>
        <div class="plan-dock-copy">
          <strong class="plan-dock-title">{t('sessions.plan.proposal')} · {version}</strong>
          <span class="plan-dock-summary">{summary || t('sessions.plan.ready')}</span>
        </div>
        <button type="button" class="btn primary plan-dock-review" onClick={onReview}>
          {t('sessions.plan.review')}
        </button>
      </section>
    </div>
  )
}

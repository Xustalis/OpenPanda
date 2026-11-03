// SPDX-License-Identifier: AGPL-3.0-or-later

import { useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { Icon } from '../components/icons'

/** 团队 — a destination that exists before its features do.
 *
 *  The rail needs the row: 队列·任务·项目 answers "what is this node doing",
 *  团队 answers "who is doing it with you", and those are two different doors
 *  even while the second one opens onto nothing. So the page paints the header
 *  (a reader who lands here by URL still learns where they are) and one line
 *  that says the page is empty — no board, no form, no disabled controls. A
 *  control that cannot do anything is worse than no control: it invites a click
 *  and then explains itself. */
export function TeamView() {
  useLocaleRerender()

  return (
    <section class="tm-page">
      <header class="tm-head">
        <h1 class="page-title">{t('nav.team')}</h1>
        <p class="page-sub">{t('team.subtitle')}</p>
      </header>

      <div class="tm-blank">
        <Icon name="users" size={18} />
        <span>{t('team.blank')}</span>
      </div>
    </section>
  )
}

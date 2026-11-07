// SPDX-License-Identifier: AGPL-3.0-or-later

import { t } from '../i18n'
import { Icon, type IconName } from '../components/icons'

/** The wizard's guide page: the one step that is not a question.
 *
 *  Consent is a signature; this is the tour that makes the signature mean
 *  something. It sits immediately after the terms step because that is the
 *  moment the console becomes the reader's — and it answers the three things
 *  a first-run install otherwise has to discover by clicking around: where
 *  the destinations are, what the panda in the corner does, and how a model
 *  gets wired up.
 *
 *  It is deliberately a *page*, not a spotlight tour over the live console.
 *  The wizard is modal and the rail behind it is dimmed, so punching holes in
 *  the scrim would either fight the wizard's own focus trap or leave the
 *  reader on a page they cannot click. A page also survives a narrow window
 *  and the mobile rail, where a spotlight would point at something off-screen.
 *
 *  Every label here reuses the real navigation strings (`nav.*`,
 *  `sessions.new`) so the guide cannot describe a rail that no longer exists —
 *  rename a destination and this page renames itself with it. */

const NAV: Array<{ icon: IconName; label: string; desc: string }> = [
  { icon: 'square-pen', label: 'sessions.new', desc: 'onboarding.guide.nav.newChat' },
  { icon: 'devices', label: 'nav.workspace', desc: 'onboarding.guide.nav.workspace' },
  { icon: 'kanban', label: 'nav.control', desc: 'onboarding.guide.nav.control' },
  { icon: 'users', label: 'nav.team', desc: 'onboarding.guide.nav.team' },
  { icon: 'orbit', label: 'nav.memory', desc: 'onboarding.guide.nav.memory' },
  { icon: 'gear', label: 'nav.settings', desc: 'onboarding.guide.nav.settings' },
]

/** Gestures the pet answers to. Kept to five rows: the press model has more
 *  corners than this (a third tap inside the rhythm, the 1.2s cool-down), and
 *  listing them all would read as a manual for a toy. */
const GESTURES: Array<{ key: string; desc: string }> = [
  { key: 'onboarding.guide.panda.click', desc: 'onboarding.guide.panda.clickDesc' },
  { key: 'onboarding.guide.panda.working', desc: 'onboarding.guide.panda.workingDesc' },
  { key: 'onboarding.guide.panda.double', desc: 'onboarding.guide.panda.doubleDesc' },
  { key: 'onboarding.guide.panda.burst', desc: 'onboarding.guide.panda.burstDesc' },
  { key: 'onboarding.guide.panda.night', desc: 'onboarding.guide.panda.nightDesc' },
]

const MODEL_STEPS: Array<{ title: string; desc: string }> = [
  { title: 'onboarding.guide.model.one', desc: 'onboarding.guide.model.oneDesc' },
  { title: 'onboarding.guide.model.two', desc: 'onboarding.guide.model.twoDesc' },
  { title: 'onboarding.guide.model.three', desc: 'onboarding.guide.model.threeDesc' },
]

export function GuideStep() {
  return (
    <div class="guide-body">
      <h2 class="modal-title">{t('onboarding.guide.title')}</h2>
      <p class="modal-msg">{t('onboarding.guide.sub')}</p>

      <div class="guide-grid">
        <section class="guide-card">
          <h3 class="guide-card-title">{t('onboarding.guide.nav')}</h3>
          <ul class="guide-nav">
            {NAV.map((row) => (
              <li key={row.label} class="guide-nav-row">
                <span class="guide-nav-icon" aria-hidden="true">
                  <Icon name={row.icon} />
                </span>
                <span class="guide-nav-text">
                  <span class="guide-nav-name">{t(row.label)}</span>
                  <span class="guide-nav-desc">{t(row.desc)}</span>
                </span>
              </li>
            ))}
          </ul>
        </section>

        <section class="guide-card">
          <h3 class="guide-card-title">{t('onboarding.guide.panda')}</h3>
          {/* The real sprite, not a redrawn one: the guide's job is to make
              the thing in the corner recognisable, and a stand-in would teach
              the wrong panda. The pose is the same one the composer shows. */}
          <div class="guide-panda">
            <span class="guide-panda-stage" aria-hidden="true">
              <span class="composer-panda-sprite" />
            </span>
            <p class="guide-panda-pose">{t('onboarding.guide.panda.pose')}</p>
          </div>
          <dl class="guide-gestures">
            {GESTURES.map((g) => (
              <div key={g.key} class="guide-gesture">
                <dt>{t(g.key)}</dt>
                <dd>{t(g.desc)}</dd>
              </div>
            ))}
          </dl>
        </section>

        <section class="guide-card guide-card-wide">
          <h3 class="guide-card-title">{t('onboarding.guide.model')}</h3>
          <ol class="guide-steps">
            {MODEL_STEPS.map((s, i) => (
              <li key={s.title} class="guide-step">
                <span class="guide-step-n" aria-hidden="true">
                  {i + 1}
                </span>
                <span class="guide-step-text">
                  <span class="guide-step-name">{t(s.title)}</span>
                  <span class="guide-step-desc">{t(s.desc)}</span>
                </span>
              </li>
            ))}
          </ol>
          <p class="guide-note">{t('onboarding.guide.modelNote')}</p>
        </section>
      </div>
    </div>
  )
}

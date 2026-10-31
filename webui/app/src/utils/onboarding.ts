// SPDX-License-Identifier: AGPL-3.0-or-later

import type { OnboardingState } from '../api/client'

export interface OnboardingGate {
  /** Consent is missing or stale — something must still be acknowledged. */
  needsWizard: boolean
  /** The user has already answered something, so this is not a fresh install. */
  recorded: boolean
  /** Fresh install: the wizard opens without a click. */
  firstRun: boolean
  /** A recorded user with stale consent: open only at the terms step. */
  termsOnly: boolean
  /** Which banner the page should show when the modal is closed. */
  banner: 'terms' | 'model' | 'none'
}

/** Decide how the first-run gate behaves for one onboarding payload.
 *
 *  Consent, not wizard completion, is the hard gate. But an explicit dismissal
 *  is itself an answer: re-opening the full wizard on every page load for
 *  someone who already clicked "not now" is nagging, not consent. A dismissed
 *  install therefore gets a banner whose CTA reopens the wizard — the terms
 *  step stays one click away without blocking the console. */
export function onboardingGate(state: OnboardingState): OnboardingGate {
  const current = state.terms_version_current ?? 0
  const termsCurrent = state.terms_accepted && (state.terms_version ?? 0) >= current
  const needsWizard = !termsCurrent
  const recorded = Boolean(
    state.onboarded || state.terms_accepted || state.model_configured || state.locale,
  )
  return {
    needsWizard,
    recorded,
    firstRun: needsWizard && !recorded,
    termsOnly: needsWizard && recorded,
    banner: needsWizard ? 'terms' : state.model_configured ? 'none' : 'model',
  }
}

// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it } from 'node:test'
import assert from 'node:assert/strict'

import type { OnboardingState } from '../api/client.ts'
import { onboardingGate } from './onboarding.ts'

const BASE: OnboardingState = {
  locale: '',
  terms_accepted: false,
  terms_version: 0,
  terms_version_current: 3,
  onboarded: false,
  approval_mode: 'on-request',
  model_configured: false,
}

function state(patch: Partial<OnboardingState>): OnboardingState {
  return { ...BASE, ...patch }
}

describe('onboardingGate', () => {
  it('opens the full wizard on a fresh install', () => {
    const gate = onboardingGate(state({}))
    assert.equal(gate.needsWizard, true)
    assert.equal(gate.recorded, false)
    assert.equal(gate.firstRun, true)
    assert.equal(gate.termsOnly, false)
    assert.equal(gate.banner, 'terms')
  })

  it('does not re-open the full wizard after the user dismissed it', () => {
    const gate = onboardingGate(state({ onboarded: true }))
    assert.equal(gate.needsWizard, true)
    assert.equal(gate.recorded, true)
    assert.equal(gate.firstRun, false)
    assert.equal(gate.termsOnly, true)
    assert.equal(gate.banner, 'terms')
  })

  it('re-prompts stale consent at the terms step only', () => {
    const gate = onboardingGate(
      state({
        locale: 'zh-CN',
        terms_accepted: true,
        terms_version: 2,
        onboarded: true,
      }),
    )
    assert.equal(gate.firstRun, false)
    assert.equal(gate.termsOnly, true)
    assert.equal(gate.banner, 'terms')
  })

  it('shows the model banner once consent is current', () => {
    const gate = onboardingGate(
      state({ terms_accepted: true, terms_version: 3, onboarded: true }),
    )
    assert.equal(gate.needsWizard, false)
    assert.equal(gate.termsOnly, false)
    assert.equal(gate.banner, 'model')
  })

  it('shows no banner when consent and model are both ready', () => {
    const gate = onboardingGate(
      state({
        terms_accepted: true,
        terms_version: 3,
        onboarded: true,
        model_configured: true,
      }),
    )
    assert.equal(gate.needsWizard, false)
    assert.equal(gate.banner, 'none')
  })
})

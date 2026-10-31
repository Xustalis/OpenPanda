// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ProviderInfo } from '../api/client'
import { ProviderLogo } from '../brand/providers'
import { t } from '../i18n'

/** One tile in the provider catalogue: the vendor's mark, its label, the
 *  default model the pick would land on, and the state badges that tell you
 *  whether this provider is already keyed or needs no key at all.
 *
 *  Shared by the two places that offer the same catalogue — the add-model
 *  wizard on the models page and the onboarding wizard's model step — so the
 *  tiles cannot drift apart. */
export function ProviderCard(props: {
  provider: ProviderInfo
  onPick: () => void
  type?: 'button' | 'submit'
}) {
  const p = props.provider
  return (
    <button type={props.type ?? 'button'} class="provider-card" onClick={props.onPick}>
      {/* The mark rides on the label's line rather than owning a column of its
          own: in a 190px tile a separate icon column pushed every label 42px
          in, leaving a gutter of white the eye read as a hole. */}
      <span class="provider-head">
        <span class="provider-logo">
          <ProviderLogo id={p.id} size={18} />
        </span>
        <span class="provider-name">{p.label}</span>
      </span>
      <span class="provider-body">
        <span class="provider-sub dim mono">{p.default_model || p.api_type}</span>
        <span class="provider-badges">
          {p.no_auth && <span class="badge">{t('models.noAuth')}</span>}
          {p.key_saved && <span class="badge green">{t('models.keySaved')}</span>}
        </span>
      </span>
    </button>
  )
}

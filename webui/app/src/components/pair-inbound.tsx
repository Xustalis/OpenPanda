// SPDX-License-Identifier: AGPL-3.0-or-later

import type { PairRequest } from '../api/client'
import { t } from '../i18n'
import { Icon } from './icons'

/** One inbound pairing request, as a decision.
 *
 *  It appears in two places — inline in 工作空间 → 连接设备, and as a floating
 *  card whenever one arrives while the operator is somewhere else
 *  (pair-request-host.tsx) — so it lives here rather than twice. The two
 *  placements want the same three facts and the same two buttons: who is
 *  asking, the code both screens must show, and how long is left before the
 *  request sweeps itself away.
 *
 *  It is not a modal and never takes focus: pairing is answered in a second,
 *  and stealing the caret from someone mid-sentence to show them a code they
 *  have to walk to another machine to compare is the wrong trade. */
export function PairInboundCard({
  req,
  busy,
  pending,
  left,
  onAnswer,
}: {
  req: PairRequest
  /** Another answer is in flight (any request) — both buttons stay disabled. */
  busy: boolean
  /** This request is the one being answered right now. */
  pending: boolean
  /** Formatted mm:ss remaining, '' when the server reported none. */
  left: string
  onAnswer(confirm: boolean): void
}) {
  const who = req.peer_name || req.peer_addr
  return (
    <section class="pair-callout request" aria-labelledby={`req-${req.id}`}>
      <div class="pair-callout-head">
        <span class="pair-callout-plate" aria-hidden="true">
          <Icon name="devices" size={15} />
        </span>
        <div class="pair-callout-copy">
          <p class="pair-callout-title" id={`req-${req.id}`}>
            {t('nodes.pair.requestFrom', { name: who })}
          </p>
          {/* The address is already in the title when no name came with the
              request; repeating it underneath is the same fact twice. */}
          {req.peer_name && req.peer_name !== req.peer_addr && (
            <p class="pair-callout-sub">{req.peer_addr}</p>
          )}
        </div>
        {left !== '' && (
          <span class="pair-callout-clock mono" title={t('nodes.pair.expiresAt')}>
            {left}
          </span>
        )}
      </div>

      {req.state === 'confirmed' ? (
        <p class="pair-callout-wait">
          <span class="spinner spinner-inline" aria-hidden="true" />
          {t('nodes.pair.joining')}
        </p>
      ) : (
        <>
          <div class="pair-callout-code">
            <span class="pair-code-chip mono" title={t('nodes.pair.code')}>
              {req.code}
            </span>
            <span class="pair-callout-code-hint">{t('nodes.pair.requestHint')}</span>
          </div>
          <div class="pair-callout-actions">
            <button
              class="btn primary pair-cta"
              type="button"
              disabled={busy}
              onClick={() => onAnswer(true)}
            >
              {pending ? t('common.loading') : t('nodes.pair.answerCta', { name: who })}
            </button>
            <button
              class="btn ghost pair-cta-secondary"
              type="button"
              disabled={busy}
              onClick={() => onAnswer(false)}
            >
              {t('nodes.pair.reject')}
            </button>
          </div>
        </>
      )}
    </section>
  )
}

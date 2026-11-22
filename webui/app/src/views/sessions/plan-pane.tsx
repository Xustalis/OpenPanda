// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from 'preact/hooks'
import { t } from '../../i18n'
import { Markdown } from '../../md/render'

import { ConversationDialog } from './conversation-dialogs'

export function PlanPane({ version, text, resumesGoal, busy, review, onAdjust, onConfirm, onClose, onReview, onDismiss }: {
  version: number
  text: string
  resumesGoal: boolean
  busy: boolean
  /** Opened from the card's button rather than its body: focus the note. */
  review: boolean
  /** Send the note back to the model as the next planning turn. */
  onAdjust(note: string): Promise<boolean>
  onConfirm(): Promise<void>
  onReview(): void
  onDismiss(): void
  onClose(): void
}) {
  const [note, setNote] = useState('')
  const [sending, setSending] = useState(false)
  const trimmed = note.trim()

  async function adjust() {
    if (!trimmed || sending || busy) return
    setSending(true)
    try {
      if (await onAdjust(trimmed)) setNote('')
    } finally {
      setSending(false)
    }
  }

  return (
    <div class="plan-pane">
      <header class="plan-pane-head">
        <span class="plan-pane-title">{t('sessions.plan.proposal')} · {version}</span>
        <button type="button" class="btn ghost plan-pane-close" onClick={onClose}>
          {t('common.close')}
        </button>
      </header>

      <div class="plan-pane-body" tabindex={0}>
        <Markdown text={text} />
      </div>

      <footer class="plan-pane-foot">
        <button type="button" class="btn primary" onClick={onReview}>{t('sessions.plan.review')}</button>
      </footer>
      {review && <ConversationDialog title={t('sessions.plan.review')} onClose={onDismiss}>
        <p class="plan-pane-hint">{t('sessions.plan.decideHint')}</p>
        <textarea
          autofocus
          class="plan-pane-note"
          rows={3}
          value={note}
          disabled={busy || sending}
          placeholder={t('sessions.plan.adjustPlaceholder')}
          aria-label={t('sessions.plan.adjust')}
          onInput={(event) => setNote(event.currentTarget.value)}
          onKeyDown={(event) => {
            // Enter sends the note; Shift+Enter is a new line. The note is a
            // remark about the plan, not a document — one key to act on it.
            if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
              event.preventDefault()
              void adjust()
            }
          }}
        />
        <div class="modal-actions">
          <button type="button" class="btn ghost" onClick={onDismiss}>{t('common.cancel')}</button>
          <button type="button" class="btn ghost" disabled={!trimmed || busy || sending} onClick={() => void adjust()}>
            {t('sessions.plan.adjust')}
          </button>
          <button type="button" class="btn primary" disabled={busy || sending || !!trimmed} onClick={async () => {
            setSending(true)
            try { await onConfirm() } finally { setSending(false) }
          }}>
            {t(resumesGoal ? 'sessions.plan.implementGoal' : 'sessions.plan.implement')}
          </button>
        </div>
      </ConversationDialog>}
    </div>
  )
}

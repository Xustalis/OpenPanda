// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ComponentChildren } from 'preact'
import { useLayoutEffect, useRef, useState } from 'preact/hooks'
import { t } from '../../i18n'

export function ConversationDialog({ title, wide, onClose, children }: {
  title: string
  wide?: boolean
  onClose(): void
  children: ComponentChildren
}) {
  const dialog = useRef<HTMLDialogElement>(null)
  useLayoutEffect(() => {
    const el = dialog.current
    if (!el) return
    el.showModal()
    return () => el.close()
  }, [])

  return (
    <dialog
      ref={dialog}
      class={wide ? 'modal conversation-dialog plan-dialog' : 'modal conversation-dialog'}
      aria-label={title}
      onCancel={(event) => { event.preventDefault(); onClose() }}
    >
      <h2 class="modal-title">{title}</h2>
      {children}
    </dialog>
  )
}

export function GoalBudgetDialog({ used, limit, pending, onClose, onResume }: {
  used: number
  limit: number
  pending: boolean
  onClose(): void
  onResume(budget: number): Promise<boolean>
}) {
  const [input, setInput] = useState('')
  const budget = Number(input)
  const min = Math.max(used, limit) + 1
  const valid = /^\d+$/.test(input) && Number.isSafeInteger(budget) && budget >= min
  const title = t('sessions.goal.raiseBudget')
  return (
    <ConversationDialog title={title} onClose={onClose}>
      <form class="goal-budget-form" onSubmit={async (event) => {
        event.preventDefault()
        if (valid && !pending && await onResume(budget)) onClose()
      }}>
        <p class="modal-msg">{t('sessions.goal.budgetSummary', { used, limit })}</p>
        <label>
          {t('sessions.goal.newBudget')}
          <input
            type="number"
            class="input"
            inputMode="numeric"
            min={min}
            max={Number.MAX_SAFE_INTEGER}
            step={1}
            value={input}
            onInput={(event) => setInput(event.currentTarget.value)}
            autofocus
          />
        </label>
        <div class="modal-actions">
          <button type="button" class="btn ghost" onClick={onClose}>{t('common.cancel')}</button>
          <button type="submit" class="btn primary" disabled={!valid || pending}>{title}</button>
        </div>
      </form>
    </ConversationDialog>
  )
}

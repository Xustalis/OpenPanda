// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'preact/hooks'
import { api, type SessionOperation, type ConversationState, type ServerGoal } from '../../api/client'
import { busSubscribeChange } from '../../hooks'
import { Icon } from '../../components/icons'
import { t } from '../../i18n'

export type GoalPhase = 'ready' | 'running' | 'review' | 'paused' | 'finished' | 'interrupted' | 'cancelled' | 'blocked' | 'usage_limited' | 'budget_limited'
export interface ComposerGoal { id: string; text: string; phase: GoalPhase; round?: string; tokens?: number; tokenBudget?: number; reason?: string }
export function goalActive(goal?: ComposerGoal): boolean { return goal?.phase === 'running' || goal?.phase === 'review' }
export function goalNeedsMoreBudget(goal: ComposerGoal): boolean {
  return goal.tokenBudget != null && (goal.tokens ?? 0) >= goal.tokenBudget
}

function present(goal?: ServerGoal): ComposerGoal | undefined {
  return goal && {
    id: goal.id,
    text: goal.objective,
    phase: goal.status === 'active' ? 'running' : goal.status === 'complete' ? 'finished' : goal.status,
    round: goal.round_id,
    tokens: goal.tokens,
    tokenBudget: goal.token_budget,
    reason: goal.reason,
  }
}

function completionKey(goal?: ServerGoal): string | undefined {
  // A resumed goal can complete again under the same id.
  return goal?.status === 'complete' ? `${goal.id}:${goal.generation}:${goal.rounds}` : undefined
}

function dismissalStorageKey(sid: string): string { return `openpanda.dismissedGoal:${sid}` }

export function legacyGoalFor(sid: string): ComposerGoal | undefined {
  try {
    const goal = JSON.parse(sessionStorage.getItem('openpanda.composerGoals') || '{}')[sid]
    return goal?.text ? { ...goal, phase: 'interrupted' } : undefined
  } catch { return undefined }
}

export function useComposerGoals(sessionId: string | null, operation: SessionOperation | undefined, stopping: boolean) {
  const [states, setStates] = useState<Record<string, ConversationState>>({})
  const [dismissed, setDismissed] = useState<Record<string, string>>({})
  const current = useRef(states)
  const nextRequest = useRef(0)
  const appliedRequests = useRef(new Map<string, number>())
  async function refresh(sid: string) {
    const request = ++nextRequest.current
    const state = await api.conversationState(sid)
    if (request > (appliedRequests.current.get(sid) ?? 0)) {
      appliedRequests.current.set(sid, request)
      current.current = { ...current.current, [sid]: state }
      setStates(current.current)
    }
    return state
  }
  useEffect(() => {
    if (!sessionId) return
    const sid = sessionId
    let alive = true
    let timer = 0
    async function poll() {
      try {
        await refresh(sid)
      } catch { /* Reconnect without starting a round. */ }
      if (alive) timer = window.setTimeout(poll, 1000)
    }
    const unsubscribe = busSubscribeChange(ev => {
      if (ev.kinds.includes('conversation')) void refresh(sid).catch(() => {})
    })
    void poll()
    return () => { alive = false; unsubscribe(); window.clearTimeout(timer) }
  }, [sessionId, operation?.id, stopping])
  const sid = sessionId ?? ''
  const state = states[sid]
  const goal = present(state?.goal)
  const completed = completionKey(state?.goal)
  let dismissedKey = dismissed[sid]
  if (completed && !dismissedKey) {
    try { dismissedKey = sessionStorage.getItem(dismissalStorageKey(sid)) ?? '' } catch { /* Storage is optional. */ }
  }
  return {
    goal,
    shownGoal: goal?.phase === 'cancelled' || (completed && completed === dismissedKey) ? undefined : goal,
    state,
    refresh,
    goalFor: (id: string) => present(current.current[id]?.goal),
    completedGoalKey: (id: string) => completionKey(current.current[id]?.goal),
    dismissCompletedGoal(id: string, key: string) {
      // An acknowledgement for an older completion may arrive out of order.
      if (completionKey(current.current[id]?.goal) !== key) return
      setDismissed((previous) => ({ ...previous, [id]: key }))
      try { sessionStorage.setItem(dismissalStorageKey(id), key) } catch { /* Storage is optional. */ }
    },
  }
}

export function GoalBar({ goal, pending, onStart, onPause, onCancel, onStop, onEdit }: {
  goal: ComposerGoal
  pending: boolean
  onStart(): void
  onPause(): void
  onCancel(): void
  onStop(): void
  onEdit(): void
}) {
  const active = goalActive(goal)
  // The bar only exists once the goal is published, so there is no draft
  // "start" step left: not active means resume.
  const nextAction = active ? 'pause' : goalNeedsMoreBudget(goal) ? 'raiseBudget' : 'resume'
  const actionLabel = t('sessions.goal.' + nextAction)
  const cancelLabel = t('sessions.goal.clear')
  const title = goal.text || t('sessions.goal.hint')
  return (
    <section class="composer-goal" aria-label={t('sessions.goal.label')}>
      <Icon name="target" size={17} />
      <div class="composer-goal-copy">
        <strong class="composer-goal-title" title={title}>{title}</strong>
        <span class="composer-goal-state" aria-live="polite">
          {t('sessions.goal.state.' + goal.phase)}
        </span>
      </div>
      <div class="composer-goal-actions">
        <button type="button" class="composer-goal-action" title={t('sessions.goal.edit')} aria-label={t('sessions.goal.edit')} disabled={pending} onClick={onEdit}><Icon name="pencil" size={16} /></button>
        {goal.round && <button type="button" class="composer-goal-action" title={t('sessions.goal.stop')} aria-label={t('sessions.goal.stop')} disabled={pending} onClick={onStop}><Icon name="stop" size={16} /></button>}
        {goal.phase !== 'finished' && (
          <button type="button" class="composer-goal-action composer-goal-action-main" title={actionLabel} aria-label={actionLabel}
            disabled={pending || !goal.text}
            onClick={active ? onPause : onStart}>
            <Icon name={active ? 'pause' : 'play'} size={16} />
          </button>
        )}
        <button type="button" class="composer-goal-action" title={cancelLabel} aria-label={cancelLabel}
          disabled={pending} onClick={onCancel}>
          <Icon name="x" size={16} />
        </button>
      </div>
    </section>
  )
}

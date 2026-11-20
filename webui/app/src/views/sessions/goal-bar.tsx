// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'preact/hooks'
import { api, type SessionOperation, type ConversationState, type ServerGoal } from '../../api/client'
import { busSubscribeChange } from '../../hooks'
import { Icon } from '../../components/icons'
import { t } from '../../i18n'

export type GoalPhase = 'ready' | 'running' | 'review' | 'paused' | 'finished' | 'interrupted' | 'cancelled' | 'blocked' | 'usage_limited' | 'budget_limited'
export interface ComposerGoal { id: string; text: string; phase: GoalPhase; round?: string; tokens?: number; reason?: string }
export function goalActive(goal?: ComposerGoal): boolean { return goal?.phase === 'running' || goal?.phase === 'review' }
function present(goal?: ServerGoal): ComposerGoal | undefined {
  return goal && { id: goal.id, text: goal.objective, phase: goal.status === 'active' ? 'running' : goal.status === 'complete' ? 'finished' : goal.status, round: goal.round_id, tokens: goal.tokens, reason: goal.reason }
}

export function legacyGoalFor(sid: string): ComposerGoal | undefined {
  try {
    const goal = JSON.parse(sessionStorage.getItem('openpanda.composerGoals') || '{}')[sid]
    return goal?.text ? { ...goal, phase: 'interrupted' } : undefined
  } catch { return undefined }
}

export function useComposerGoals(sessionId: string | null, operation: SessionOperation | undefined, stopping: boolean) {
  const [states, setStates] = useState<Record<string, ConversationState>>({})
  const current = useRef(states)
  async function refresh(sid: string) {
    const state = await api.conversationState(sid)
    current.current = { ...current.current, [sid]: state }
    setStates(current.current)
    return state
  }
  useEffect(() => {
    if (!sessionId) return
    let alive = true
    let timer = 0
    async function poll() {
      try {
        const state = await api.conversationState(sessionId!)
        if (alive) { current.current = { ...current.current, [sessionId!]: state }; setStates(current.current) }
      } catch { /* Reconnect without starting a round. */ }
      if (alive) timer = window.setTimeout(poll, 1000)
    }
    const unsubscribe = busSubscribeChange(ev => { if (ev.kinds.includes('conversation')) void refresh(sessionId!) })
    void poll()
    return () => { alive = false; unsubscribe(); window.clearTimeout(timer) }
  }, [sessionId, operation?.id, stopping])
  return { goal: present(states[sessionId ?? '']?.goal), state: states[sessionId ?? ''], refresh, goalFor: (sid: string) => present(current.current[sid]?.goal) }
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
  const nextAction = active ? 'pause' : 'resume'
  const actionLabel = t('sessions.goal.' + nextAction)
  const cancelLabel = t('sessions.goal.clear')
  const showState = true
  const title = goal.text || t('sessions.goal.hint')
  return (
    <section class="composer-goal" aria-label={t('sessions.goal.label')}>
      <Icon name="target" size={17} />
      <div class="composer-goal-copy">
        <strong class="composer-goal-title" title={title}>{title}</strong>
        <span class={showState ? 'composer-goal-state' : 'sr-only'} aria-live="polite">
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

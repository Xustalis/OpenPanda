// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'preact/hooks'
import { api, type SessionOperation, type ConversationState, type ServerGoal } from '../../api/client'
import { completionKey, onCompletionAcknowledged } from './goal-dismissal'
import { busSubscribeChange } from '../../hooks'
import { Icon } from '../../components/icons'
import { t } from '../../i18n'

export type GoalPhase = 'ready' | 'running' | 'review' | 'paused' | 'finished' | 'interrupted' | 'cancelled' | 'blocked' | 'usage_limited' | 'budget_limited'
export interface ComposerGoal { id: string; text: string; phase: GoalPhase; round?: string; rounds?: number; waitingFor?: string; tokens?: number; tokenBudget?: number; reason?: string }
export function goalActive(goal?: ComposerGoal): boolean { return goal?.phase === 'running' || goal?.phase === 'review' }
export function goalNeedsMoreBudget(goal: ComposerGoal): boolean {
  return goal.tokenBudget != null && (goal.tokens ?? 0) >= goal.tokenBudget
}

function present(goal?: ServerGoal): ComposerGoal | undefined {
  return goal && {
    id: goal.id,
    text: goal.objective,
    phase: goal.status === 'active' ? 'running' : goal.status === 'complete' ? 'finished' : goal.status,
    round: goal.round_id, rounds: goal.rounds, waitingFor: goal.waiting_for,
    tokens: goal.tokens,
    tokenBudget: goal.token_budget,
    reason: goal.reason,
  }
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
  const [failedSession, setFailedSession] = useState('')
  const current = useRef(states)
  const nextRequest = useRef(0)
  const appliedRequests = useRef(new Map<string, number>())
  function apply(sid: string, state: ConversationState, request = ++nextRequest.current) {
    if (request > (appliedRequests.current.get(sid) ?? 0)) {
      appliedRequests.current.set(sid, request)
      current.current = { ...current.current, [sid]: state }
      setStates(current.current)
    }
  }
  async function refresh(sid: string) {
    const request = ++nextRequest.current
    const state = await api.conversationState(sid)
    apply(sid, state, request)
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
        if (alive) setFailedSession('')
      } catch {
        if (alive) setFailedSession(sid)
      }
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
    completionDismissed: Boolean(completed && completed === dismissedKey),
    failed: failedSession === sessionId,
    apply,
    state,
    refresh,
    goalFor: (id: string) => present(current.current[id]?.goal),
    onMessageAccepted(id: string) {
      return onCompletionAcknowledged(() => current.current[id]?.goal, key => {
        setDismissed(previous => ({ ...previous, [id]: key }))
        try { sessionStorage.setItem(dismissalStorageKey(id), key) } catch { /* Storage is optional. */ }
      })
    },
  }
}

type GoalAction = 'pause' | 'resume' | 'stop' | 'clear'
export function GoalPane({ goal, draft, editing, onDraft, onEdit, onCancelEdit, pending, canStart, reviewId, onReply, onCreate, onSave, onControl }: {
  goal?: ComposerGoal; draft?: string; editing: boolean; pending: boolean; canStart: boolean; reviewId?: string
  onReply(): void; onDraft(text: string): void; onEdit(): void; onCancelEdit(): void
  onCreate(text: string): Promise<void>; onSave(text: string): Promise<void>; onControl(action: GoalAction, budget?: number): Promise<boolean>
}) {
  const editor = useRef<HTMLTextAreaElement>(null)
  const [limit,setLimit] = useState('')
  const writing = !goal || editing
  useEffect(() => { if (writing && !pending) editor.current?.focus() }, [writing, pending])
  const text = draft ?? goal?.text ?? ''
  if (writing) return <form class="goal-form" onSubmit={event => {
    event.preventDefault()
    if (!pending && text.trim() && (goal || canStart)) void (goal ? onSave(text.trim()) : onCreate(text.trim()))
  }}>
    <label for="goal-objective">{t(goal ? 'sessions.goal.edit' : 'goal.define')}</label>
    <p id="goal-write-hint">{t('goal.writeHint')}</p>
    <textarea ref={editor} id="goal-objective" aria-describedby="goal-write-hint" rows={6} value={text} placeholder={t('goal.example')} disabled={pending} onInput={e => onDraft(e.currentTarget.value)} />
    {goal && goalActive(goal) && <p>{t('goal.editHint')}</p>}
    {!goal && !canStart && <p role="status">{t('sessions.goal.busy')}</p>}
    <div class="goal-controls">{goal && <button class="btn" type="button" disabled={pending} onClick={onCancelEdit}>{t('common.cancel')}</button>}
      <button class="btn primary" type="submit" disabled={pending || !text.trim() || (goal ? text.trim() === goal.text : !canStart)}><Icon name={goal ? 'check' : 'play'} size={14} />{t(goal ? 'common.save' : 'goal.start')}</button></div>
  </form>
  const active = goalActive(goal)
  const phase = active && reviewId ? 'review' : goal.phase
  const limited = goalNeedsMoreBudget(goal)
  return <div class="goal-overview">
    <div class="goal-phase" role="status"><Icon name={phase === 'finished' ? 'check' : active ? 'activity' : 'pause'} size={15} />{t(active && goal.waitingFor === 'input' ? 'goal.waitInput' : 'sessions.goal.state.' + phase)}</div>
    <h2>{goal.text}</h2><button type="button" class="btn" onClick={onEdit} disabled={pending}><Icon name="pencil" size={14}/>{t('sessions.goal.edit')}</button>
    <div class="goal-progress"><strong>{goal.rounds ? t('goal.round', { n: goal.rounds }) : t('sessions.goal.state.ready')}</strong><span>{(goal.tokens ?? 0).toLocaleString()}{goal.tokenBudget ? ' / ' + goal.tokenBudget.toLocaleString() : ''} tokens</span></div>
    {goal.reason && <p class="goal-detail">{goal.reason}</p>}
    {active && goal.waitingFor === 'input' && <button type="button" class="btn" onClick={onReply}><Icon name="message" size={14} />{t('goal.reply')}</button>}
    {active && reviewId && <a class="btn" href={'#/task/' + encodeURIComponent(reviewId)}>{t('sessions.viewTask')} →</a>}
    {limited && !active && goal.phase !== 'finished' && <label>{t('sessions.goal.newBudget')}<input type="number" min={(goal.tokens ?? 0)+1} value={limit} onInput={e => setLimit(e.currentTarget.value)} /></label>}
    <div class="goal-controls goal-management">
      <button type="button" class="btn goal-clear" disabled={pending} onClick={() => void onControl('clear')}>
        <Icon name="trash" size={15} />{t('sessions.goal.clear')}
      </button>
      {goal.phase !== 'finished' && <button class="btn primary" disabled={pending || (!active && limited && (!Number.isSafeInteger(Number(limit)) || Number(limit) <= (goal.tokens ?? 0)))} onClick={() => void onControl(active ? 'pause' : 'resume',!active && limited ? Number(limit) : undefined)}>{t(active ? 'sessions.goal.pause' : limited ? 'sessions.goal.raiseBudget' : 'sessions.goal.resume')}</button>}
    {goal.round && <button class="btn" disabled={pending} onClick={() => void onControl('stop')}>{t('sessions.goal.stop')}</button>}</div>
  </div>
}

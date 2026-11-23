// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from 'preact/hooks'
import { api, isTaskStalled, type ApprovalScope, type ClarifySpec, type Task } from '../../api/client'
import { ClarifyCard } from '../../components/clarify-card'
import { confirmDialog } from '../../components/confirm'
import { ScopeSelect } from '../../components/scope-select'
import { isActiveState } from '../../components/task-activity'
import { isStalledReview, TaskFailureNotice, taskReviewLabel } from '../../components/task-failure'
import { toast, toastError } from '../../components/toast'
import { useAsync, useChangeSignal, useVisibleInterval } from '../../hooks'
import { t } from '../../i18n'

/** parseClarifySpec decodes the server-computed question_spec field (a
 *  ClarifySpec JSON string). Malformed input reads as no option set — the
 *  free-text question, when present, is still answerable. */
function parseClarifySpec(raw?: string): ClarifySpec | null {
  if (!raw) return null
  try {
    const spec = JSON.parse(raw) as ClarifySpec
    return spec && Array.isArray(spec.questions) && spec.questions.length > 0 ? spec : null
  } catch {
    return null
  }
}

/** Inline approval gate for a task referenced by a chat bubble.
 *
 *  It re-reads the task on every change signal, so a task that parks in review
 *  after the turn already finished (a queued run, a peer's delegation landing
 *  back here) still surfaces its decision in the conversation rather than only
 *  on the board. Approve/Reject call the same endpoints the queue and detail
 *  views use; needs_changed_input disables Approve, because consent alone
 *  cannot continue that task and a button that silently does nothing is worse
 *  than an honest disabled one. */
export function TaskApproval({
  taskId,
  preselect,
  hint,
}: {
  taskId: string
  preselect?: ApprovalScope
  /** The bubble's own copy of the row, when it has one — the thread's task
   *  list is already polled once per frame, so a second fetch per bubble is
   *  load with no fresher answer. */
  hint?: Task | null
}) {
  const change = useChangeSignal()
  const { data: fetched } = useAsync<Task | null>(
    () => (hint ? Promise.resolve(null) : api.taskBrief(taskId).catch(() => null)),
    [taskId, Boolean(hint)],
    change,
  )
  const task = hint ?? fetched
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // Free-text answer for an agent that asked in prose (no option set).
  const [answerText, setAnswerText] = useState('')
  // The remember scope: where this answer lands for the gates that come after.
  // The server names its default (the project's configured scope) so the card
  // preselects it; the user can still pick differently per decision.
  const [scope, setScope] = useState<ApprovalScope>(preselect ?? 'once')
  useEffect(() => {
    if (preselect) setScope(preselect)
  }, [preselect])
  // A stalled task emits no SSE event — nothing changes — so the badge can
  // only appear on a local clock tick.
  const [, setStallTick] = useState(0)
  useVisibleInterval(() => setStallTick((v) => v + 1), 30_000)

  if (!task) return null
  if (task.state !== 'review') {
    // The other half of "no silent stall": a task the conversation spawned
    // that stopped moving without reaching a decision shows it right in the
    // thread, not only on the board.
    if (!isTaskStalled(task)) return null
    return (
      <div class="task-approval" data-testid="chat-task-stalled">
        <span class="badge yellow">{t('task.stalled')}</span>
        <span class="dim">{t('task.stalledHint')}</span>
      </div>
    )
  }

  async function act(fn: () => Promise<unknown>) {
    setBusy(true)
    setError('')
    try {
      await fn()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  if (isStalledReview(task) && task.needs_followup) {
    return (
      <div class="task-approval" data-testid="chat-task-stalled-review">
        <TaskFailureNotice task={task} />
        <button
          type="button"
          class="btn primary small"
          disabled={busy}
          onClick={() => void act(async () => {
            const out = await api.continueTask(task.id)
            toast(out.warning || t('detail.continuedToast'), out.warning ? 'info' : 'success')
          })}
        >
          {t('detail.stalledRetry')}
        </button>
        <button
          type="button"
          class="btn danger small"
          disabled={busy}
          onClick={() => void act(() => api.reject(task.id))}
        >
          {t('detail.stalledEnd')}
        </button>
        {error && <p class="gate-error">{error}</p>}
      </div>
    )
  }

  // A task the agent parked on a question is not waiting for a yes/no: it is
  // waiting for an answer. Show the same choice card the running turn uses,
  // and resume through the approval endpoint with the answer attached.
  const parkedSpec = parseClarifySpec(task.question_spec)
  const parkedQuestionText = (task.question ?? '').trim()
  if (task.approval_disposition === 'resume_execution' && (parkedQuestionText || parkedSpec)) {
    return (
      <div class="task-approval is-clarify" data-testid="chat-task-question">
        <div class="clarify-head">
          <span class="badge yellow">{t('state.review')}</span>
          <span class="dim review-kind">{t('detail.reviewKind.resume_execution')}</span>
        </div>
        {parkedSpec ? (
          <ClarifyCard
            spec={parkedSpec}
            onSubmit={(answers) => void act(() => api.approveWithAnswer(task.id, { answers }))}
          />
        ) : (
          <form
            class="clarify-other"
            onSubmit={(e) => {
              e.preventDefault()
              const text = answerText.trim()
              if (!text) return
              void act(() => api.approveWithAnswer(task.id, { answer: text }))
            }}
          >
            <div class="clarify-question">{parkedQuestionText}</div>
            <input
              value={answerText}
              onInput={(e) => setAnswerText(e.currentTarget.value)}
              placeholder={t('clarify.otherPlaceholder')}
              disabled={busy}
            />
            <button type="submit" class="btn primary small" disabled={busy || !answerText.trim()}>
              {t('clarify.submit')}
            </button>
          </form>
        )}
        <div class="clarify-actions">
          <button
            class="btn danger small"
            disabled={busy}
            onClick={() => void act(() => api.reject(task.id, t('detail.rejectedViaWeb'), scope))}
          >
            {t('detail.reject')}
          </button>
        </div>
        {error && <p class="gate-error">{error}</p>}
      </div>
    )
  }
  // Failed execution cannot be accepted as completed work. A separate retry
  // is offered only when the server found a current failed execution; policy
  // parks keep their approval guard and their own instructions.
  const blocked = task.approval_disposition === 'needs_changed_input'
  const retry = task.retry_available === true
  // A task with MAX objectives whose stored result carries no acceptance
  // evidence can never be approved: the assessments the gate demands come from
  // the reviewing model, and its verdict never arrived. Offer the re-review
  // (the stored work, judged again) instead of a button that only fails.
  const rejudge = task.needs_rejudge === true
  const followup = task.needs_followup === true
  // ...and one of those failures is not about the prompt at all: the server
  // classified a park whose reason is the agent's own model configuration
  // (the endpoint refused its model id or login). Rewording the request
  // cannot fix that, so the card names the configuration instead of asking
  // for new input.
  const modelConfig = blocked && task.review_cause === 'model_config'

  return (
    <div class="task-approval" data-testid="chat-task-approval">
      <span class="badge yellow">{t('state.review')}</span>
      {task.approval_disposition && (
        <span class="dim review-kind">
          {taskReviewLabel(task)}
        </span>
      )}
      <TaskFailureNotice task={task} />
      {!retry && !rejudge && !followup && <ScopeSelect value={scope} onChange={setScope} disabled={busy} />}
      <button
        class="btn primary small"
        disabled={busy || (blocked && !retry)}
        onClick={() => void act(async () => {
          if (rejudge) {
            await api.rejudgeTask(task.id)
            toast(t('detail.rejudgeStartedToast'), 'success')
          } else if (followup) {
            const out = await api.continueTask(task.id)
            toast(out.warning || t('detail.continuedToast'), out.warning ? 'info' : 'success')
          } else if (retry) {
            await api.retryTask(task.id)
            toast(t('detail.retryStartedToast'), 'success')
          } else await api.approve(task.id, scope)
        })}
      >
        {rejudge ? t(busy ? 'detail.rejudging' : 'detail.rejudge')
          : followup ? t('detail.continue') : retry ? t(busy ? 'detail.retrying' : 'common.retry') : t('detail.approve')}
      </button>
      <button
        class="btn danger small"
        disabled={busy}
        onClick={() => void act(() => api.reject(task.id, t('detail.rejectedViaWeb'), scope))}
      >
        {t('detail.reject')}
      </button>
      {blocked && !retry && (
        <p class="gate-hint">
          {modelConfig ? t('detail.approveBlockedModelConfig') : t('detail.approveBlocked')}
        </p>
      )}
      {rejudge && <p class="gate-hint">{t('detail.rejudgeHint')}</p>}
      {error && <p class="gate-error">{error}</p>}
    </div>
  )
}

/** TaskQuickSend delivers a message into the task straight from its card in
 *  the thread — the same two modes the detail view and TUI expose: queue
 *  (the agent reads it at its next round) or inject (a steer that leads the
 *  next round, aborting the in-flight one when this console runs the task).
 *  Renders nothing once the task is terminal: a finished task refuses
 *  messages, and a button that can only fail is worse than none. */
export function TaskQuickSend({ taskId, hint }: { taskId: string; hint?: Task | null }) {
  const change = useChangeSignal()
  const { data: fetched } = useAsync<Task | null>(
    () => (hint ? Promise.resolve(null) : api.taskBrief(taskId).catch(() => null)),
    [taskId, Boolean(hint)],
    change,
  )
  const task = hint ?? fetched
  const [open, setOpen] = useState(false)
  const [text, setText] = useState('')
  const [images, setImages] = useState<File[]>([])
  const [busy, setBusy] = useState(false)

  if (!task || ['done', 'failed', 'cancelled', 'expired'].includes(task.state)) return null

  async function deliver(inject: boolean) {
    const body = text.trim()
    if ((!body && !images.length) || busy) return
    setBusy(true)
    try {
      const uploaded = images.length && task?.session_id ? await api.uploadSessionFiles(task.session_id, images) : null
      const r = await api.sendTaskMessage(taskId, body, inject, uploaded?.files.map(file => file.mention) ?? [])
      toast(
        r.warning ||
          (r.status === 'interrupted' ? t('detail.msgInjectedToast') : t('detail.msgQueuedToast')),
        r.warning ? 'info' : 'success',
      )
      setText('')
      setImages([])
      setOpen(false)
    } catch (e) {
      toastError(e)
    } finally {
      setBusy(false)
    }
  }

  if (!open) {
    return (
      <button class="btn small" onClick={() => setOpen(true)}>
        {t('sessions.msgSend')}
      </button>
    )
  }
  return (
    <div class="task-quick-send">
      {task.session_id && <input type="file" accept="image/*" multiple disabled={busy}
        aria-label={t('detail.msgAttachImage')} onChange={e => setImages(Array.from(e.currentTarget.files ?? []))} />}
      <input
        class="input"
        type="text"
        placeholder={t('detail.msgPlaceholder')}
        value={text}
        onInput={(e) => setText((e.target as HTMLInputElement).value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && !e.isComposing && e.keyCode !== 229) void deliver(false)
          if (e.key === 'Escape') setOpen(false)
        }}
      />
      <button class="btn small" disabled={busy || (!text.trim() && !images.length)} onClick={() => deliver(false)}>
        {t('detail.msgQueue')}
      </button>
      <button class="btn small primary" disabled={busy || (!text.trim() && !images.length)} onClick={() => deliver(true)}>
        {t('detail.msgInject')}
      </button>
    </div>
  )
}

export function TaskCancel({ task }: { task: Task }) {
  const [busy, setBusy] = useState(false)
  if (!isActiveState(task.state)) return null

  async function cancel() {
    if (busy) return
    setBusy(true)
    try {
      if (await confirmDialog({ title: t('detail.cancelConfirmTitle'), message: t('detail.cancelConfirmMsg'),
        confirmLabel: t('detail.cancelTask') })) {
        await api.cancel(task.id)
        toast(t('detail.cancelledToast'), 'success')
      }
    } catch (error) {
      toastError(error)
    } finally {
      setBusy(false)
    }
  }

  return <button type="button" class="btn small" disabled={busy} onClick={cancel}>{t('detail.cancelTask')}</button>
}

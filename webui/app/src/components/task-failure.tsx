// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Task } from '../api/client'
import { t } from '../i18n'

/** The same error leads the transcript, task dock, board and detail page.
 * The server supplies it even on brief rows, where events/results are absent. */
export function taskReviewLabel(task: Task): string {
  if (task.needs_followup) return t('detail.continue')
  switch (task.review_cause) {
    case 'rate_limited': return t('detail.failureRateLimited')
    case 'input_rejected': return t('detail.failureInputRejected')
    case 'provider_error': return t('detail.failureProvider')
    case 'model_config': return t('detail.reviewCauseModelConfig')
  }
  if (task.retry_available) return t('detail.executionFailed')
  return task.approval_disposition ? t(`detail.reviewKind.${task.approval_disposition}`) : ''
}

export function TaskFailureNotice({ task }: { task: Task }) {
  if (task.state !== 'review' || !task.review_reason) return null
  if (!task.needs_followup && !task.retry_available && task.approval_disposition !== 'needs_changed_input') return null
  let hint = t('detail.retryHint')
  if (task.review_cause === 'rate_limited') hint = t('detail.retryRateLimitedHint')
  else if (task.review_cause === 'input_rejected') hint = t('detail.retryInputRejectedHint')
  else if (task.review_cause === 'model_config') hint = t('detail.retryModelConfigHint')
  if (!task.retry_available) hint = t(task.review_cause === 'model_config'
    ? 'detail.approveBlockedModelConfig' : 'detail.needsChangedInputHint')
  if (task.needs_followup) hint = t('detail.continueHint')
  return (
    <div class="task-failure" role="alert" data-testid="task-failure">
      <strong>{taskReviewLabel(task)}</strong>
      <pre>{task.review_reason}</pre>
      <p>{hint}</p>
    </div>
  )
}

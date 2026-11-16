// SPDX-License-Identifier: AGPL-3.0-or-later
import type { Task, TaskEvent } from '../api/client'
import { t } from '../i18n'
import { executionAttribution } from './execution-steps'

/** Attribution follows actual result/attempt events, never the planned agent. */
export function TaskExecution({ task, events }: { task: Task; events: TaskEvent[] }) {
 const { executor, mode } = executionAttribution(task, events)
 if (!executor && !mode) return null
 return <dl class="task-execution-meta">
  {executor && <><dt>{t('activity.executor')}</dt><dd>{executor}</dd>
   <dt>{t('activity.coordinator')}</dt><dd>{executor}</dd></>}
  {mode && <><dt>{t('activity.coordination')}</dt><dd>{t(mode === 'IMMEDIATE' ? 'activity.immediate' : 'activity.roundEnd')}</dd></>}
 </dl>
}

// SPDX-License-Identifier: AGPL-3.0-or-later
import type { TaskEvent } from '../api/client.ts'
import { taskSteps, type ExecutionStep } from './execution-steps.ts'
import { deliveryPayload } from './task-delivery.ts'
import { t } from '../i18n/index.ts'

export function taskWorkedDuration(from: string, to: string): string {
  const elapsed = Date.parse(to) - Date.parse(from)
  if (!Number.isFinite(elapsed) || elapsed < 0) return ''
  let seconds = Math.round(elapsed / 1000)
  const parts: string[] = []
  for (const [unit, width] of [['d', 86400], ['h', 3600], ['m', 60], ['s', 1]] as const) {
    const count = Math.floor(seconds / width)
    if (count || (unit === 's' && !parts.length)) parts.push(`${count}${t('time.unit.' + unit)}`)
    seconds %= width
    if (parts.length === 2) break
  }
  return parts.join(' ')
}

export function turnActivitySteps(events: TaskEvent[], active: boolean, attemptId?: string,
  finalText = ''): ExecutionStep[] {
  // Unidentified legacy logs remain usable, but a retry must not show
  // another attempt's activity as part of its own turn.
  let scoped = events
  if (attemptId) {
    const attempts = events.map(event => deliveryPayload(event.data).attempt_id)
    if (attempts.some(id => typeof id === 'string' && id !== '')) {
      scoped = events.filter((_, i) => attempts[i] === attemptId)
    }
  }
  const steps = taskSteps(scoped, active).filter(step =>
    step.kind === 'tool' || step.kind === 'gap' || (step.kind === 'text' && step.output?.trim()))
  if (!active && finalText.trim()) {
    const last = steps.filter(step => step.kind === 'text').at(-1)
    // The same final message can arrive as both an item and the result.
    // Do not remove other prose or try to match paraphrases.
    const normalize = (text: string) => text.replace(/\r\n/g, '\n').trim()
    if (last?.output && normalize(last.output) === normalize(finalText)) {
      return steps.filter(step => step !== last)
    }
  }
  return steps
}

export function activityToolKind(step: ExecutionStep): 'command' | 'image' | 'tool' {
  const input = step.input
  if (/^(bash|shell|terminal|exec_command|command_execution)$/i.test(step.title) ||
      (input && typeof input === 'object' && ('command' in input || 'cmd' in input))) return 'command'
  if (step.image && /^(read|read_file|view_image|open_image)$/i.test(step.title.split(/[.:]/).at(-1) ?? '')) return 'image'
  return 'tool'
}

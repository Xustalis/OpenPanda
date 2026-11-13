// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Task, TaskEvent } from '../api/client.ts'
import { liveStepOf, mergeLive, type LiveStep } from './task-live.ts'

/** Result fields are untrusted adapter data, not proof of successful checks. */
export function deliveryPayload(result?: string): Record<string, unknown> {
  try {
    const value: unknown = JSON.parse(result || '{}')
    return value && typeof value === 'object' && !Array.isArray(value)
      ? value as Record<string, unknown> : {}
  } catch { return {} }
}

export function deliveryText(result?: string): string {
  const payload = deliveryPayload(result)
  for (const key of ['result', 'answer', 'stdout', 'stderr', 'failed']) {
    const value = payload[key]
    if (typeof value === 'string' && value.trim()) return value.trim()
  }
  return ''
}

/** A readable first screen, with the rest still available rather than dropped. */
export function deliveryPreview(text: string, limit = 6000): { text: string; more: boolean } {
  const runes = [...text]
  if (runes.length <= limit) return { text, more: false }
  const prefix = runes.slice(0, limit).join('')
  const paragraph = prefix.lastIndexOf('\n\n')
  return { text: prefix.slice(0, paragraph > limit / 2 ? paragraph : prefix.length) + '\n\n…', more: true }
}

export function deliveryFacts(task: Pick<Task, 'result' | 'files_changed' | 'verification' | 'state'>) {
  const payload = deliveryPayload(task.result)
  const files = (task.files_changed ?? (Array.isArray(payload.files_changed) ? payload.files_changed : []))
    .filter((path): path is string => typeof path === 'string' && path.trim() !== '')
  const raw = task.verification || payload.verification
  const verification = typeof raw === 'string' && ['verified', 'failed', 'partial', 'skipped'].includes(raw)
    ? raw : 'unknown'
  const followup = (typeof payload.followup === 'string' ? payload.followup.trim() : '') ||
    ((task.state !== 'done' || payload.needs_followup === true) && typeof payload.verdict_reason === 'string'
      ? payload.verdict_reason.trim() : '')
  const remaining = task.state === 'review' ? 'review'
    : task.state !== 'done' ? 'failed'
    : payload.needs_followup === true || verification === 'failed' || verification === 'partial'
      ? 'followup' : 'unreported'
  return { files: [...new Set(files)], verification, followup, remaining }
}

/** The preview narrates actual actions, not provider thoughts or usage counters. */
export function deliverySteps(events: TaskEvent[]): LiveStep[] {
  const steps: LiveStep[] = []
  for (const ev of mergeLive(events, [])) {
    if (ev.type === 'agent_usage' || ev.type === 'result') continue
    if (ev.type === 'agent_event') {
      const data = deliveryPayload(ev.raw)
      if (!['tool_use', 'tool_result', 'progress'].includes(String(data.ev))) continue
    }
    const step = liveStepOf(ev, true)
    if (!step) continue
    const previous = steps.at(-1)
    if (previous?.label === step.label && previous.summary === step.summary) continue
    steps.push(step)
  }
  return steps
}

/** A retry must not rewrite an earlier attempt's handoff with newer evidence. */
export function deliveryMatchesTask(task: Pick<Task, 'attempt_id' | 'updated_at' | 'state'>,
  attemptId?: string, reportedAt?: number, state?: string): boolean {
  if (attemptId) return task.attempt_id === attemptId
  if (state && task.state !== state && !(state === 'review' && task.state === 'done')) return false
  const updated = Date.parse(task.updated_at) / 1000
  return !reportedAt || !Number.isFinite(updated) || updated <= reportedAt
}

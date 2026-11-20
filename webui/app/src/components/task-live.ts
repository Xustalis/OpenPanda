// SPDX-License-Identifier: AGPL-3.0-or-later

/** Live-step extraction for a running task's transcript row.
 *
 *  The adapter's event stream is rich but flat: tool calls, results,
 *  thinking, progress heartbeats and supervision rounds all arrive as
 *  task_events rows. The chat wants a short human timeline, not the task
 *  page's full chain, so this module maps one row to one line and keeps the
 *  formatter (event-parser) as the single vocabulary.
 */

import type { TaskEvent, TraceEvent } from '../api/client.ts'
import { formatTaskEvent, toolArgSummary } from './event-parser.ts'
import { readableOutput } from './output-text.ts'
import { t } from '../i18n/index.ts'

export type LiveEvent = {
  key: string
  id: number
  ts: number
  type: string
  data?: unknown
  raw?: string
}

export type LiveStep = {
  key: string
  ts: number
  label: string
  summary: string
  tone: string
  /** Something the summary cannot say itself — today, how many lines of a
   *  tool result were dropped as undecodable (see output-text). */
  note?: string
  /** A silence heartbeat ("no output for 1m — agent still running"). It is
   *  not work, but it is the only signal a quiet agent gives; the row keeps
   *  the newest one instead of dropping it and looking frozen. */
  quiet?: boolean
}

/** Recent, attempt-tagged adapter activity shown before the full chain opens. */
export function recentAttemptSteps(events: LiveEvent[], attempt: string): LiveStep[] {
  if (!attempt) return []
  const steps: LiveStep[] = []
  for (const event of events) {
    if (event.type !== 'agent_event' && event.type !== 'progress') continue
    const data = eventData(event)
    if (data.attempt_id !== attempt) continue
    if (event.type === 'agent_event' && !['thinking', 'tool_use', 'tool_result', 'progress'].includes(String(data.ev))) continue
    const step = liveStepOf(event, data.ev === 'thinking')
    if (!step?.summary || step.quiet) continue
    const previous = steps.at(-1)
    if (previous?.label === step.label && previous.summary === step.summary) continue
    steps.push(step)
  }
  return steps.slice(-3)
}

/** The event kinds a running task should narrate. Lifecycle plumbing,
 *  session bookkeeping and silence heartbeats stay in the task page's full
 *  chain; the live row is the story, not the log. */
const LIVE_TYPES = new Set([
  'exec_agent_start',
  'progress',
  'agent_event',
  'supervision_round',
  'agent_usage',
  'agent_fallback',
  'judge_start',
  'delegate_request',
  'result',
  'review',
])

export function mergeLive(history: TaskEvent[], traces: TraceEvent[]): LiveEvent[] {
  const byKey = new Map<string, LiveEvent>()
  for (const e of history) {
    const key = e.id ? `id:${e.id}` : `ts:${e.ts}:${e.type}`
    byKey.set(key, { key, id: e.id ?? 0, ts: e.ts, type: e.type, raw: e.data })
  }
  for (const e of traces) {
    const key = e.id ? `id:${e.id}` : `ts:${e.ts}:${e.type}`
    // Saved logs are authoritative; SSE may clip strings for transport.
    if (byKey.has(key)) continue
    byKey.set(key, {
      key,
      id: e.id ?? 0,
      ts: e.ts,
      type: String(e.type),
      data: e.data,
      raw: e.data_raw,
    })
  }
  return [...byKey.values()].sort((a, b) => a.ts - b.ts || a.id - b.id)
}

function eventData(e: LiveEvent): Record<string, unknown> {
  if (e.data && typeof e.data === 'object') return e.data as Record<string, unknown>
  const raw = typeof e.data === 'string' ? e.data : e.raw
  if (!raw) return {}
  try {
    const parsed = JSON.parse(raw)
    return parsed && typeof parsed === 'object' ? (parsed as Record<string, unknown>) : {}
  } catch {
    return {}
  }
}

/** The latest message from the running agent. Nested agents and old attempts
 * remain available in the execution log. */
export function latestAgentText(events: LiveEvent[], attemptId?: string): string {
  for (let i = events.length - 1; i >= 0; i--) {
    const event = events[i]!
    if (event.type !== 'agent_event') continue
    const data = eventData(event)
    if (attemptId && data.attempt_id && data.attempt_id !== attemptId) continue
    if (data.ev === 'text' && !data.parent && typeof data.text === 'string' && data.text.trim()) {
      return data.text
    }
  }
  return ''
}

function firstLine(text?: string): string {
  if (!text) return ''
  return text
    .replace(/\r\n/g, '\n')
    .split('\n')
    .map((l) => l.trim())
    .find((l) => l !== '') ?? ''
}

function clip(text: string, max = 110): string {
  const runes = [...text]
  return runes.length > max ? runes.slice(0, max).join('') + '…' : text
}

export function clock(ts: number): string {
  return new Date(ts * 1000).toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}

/** One trace row as a compact, human line. It reuses `formatTaskEvent` for the
 *  vocabulary, then lifts what that formatter leaves in structured fields —
 *  a tool call's arguments, a result's first line — because a timeline row has
 *  one line, not a card. */
export function liveStepOf(e: LiveEvent, detail = false): LiveStep | null {
  if (!LIVE_TYPES.has(e.type)) return null
  const raw = e.raw ?? (e.data === undefined ? '{}' : JSON.stringify(e.data))
  const f = formatTaskEvent(e.type, raw)
  if (f.quiet) {
    // A finished run's process log does not need the watchdog's "still
    // running" heartbeats; the live row keeps the newest one so a quiet
    // agent never looks frozen.
    if (detail) return null
    return { key: e.key, ts: e.ts, label: f.label, summary: '', tone: 'dim', quiet: true }
  }
  let label = f.label
  let summary = f.summary ?? ''
  let tone = f.badgeClass
  let note = ''

  if (e.type === 'agent_event' && f.agent) {
    const b = f.agent
    switch (b.ev) {
      case 'tool_use':
        label = b.name || t('events.agent_tool_use')
        summary = toolArgSummary(b.input)
        tone = 'info'
        break
      case 'tool_result': {
        // A shell that lost its encoding hands back a wall of U+FFFD; the row
        // keeps the lines a reader can act on and says how many it dropped.
        const out = readableOutput(b.content ?? '')
        label = t('events.agent_tool_result')
        summary = detail ? out.text : firstLine(out.text)
        if (out.dropped > 0) note = t('events.outputOmitted', { n: String(out.dropped) })
        tone = b.isError ? 'danger' : 'dim'
        break
      }
      case 'thinking':
        label = t('events.agent_thinking')
        summary = detail ? (b.thinking ?? '') : firstLine(b.thinking)
        tone = 'dim'
        break
      case 'text':
        label = t('events.agent_text')
        summary = detail ? (b.text ?? '') : firstLine(b.text)
        tone = 'accent'
        break
    }
  } else if (e.type === 'progress') {
    const d = eventData(e)
    summary = String(d.note ?? d.msg ?? d.message ?? '')
    tone = 'info'
  } else if (e.type === 'review') {
    const d = eventData(e)
    summary = String(d.reason ?? d.rejected ?? '') || summary
    tone = 'warn'
  } else if (e.type === 'result') {
    // The terminal card carries stdout/stderr straight from the adapter, so
    // it is the other place a Windows code page reaches the transcript.
    const out = readableOutput(summary)
    summary = out.text
    if (out.dropped > 0) note = t('events.outputOmitted', { n: String(out.dropped) })
  }

  if (!summary) {
    summary = f.tags.map((tag) => tag.value).filter(Boolean).join(' · ')
  }
  const step: LiveStep = { key: e.key, ts: e.ts, label, summary: clip(summary, detail ? 800 : 110), tone }
  if (note) step.note = note
  return step
}


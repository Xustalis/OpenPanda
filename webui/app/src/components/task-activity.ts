// SPDX-License-Identifier: AGPL-3.0-or-later

// Turns a task's trace stream into the one-line "what is it doing right now"
// strip the queue cards show under an active task — the web twin of the
// TUI's rolling log line, so the kanban reads as live execution, not a
// static badge.

import type { Task, TraceEvent } from '../api/client'
import { t } from '../i18n/index.ts'
import { toolArgSummary, silenceNote } from './event-parser.ts'

/** Relative clock for the elapsed column — "3m" / "2h" / "5d". */
export function fmtElapsed(iso: string | undefined, now = Date.now()): string {
  if (!iso) return ''
  const s = Math.max(0, Math.floor((now - Date.parse(iso)) / 1000))
  if (s < 60) return `${s}${t('time.unit.s')}`
  if (s < 3600) return `${Math.floor(s / 60)}${t('time.unit.m')}`
  if (s < 86400) return `${Math.floor(s / 3600)}${t('time.unit.h')}`
  return `${Math.floor(s / 86400)}${t('time.unit.d')}`
}

function str(v: unknown): string | undefined {
  const s = typeof v === 'string' ? v : ''
  return s === '' ? undefined : s
}

function traceData(ev: TraceEvent): Record<string, unknown> {
  if (!ev.data) return {}
  if (typeof ev.data === 'object' && !Array.isArray(ev.data)) {
    return ev.data as Record<string, unknown>
  }
  if (typeof ev.data === 'string') {
    try {
      const v = JSON.parse(ev.data)
      if (v && typeof v === 'object' && !Array.isArray(v)) return v as Record<string, unknown>
    } catch {
      // opaque payload — no fields to read
    }
  }
  return {}
}

/** First line of a free-text payload, clipped — the live line is one line
 *  high, so a paragraph of agent prose would blow the card out. */
function clipLine(s: string | undefined, max = 88): string | undefined {
  if (!s) return undefined
  const line = (s.split('\n')[0] ?? '').trim()
  if (!line) return undefined
  return line.length > max ? line.slice(0, max - 1) + '…' : line
}

function num(v: unknown): number {
  return typeof v === 'number' && Number.isFinite(v) ? v : 0
}

/** 12_345 → "12.3k" — token counts only need order-of-magnitude precision. */
function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}

/** The human line for the most recent trace activity. Falls back to a
 *  state-appropriate waiting line so active cards always say something. */
export function latestActivity(
  traces: TraceEvent[],
  task: Partial<Pick<Task, 'state' | 'created_at' | 'updated_at'>>,
  now = Date.now(),
): string {
  const last = traces.length ? traces[traces.length - 1] : undefined
  const data = last ? traceData(last) : {}

  switch (last?.type) {
    case 'adapter_execution_profile': {
      const profile = data.profile as { agent?: string } | undefined
      return profile?.agent ? t('queue.live.exec', { agent: profile.agent }) : t('queue.live.execStart')
    }
    case 'agent_switch': return str(data.note) || t('events.agent_switch')
    case 'exec_agent_start': {
      const agent = str(data.agent) || str(data.adapter)
      return agent ? t('queue.live.exec', { agent }) : t('queue.live.execStart')
    }
    case 'supervision_round': {
      const round = data.round !== undefined ? String(data.round) : ''
      return round ? t('queue.live.supervise', { round }) : t('queue.live.judge')
    }
    case 'route_decision':
      return t('queue.live.route')
    case 'classify_result':
      return t('queue.live.classify')
    case 'plan_stage_changed': {
      const stage = str(data.stage_title) || str(data.stage_label) || str(data.stage_id)
      return stage ? t('queue.live.stage', { stage }) : t('queue.live.execStart')
    }
    case 'tier2_triggered':
      return t('queue.live.review')
    case 'delegation_hop': {
      const to = str(data.to_node) || str(data.to)
      return to ? t('queue.live.hop', { node: to }) : t('queue.live.route')
    }
    case 'delegate_request': {
      const child = str(data.title) || str(data.child) || ''
      if (data.isolate === true) {
        return child
          ? t('queue.live.spawnIsolated', { child })
          : t('queue.live.spawnIsolatedBare')
      }
      return child ? t('queue.live.spawn', { child }) : t('queue.live.spawnBare')
    }
    case 'isolated_merge': {
      const merged = typeof data.merged === 'number' ? data.merged : 0
      const conflicts = Array.isArray(data.conflicts) ? data.conflicts.length : 0
      return conflicts > 0
        ? t('queue.live.mergeConflicts', { merged: String(merged), conflicts: String(conflicts) })
        : t('queue.live.merge', { merged: String(merged) })
    }
    case 'artifact_transfer':
      return t('queue.live.transfer')

    // — Agent-activity stream: what the executor is doing *inside* the run.
    case 'agent_event': {
      const ev = str(data.ev)
      switch (ev) {
        case 'tool_use': {
          const tool = str(data.name) || 'tool'
          const arg = clipLine(toolArgSummary(data.input), 72)
          return arg
            ? t('queue.live.toolArg', { tool, arg })
            : t('queue.live.tool', { tool })
        }
        case 'thinking':
          return t('queue.live.thinking')
        case 'text':
          return clipLine(str(data.text)) || t('queue.live.writing')
        case 'tool_result':
          return t('queue.live.toolResult')
        default:
          return t('queue.live.execStart')
      }
    }
    case 'progress': {
      const note = str(data.note) || str(data.msg) || str(data.message)
      // The watchdog's heartbeat is English on the wire; say it in the UI's
      // language rather than pasting it into a Chinese card.
      return silenceNote(note) ?? clipLine(note) ?? t('queue.live.execStart')
    }
    case 'agent_usage': {
      const n =
        num(data.input_tokens) +
        num(data.output_tokens) +
        num(data.cache_read_tokens) +
        num(data.cache_write_tokens)
      return n > 0 ? t('queue.live.usage', { n: fmtTokens(n) }) : t('queue.live.execStart')
    }
    case 'agent_fallback': {
      const agent = str(data.agent) || str(data.to) || str(data.model)
      return agent ? t('queue.live.fallback', { agent }) : t('queue.live.execStart')
    }
    case 'judge_start':
      return t('queue.live.judge')
    case 'supervise': {
      const round = data.round !== undefined ? String(data.round) : ''
      return round ? t('queue.live.supervise', { round }) : t('queue.live.judge')
    }
    case 'result':
      return t('queue.live.resultIn')
  }

  // No useful trace yet — say what the state itself implies.
  switch (task.state) {
    case 'running': {
      const elapsed = fmtElapsed(task.updated_at, now)
      return elapsed ? t('queue.live.running', { d: elapsed }) : t('queue.live.execStart')
    }
    case 'submitted':
    case 'queued':
    case 'dispatched':
    case 'waiting_context':
      return t('queue.live.queued')
    default:
      return ''
  }
}

/** Whether the state is one whose card shows the live strip. */
export function isActiveState(state: string | undefined): boolean {
  return state === 'submitted' || state === 'queued' || state === 'dispatched' ||
    state === 'waiting_context' || state === 'running'
}

/** The jobs a thread still has hands on, in dispatch order.
 *
 *  The chat draws one employee row per job that is still moving, and it draws
 *  them after the last turn — not under the bubble that dispatched them. In a
 *  batch the two differ: the session finalizer files one report per task the
 *  moment each settles, so a colleague who finished first lands *below* a row
 *  that is still working, and the thread reads as if the running job were
 *  older than the work that overtook it. Whatever is still running is the
 *  newest thing in the thread, so it goes at the foot of it.
 *
 *  Dispatch order (oldest first) rather than the board's own recency order: a
 *  row that re-sorts itself every time its task takes a step is worse than one
 *  that stays put. A spawned sub-agent is not its own row — the dispatch card
 *  counts jobs, and a tree of three would read as three jobs. */
export function liveTaskIds(
  tasks: Iterable<Pick<Task, 'id' | 'state' | 'created_at' | 'spawn_depth'>>,
): string[] {
  const out: Pick<Task, 'id' | 'state' | 'created_at' | 'spawn_depth'>[] = []
  for (const task of tasks) {
    if (!isActiveState(task.state)) continue
    if (task.spawn_depth && task.spawn_depth > 0) continue
    out.push(task)
  }
  return out
    .sort((a, b) => a.created_at.localeCompare(b.created_at) || a.id.localeCompare(b.id))
    .map((task) => task.id)
}

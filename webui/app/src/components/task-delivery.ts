// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Task, TaskEvent } from '../api/client.ts'
import { parseBlocks, type Block, type Inline } from '../md/parse.ts'
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

const inlineText = (nodes: Inline[]): string => nodes.map(node => {
  if (node.t === 'text') return node.s.replace(/\[([^\]\n]+)\]\([^\s()]+\)/g, '$1')
  return 's' in node ? node.s : inlineText(node.kids)
}).join('')

function reportProse(blocks: Block[]): string[] {
  return blocks.flatMap(block => {
    if (block.t === 'para') return [inlineText(block.kids)]
    if (block.t === 'list') return block.items.map(item => inlineText(item.kids))
    if (block.t === 'quote') return reportProse(block.kids)
    return []
  })
}

/** One plain-text excerpt from the report, not the dispatch or verdict digest.
 * Keep Markdown links readable but non-interactive inside the document button. */
export function reportExcerpt(text: string, limit = 220): string {
  const blocks = parseBlocks(text.slice(0, 3600))
  const lines = reportProse(blocks).map(line => line.replace(/\s+/g, ' ').trim()).filter(Boolean)
  const prose = lines[0] ?? ''
  const runes = [...prose]
  return runes.length > limit ? `${runes.slice(0, limit).join('').trimEnd()}…` : prose
}

/** Verbatim reading cues. Only lift evidence from an explicitly labelled
 * section; adapter prose is not an independent verification badge. */
export function receiptCopy(text: string) {
  const blocks = parseBlocks(text.slice(0, 12000))
  const lines = reportProse(blocks)
    .map(line => line.replace(/\s+/g, ' ').trim()).filter(Boolean)
  const lead = lines[0] ?? ''
  const caveat = /^(?:未完成|未验证|尚未|未实测|待确认|限制[：:]|not (?:verified|tested)|unverified|limitations?\b|未検証|制限[：:]|nicht (?:getestet|verifiziert)|einschränkungen\b|sin verificar|limitaciones\b)/i
  const limitation = lines.slice(1).find(line => caveat.test(line))
  const next = limitation?.match(/^.*?(?:[。！？]|[.!?](?=\s|$)|$)/u)?.[0] || lines.slice(1).find(line =>
    line !== lead && !/^\[isolated\]/.test(line)) || ''
  const label = /^(?:实际校验|验证结果|verification|checks|test results|検証結果|prüfergebnisse|verificación)[:：]?$/i
  const section = blocks.findIndex(b => (b.t === 'heading' || b.t === 'para') && label.test(inlineText(b.kids).trim()))
  const evidence = section >= 0 ? reportProse(blocks.slice(section + 1, section + 2))[0] || '' : ''
  const clip = (s: string, n: number) => [...s].length > n ? [...s].slice(0, n).join('').trimEnd() + '…' : s
  return { lead: clip(lead, 260), evidence: evidence && evidence !== lead ? clip(evidence, 180) : '',
    detail: next !== lead && next !== evidence && !label.test(next) ? clip(next, 180) : '' }
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

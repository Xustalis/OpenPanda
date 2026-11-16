// SPDX-License-Identifier: AGPL-3.0-or-later
import { t } from '../i18n/index.ts'
import type { OperationActivity, Task, TaskEvent } from '../api/client.ts'
import { formatTaskEvent, imageRefOf, type ImageRef } from './event-parser.ts'

export interface ExecutionStep {
 id: string; title: string; status: string; ts: number; input?: unknown; output?: string
 thought?: boolean; prose?: boolean; raw?: string; truncated?: boolean; unavailable?: boolean; image?: ImageRef | null; executor?: string
}

export function executionAttribution(task: Task, events: TaskEvent[]): { executor: string; mode: string } {
 let finalExecutor = '', executor = '', mode = ''
 try {
  const value = JSON.parse(task.result || '{}').executor
  if (typeof value === 'string') finalExecutor = value
 } catch { /* legacy result */ }
 const parsed = events.flatMap(event => {
  try {
   const data = JSON.parse(event.data || '{}')
   return data && typeof data === 'object' ? [{ event, data }] : []
  } catch { return [] }
 })
 const current = !!task.attempt_id && parsed.some(({ data }) => data.attempt_id === task.attempt_id)
 for (const { event, data } of parsed) {
  if (task.attempt_id && (current ? data.attempt_id !== task.attempt_id : data.attempt_id && data.attempt_id !== task.attempt_id)) continue
  if (!executor && event.type === 'accept' && typeof data.owner === 'string') executor = data.owner
  if (typeof data.executor_id === 'string' && data.executor_id) executor = data.executor_id
  if (event.type === 'exec_agent_start' && typeof data.node_name === 'string' && data.node_name) executor = data.node_name
  if (event.type === 'adapter_execution_profile' && ['IMMEDIATE', 'ROUND_END'].includes(data.profile?.coordination)) mode = data.profile.coordination
 }
 return { executor: finalExecutor || executor, mode }
}

export function upsertActivity(records: OperationActivity[], next: OperationActivity): OperationActivity[] {
 const index = records.findIndex(r => r.id === next.id)
 if (index < 0) return [...records, next]
 return records.map((r, i) => i === index ? next : r)
}

export function operationSteps(records: OperationActivity[]): ExecutionStep[] {
 return records.map(r => ({ id: r.id, title: r.id === 'archive-unavailable' ? t('work.unavailable') : r.name, status: r.status, ts: r.started_at / 1000,
 input: r.input, output: r.output, truncated: r.truncated, unavailable: r.id === 'archive-unavailable', raw: JSON.stringify(r, null, 2) }))
}

// Tool IDs can repeat between rounds, executors and retry attempts. The pairing
// domain must follow execution identity, never a global tool-name map.
export function taskSteps(events: TaskEvent[], active = false, suppressResultBody = false): ExecutionStep[] {
 const steps: ExecutionStep[] = []
 const calls = new Map<string, ExecutionStep>()
 let round = 0
 let executor = ''
 let lastResult: ExecutionStep | undefined
 for (const e of events) {
  let data: Record<string, any> = {}
  try {
   const parsed = JSON.parse(e.data || '{}')
   if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) data = parsed
  } catch { /* legacy unstructured row */ }
  if (e.type === 'adapter_execution_profile') {
   const p = data.profile || {}
   steps.push({ id: 'profile:' + (e.id || e.ts), title: t('activity.coordination'), status: 'info', ts: e.ts, executor: data.executor_id,
    input: { agent: p.agent, [t('activity.coordination')]: t(p.coordination === 'IMMEDIATE' ? 'activity.immediate' : 'activity.roundEnd'), cwd: p.cwd }, raw: e.data })
   continue
  }
  if (e.type === 'supervision_round' || e.type === 'exec_agent_start') { round++ }
  if (e.type === 'exec_agent_start') executor = data.executor_id || data.node_name || ''
  const f = formatTaskEvent(e.type, e.data)
  if (f.quiet || e.type === 'agent_usage') continue
  const b = f.agent
  const domain = [data.attempt_id || data.attempt || '', data.executor_id || data.executor || '', data.round ?? round, b?.parent || ''].join(':')
  if (b?.ev === 'tool_result' && b.toolUseId) {
   const call = calls.get(domain + ':' + b.toolUseId)
   if (call) {
    call.output = b.content || ''; call.status = b.isError ? 'failed' : 'success'
    call.raw = JSON.stringify({ call: call.raw, result: data }, null, 2)
    continue
   }
  }
  const step: ExecutionStep = { id: e.id ? 'event:' + e.id : e.ts + ':' + e.type + ':' + steps.length,
   title: f.label, status: 'info', ts: data.source_ts || e.ts, raw: f.rawJson || e.data, executor: data.executor_id || data.executor || executor }
  if (b) {
   if (b.ev === 'tool_use') {
    step.title = b.name || f.label; step.input = b.input; step.status = active ? 'running' : 'unknown'
    if (b.id) calls.set(domain + ':' + b.id, step)
   } else if (b.ev === 'tool_result') { step.output = b.content; step.status = b.isError ? 'failed' : 'success' }
   else if (b.ev === 'thinking') { step.output = b.thinking; step.thought = true }
   else if (b.ev === 'text') { step.output = b.text; step.prose = true }
   else if (b.ev === 'transcript_truncated' || b.ev === 'activity_truncated' || b.ev === 'activity_unavailable') {
    step.truncated = true; step.unavailable = b.ev === 'activity_unavailable'; step.status = step.unavailable ? 'unknown' : 'truncated'
   }
   step.image = imageRefOf(b)
  } else if (f.thought) { step.output = f.thought; step.thought = true }
  else if (f.result) {
   step.output = [f.result.stdout, f.result.stderr].filter(Boolean).join('\n') || f.summary
   step.status = f.result.ok ? 'success' : 'failed'
   step.prose = true
   lastResult = step
  } else {
   step.output = f.summary
   if (f.tags.length) step.input = Object.fromEntries(f.tags.map(t => [t.key, t.value]))
   if (f.lifecycle?.node) step.executor = f.lifecycle.node
   if (f.badgeClass === 'danger' || f.lifecycle?.failed) step.status = 'failed'
  }
  steps.push(step)
 }
 if (suppressResultBody && lastResult) lastResult.output = t('detail.result.above')
 return steps
}


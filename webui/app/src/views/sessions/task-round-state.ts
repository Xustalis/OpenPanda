// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Task } from '../../api/client'
import { splitReportHead } from '../../components/report-head.ts'
import { deliveryMatchesTask, deliveryText } from '../../components/task-delivery.ts'
import type { ChatMsg } from './transcript-state'

const terminal = new Set(['done', 'failed', 'cancelled', 'expired'])
const queued = new Set(['submitted', 'queued', 'dispatched', 'waiting_context'])

export interface RoundJob {
  id: string
  receipt?: ChatMsg
  task?: Task
  reports: ChatMsg[]
}

export interface TaskRound {
  key: string
  operation?: string
  anchor?: string
  jobs: RoundJob[]
  settled: boolean
  running: number
  waiting: number
  notes: ChatMsg[]
  receipts: ChatMsg[]
}

export function currentRoundReport(job: RoundJob) {
  const task = job.task
  if (!task) return undefined
  return [...job.reports].reverse().find(report => splitReportHead(report.text)?.state === task.state &&
    (report.delivery_attempt_id || report.ts) && deliveryMatchesTask(task, report.delivery_attempt_id, report.ts))
}

export function roundReportText(job: RoundJob, report = currentRoundReport(job)) {
  return deliveryText(job.task?.result) || job.task?.delivery_excerpt || report?.delivery || ''
}

// The server binds real round membership transactionally with task creation.
// Old receipts are only a fallback; never infer membership from report time.
export function projectTaskRounds(msgs: ChatMsg[], tasks: Task[] | null, openOperation?: string): {
  rounds: TaskRound[]; hidden: Set<string>; assigned: Set<string>
} {
  const rounds = new Map<string, TaskRound>()
  const claimed = new Map<string, Set<string>>()
  const byID = new Map((tasks ?? []).map(task => [task.id, task]))
  const jobs = new Map<string, { round: TaskRound; job: RoundJob }>()
  const hidden = new Set<string>()
  const assigned = new Set<string>()
  const add = (id: string, operation?: string, receipt?: ChatMsg) => {
    let entry = jobs.get(id)
    if (!entry) {
      const key = operation ? `op:${operation}` : `task:${id}`
      let round = rounds.get(key)
      if (!round) {
        round = { key, operation, jobs: [], notes: [], receipts: [], settled: false, running: 0, waiting: 0 }
        rounds.set(key, round)
      }
      const job = { id, receipt, task: byID.get(id), reports: [] }
      round.jobs.push(job)
      entry = { round, job }
      jobs.set(id, entry)
      assigned.add(id)
    }
    if (receipt) {
      entry.job.receipt ??= receipt
      entry.round.anchor ??= receipt.k
      entry.round.receipts.push(receipt)
      hidden.add(receipt.k)
    }
  }
  for (const msg of msgs) {
    if (msg.kind !== 'task' || !msg.ref) continue
    const claims = claimed.get(msg.ref) ?? new Set<string>()
    claims.add(msg.operation_id || '')
    claimed.set(msg.ref, claims)
  }
  for (const task of [...(tasks ?? [])].sort((a, b) => (a.dispatch_order ?? Infinity) - (b.dispatch_order ?? Infinity) ||
    a.created_at.localeCompare(b.created_at) || a.id.localeCompare(b.id))) {
    if (task.spawn_depth || task.parent_id || !task.operation_id) continue
    add(task.id, task.operation_id)
  }
  for (const msg of msgs) {
    if (msg.kind === 'plan' && msg.operation_id) {
      const round = rounds.get(`op:${msg.operation_id}`)
      if (round?.jobs.some(job => job.task?.plan_meta?.plan_id === msg.ref)) {
        round.anchor ??= msg.k
        round.receipts.push(msg)
        hidden.add(msg.k)
      }
    }
    if (msg.kind !== 'task' || !msg.ref) continue
    const task = byID.get(msg.ref)
    if (task?.plan_meta?.plan_id || (task?.operation_id && msg.operation_id && task.operation_id !== msg.operation_id)) continue
    if (!jobs.has(msg.ref) && claimed.get(msg.ref)?.size !== 1) continue
    add(msg.ref, task?.operation_id || msg.operation_id, msg)
  }
  for (const task of tasks ?? []) {
    if (jobs.has(task.id) || claimed.has(task.id) || task.spawn_depth || task.parent_id || task.plan_meta?.plan_id) continue
    add(task.id, task.operation_id)
  }
  for (const msg of msgs) {
    if (msg.round_id && rounds.has(`op:${msg.round_id}`)) {
      hidden.add(msg.k) // stored report remains available to replay/export
      continue
    }
    if (msg.kind !== 'answer' || !msg.ref || !splitReportHead(msg.text)) continue
    const entry = jobs.get(msg.ref)
    if (!entry || (msg.operation_id && msg.operation_id !== entry.round.operation)) continue
    entry.job.reports.push(msg)
    hidden.add(msg.k)
  }
  for (const round of rounds.values()) {
    if (round.operation) {
      const same = msgs.filter(msg => msg.operation_id === round.operation)
      // Earlier than a late report, even while the entry reply is streaming.
      round.anchor = same.find(msg => msg.role === 'user')?.k ?? round.anchor ?? same.find(msg => msg.role === 'assistant' && !msg.ref)?.k
      round.notes = same.filter(msg => msg.role === 'assistant' && !msg.ref && !msg.round_id &&
        !msg.warning && !msg.result?.warning && (msg.kind === 'answer' || (msg.streaming && !msg.kind)))
      for (const note of round.notes) hidden.add(note.k)
    }
    round.running = round.jobs.filter(({ task }) => task?.state === 'running' && task.executing).length
    round.waiting = round.jobs.filter(({ task }) => task && queued.has(task.state)).length
    round.settled = Boolean(tasks && (openOperation === undefined || round.operation !== openOperation) && round.jobs.every(({ task }) =>
      task && terminal.has(task.state) && !task.dispatch_open && !task.needs_followup))
  }
  return { rounds: [...rounds.values()], hidden, assigned }
}

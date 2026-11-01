// SPDX-License-Identifier: AGPL-3.0-or-later

/** Who is speaking in one row of the transcript.
 *
 *  The thread used to be two voices: you on the right, the console on the
 *  left. But a dispatched task is not one voice — the panda splits the job up
 *  and hands it out, an employee does it and reports back, and the panda
 *  closes the round. Drawing all of that as one anonymous column is what made
 *  a finished task read like a log file: a duration caption, a task card, and
 *  a `[done] @agent` line with nobody attached to it.
 *
 *  So every turn gets a speaker, decided from what the turn already carries —
 *  no extra request, no new field on the wire:
 *
 *    - the dispatch turn (`kind=task|plan`) is the panda orchestrating;
 *    - a report turn whose head names an employee (`[done] @claude_code · …`)
 *      is that employee reporting;
 *    - a plain reply, a thought still streaming, an error, and the finalizer's
 *      headless report are the panda talking;
 *    - your own turn is you.
 *
 *  The panda's closing summary is not a turn at all — it is rolled up from the
 *  run (see `trailingRunSummary`) and drawn after the last report. */

import type { SessionTurn } from '../api/client.ts'
import { splitReportHead, type ReportHead } from './report-head.ts'

export type CastRole = 'plan' | 'report' | 'summary' | 'reply' | 'think' | 'error'

export interface Speaker {
  /** Which face the row wears. */
  who: 'panda' | 'agent' | 'user'
  /** Registry key of the employee (`claude_code`), when `who === 'agent'`. */
  agent: string
  /** What this row is doing in the conversation — the chip beside the name. */
  role: CastRole
}

/** The subset of a turn this module reads. */
export interface CastTurn {
  role: 'user' | 'assistant'
  kind?: 'answer' | 'task' | 'plan' | 'error'
  text?: string
  /** The task or plan this turn dispatched. */
  ref?: string
  streaming?: boolean
}

/** Display names for the CLIs the console ships marks for. A name that is not
 *  here is title-cased from its registry key, so a new hire is readable
 *  before anyone adds it to the table — `some_new_cli` becomes `Some New Cli`,
 *  which is wrong about the acronym but never unreadable. */
const AGENT_NAMES: Record<string, string> = {
  antigravity: 'Antigravity',
  claude_code: 'Claude Code',
  codex: 'Codex',
  deepseek_harness: 'DeepSeek Harness',
  grok_build: 'Grok Build',
  hermes: 'Hermes',
  openclaw: 'OpenClaw',
  opencode: 'OpenCode',
  pi: 'Pi',
  zcode: 'ZCode',
}

/** prettyAgent turns a registry key into the name a person would say. */
export function prettyAgent(key: string): string {
  const known = AGENT_NAMES[key.trim().toLowerCase()]
  if (known) return known
  const words = key.trim().split(/[_-]+/).filter(Boolean)
  if (words.length === 0) return key
  return words.map((w) => w[0]!.toUpperCase() + w.slice(1)).join(' ')
}

/** speakerOf decides who owns a turn. `report` is the parsed head, passed in
 *  because the caller needs it for the row's own chrome anyway. */
export function speakerOf(turn: CastTurn, report: ReportHead | null): Speaker {
  if (turn.role === 'user') return { who: 'user', agent: '', role: 'reply' }
  if (turn.kind === 'error') return { who: 'panda', agent: '', role: 'error' }
  if (report) {
    return report.agent
      ? { who: 'agent', agent: report.agent, role: 'report' }
      : { who: 'panda', agent: '', role: 'summary' }
  }
  if (turn.kind === 'task' || turn.kind === 'plan') {
    return { who: 'panda', agent: '', role: 'plan' }
  }
  // An empty streaming bubble is the panda thinking out loud; the moment a
  // delta lands it is a reply.
  if (turn.streaming && !turn.text) return { who: 'panda', agent: '', role: 'think' }
  return { who: 'panda', agent: '', role: 'reply' }
}

/** stripRefId removes the id the dispatch templates append.
 *
 *  `sessions.go` files a dispatch as `已派发任务：<title>（<id>）` and a plan as
 *  `已启动多阶段计划：<goal>（<n> 个阶段，<plan id>）` so the id survives into
 *  the model's history. In a chat row that parenthesis is noise twice over: the
 *  row already links to the work, and a uuid is not a thing a person reads. Only
 *  the turn's own id is stripped — a title that happens to end in parentheses
 *  keeps it. */
export function stripRefId(text: string, ref?: string): string {
  if (!ref) return text
  for (const tail of [`（${ref}）`, `(${ref})`]) {
    if (text.endsWith(tail)) return text.slice(0, -tail.length).replace(/[ \t]+$/, '')
  }
  // The plan template keeps the id inside the parenthesis, after a comma.
  for (const [tail, close] of [
    [`，${ref}）`, '）'],
    [`, ${ref})`, ')'],
  ] as const) {
    if (text.endsWith(tail)) return text.slice(0, -tail.length) + close
  }
  return text
}

/** A finished round, as the panda sums it up. */
export interface RunSummary {
  /** Employees that reported during the round, in the order they spoke. */
  agents: string[]
  /** The jobs they closed. */
  titles: string[]
  /** The last report's state — what the round ended on. */
  state: string
  /** Every report's state, in the order they landed. A round is a batch, so
   *  one employee's failure is not the batch's verdict: the roll-up reads the
   *  split off this instead of stamping the whole round with `state`. */
  outcomes: string[]
  /** Where the round's work lives, when the round dispatched exactly one thing:
   *  a task page, or the plan board. Empty when there is nothing single to
   *  point at. */
  ref: string
  refKind: 'task' | 'plan' | ''
}

/** States that end a round. `review` is included: the work has stopped, it is
 *  waiting on a person, and a round that says nothing there reads as still
 *  running. */
const SETTLED = new Set(['done', 'failed', 'cancelled', 'expired', 'review'])

/** States where a task still has an employee's hands on it. */
const IN_FLIGHT = new Set(['submitted', 'queued', 'dispatched', 'waiting_context', 'running'])

/** Review parks that still owe the round work.
 *
 *  `review` is two different things wearing one badge. An `accept_work` park
 *  holds finished output and asks a person to sign it off — the employee is
 *  done. A pre-execution park asks whether the employee may start at all:
 *  `resume_execution` (consent or a clarification question), `admission_review`
 *  (the trust gate), `needs_changed_input` (the budget ran out and the prompt
 *  must change). Nothing has been delivered in those cases, so a round that
 *  closed on a colleague's report would claim "本轮工作已全部完成" while an
 *  employee had not run a single step. */
const PARKED_BEFORE_WORK = new Set(['resume_execution', 'admission_review', 'needs_changed_input'])

/** roundStillOpen answers "is the batch this round dispatched finished?".
 *
 *  `trailingRunSummary` closes a round on the last report alone, which is
 *  right for a single job and wrong for a batch: the session finalizer files
 *  one report per task the moment each one settles, so the first employee
 *  home would close the round — "本轮工作已全部完成" — while its colleagues
 *  were still running, and the roll-up then rewrote itself (headcount,
 *  titles) every time another one landed. The transcript cannot see the
 *  batch; the task board can, so the caller hands its rows in here.
 *
 *  A row parked in review counts as in flight when approving it would resume
 *  work rather than accept it (see `PARKED_BEFORE_WORK`); rows that predate
 *  `approval_disposition` carry no disposition and keep the old reading — the
 *  store defaults them to `accept_work`. */
export function roundStillOpen(tasks: Iterable<{ state: string; approval_disposition?: string }>): boolean {
  for (const task of tasks) {
    if (IN_FLIGHT.has(task.state)) return true
    if (task.state === 'review' && PARKED_BEFORE_WORK.has(task.approval_disposition ?? '')) return true
  }
  return false
}

/** trailingRunSummary rolls up the round the thread just finished, or returns
 *  null while one is still open.
 *
 *  A round is the span between the newest dispatch and the last report: the
 *  panda handed work out, employees reported back, and nothing has happened
 *  since. Only then does the panda have something to close — a report with no
 *  dispatch above it is a single answer, not a round, and a round whose last
 *  word was the user's is still going. */
export function trailingRunSummary(turns: CastTurn[]): RunSummary | null {
  if (turns.length === 0) return null
  const last = turns[turns.length - 1]!
  if (last.role !== 'assistant' || last.streaming) return null
  const head = splitReportHead(last.text ?? '')
  if (!head || !SETTLED.has(head.state)) return null

  let start = -1
  for (let i = turns.length - 1; i >= 0; i--) {
    const t = turns[i]!
    if (t.kind === 'task' || t.kind === 'plan') {
      start = i
      break
    }
  }
  if (start === -1) return null

  const agents: string[] = []
  const titles: string[] = []
  const outcomes: string[] = []
  const refs: { ref: string; kind: 'task' | 'plan' }[] = []
  for (let i = start; i < turns.length; i++) {
    const t = turns[i]!
    if (t.ref && (t.kind === 'task' || t.kind === 'plan') && !refs.some((r) => r.ref === t.ref)) {
      refs.push({ ref: t.ref, kind: t.kind })
    }
    if (i === start) continue
    const h = splitReportHead(t.text ?? '')
    if (!h) continue
    if (SETTLED.has(h.state)) outcomes.push(h.state)
    if (h.agent && !agents.includes(h.agent)) agents.push(h.agent)
    if (h.title && !titles.includes(h.title)) titles.push(h.title)
  }
  // One destination only: a round that dispatched two things has no single
  // page to send the reader to, and a link that guesses is worse than none.
  const only = refs.length === 1 ? refs[0]! : null
  return { agents, titles, state: head.state, outcomes, ref: only?.ref ?? '', refKind: only?.kind ?? '' }
}

/** roundTally counts a round's reports by state, in one fixed order, so the
 *  same batch always reads the same way ("2 位完成、1 位失败"). */
export function roundTally(outcomes: string[]): { state: string; n: number }[] {
  const order = ['done', 'failed', 'cancelled', 'expired', 'review']
  const out: { state: string; n: number }[] = []
  for (const state of order) {
    const n = outcomes.filter((s) => s === state).length
    if (n > 0) out.push({ state, n })
  }
  return out
}

/** stateTone is the colour a settled state paints: green for work delivered,
 *  amber for a decision still owed, red for a job that will not deliver. */
export function stateTone(state: string): 'ok' | 'warn' | 'bad' {
  if (state === 'done') return 'ok'
  if (state === 'failed' || state === 'cancelled' || state === 'expired') return 'bad'
  return 'warn'
}

/** sharedTitlePrefix returns the head every job title in a round shares, when
 *  that head ends on a separator, or '' when there is nothing worth dropping.
 *
 *  A round's jobs usually share their dispatch template — "连通性测试：调用
 *  agent:claude_code", "…:codex", "…:deepseek_harness" — and printing the head
 *  three times buries the part that differs. Only a prefix that ends on a colon
 *  is cut: "Fix bug in " is half a phrase, and a reader who loses it cannot
 *  tell what the line means. */
export function sharedTitlePrefix(titles: string[]): string {
  if (titles.length < 2) return ''
  let prefix = titles[0]!
  for (const title of titles.slice(1)) {
    let i = 0
    while (i < prefix.length && i < title.length && prefix[i] === title[i]) i++
    prefix = prefix.slice(0, i)
  }
  const cut = Math.max(prefix.lastIndexOf('：'), prefix.lastIndexOf(':'))
  if (cut < 0) return ''
  const head = prefix.slice(0, cut + 1)
  // Every title must keep something to say once the head is gone.
  if (titles.some((title) => title.slice(head.length).trim() === '')) return ''
  return head
}

/** castAgent names the employee that ran a task, from the task's own trace.
 *
 *  The task row records a node (`owner`), not a person — the scheduler picks
 *  the CLI when it starts, and `exec_agent_start` is the only event that says
 *  which one. The newest such event wins, because a task that fell back to a
 *  second adapter has to be attributed to the one that actually finished. */
export function castAgent(traces?: { type: string; data?: any }[]): string {
  for (let i = (traces?.length ?? 0) - 1; i >= 0; i--) {
    const ev = traces![i]!
    if (ev.type !== 'exec_agent_start') continue
    const data = ev.data as Record<string, unknown> | undefined
    const agent = typeof data?.agent === 'string' ? data.agent : ''
    if (agent) return agent
    const adapter = typeof data?.adapter === 'string' ? data.adapter : ''
    if (adapter) return adapter
  }
  return ''
}

/** pinnedAgent names the employee a task was submitted to, from its
 *  `agent:<name>` requirement. Empty when the task named no one. */
export function pinnedAgent(requires?: string[]): string {
  for (const req of requires ?? []) {
    const trimmed = req.trim()
    if (!trimmed.startsWith('agent:')) continue
    const name = trimmed.slice('agent:'.length).trim()
    if (name) return name
  }
  return ''
}

/** dispatchAgent answers "whose job is this?" for one dispatched task.
 *
 *  The trace is the authority on who *ran* it, and it is the only source that
 *  can tell a fallback apart from the plan. But a task that never reached an
 *  adapter — it failed at routing, or was cancelled while queued — has no
 *  `exec_agent_start` at all, and reading the trace alone left exactly those
 *  rows without a face or a name: a fanned-out round drew two cards that named
 *  their employee and one that named nobody, which reads as "this job has no
 *  owner" instead of "this job's owner never started". The pin is what the
 *  submitter actually asked for, so it is the right answer for that row. */
export function dispatchAgent(task: {
  traces?: { type: string; data?: any }[]
  /** The adapter the row itself names (`task.agent`): the only signal a light
   *  list row carries, since it has no trace timeline to read. */
  agent?: string
  requires?: string[]
}): string {
  return castAgent(task.traces) || task.agent || pinnedAgent(task.requires)
}

/** What the cast row says in the slot where the employee's name goes.
 *
 *  The row answers "who got the job". Before `exec_agent_start` lands there is
 *  no name yet, and the row used to say "picking an employee" in every one of
 *  those states — so a queued or already-running task wore "picking an
 *  employee" right next to its own state chip ("已派发 · 正在挑选员工",
 *  "运行中 · 正在挑选员工"), contradicting the word beside it.
 *
 *  Only a freshly submitted task is still being routed, so only that state
 *  earns "picking". Queued and dispatched rows have an employee — the console
 *  just cannot name them until the run starts — and say so. Every other state
 *  (running, review, settled) gets no placeholder at all: the state chip is
 *  the whole truth there, and an invented word would only fight it. */
export function castPending(state: string): 'picking' | 'waiting' | '' {
  if (state === 'submitted') return 'picking'
  if (state === 'queued' || state === 'dispatched') return 'waiting'
  return ''
}

/** The turn kinds this module understands, read off a stored turn. */
export function castTurn(turn: SessionTurn & { streaming?: boolean }): CastTurn {
  return {
    role: turn.role,
    kind: turn.kind,
    text: turn.text,
    ref: turn.ref,
    streaming: turn.streaming,
  }
}

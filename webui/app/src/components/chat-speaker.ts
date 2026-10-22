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

/** States where a task still has an employee's hands on it. `review` is not
 *  one of them — the work has stopped and a person has the say. */
const IN_FLIGHT = new Set(['submitted', 'queued', 'dispatched', 'waiting_context', 'running'])

/** roundStillOpen answers "is the batch this round dispatched finished?".
 *
 *  `trailingRunSummary` closes a round on the last report alone, which is
 *  right for a single job and wrong for a batch: the session finalizer files
 *  one report per task the moment each one settles, so the first employee
 *  home would close the round — "本轮工作已全部完成" — while its colleagues
 *  were still running, and the roll-up then rewrote itself (headcount,
 *  titles) every time another one landed. The transcript cannot see the
 *  batch; the task board can, so the caller hands its states in here. */
export function roundStillOpen(states: Iterable<string>): boolean {
  for (const s of states) {
    if (IN_FLIGHT.has(s)) return true
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
  const refs: { ref: string; kind: 'task' | 'plan' }[] = []
  for (let i = start; i < turns.length; i++) {
    const t = turns[i]!
    if (t.ref && (t.kind === 'task' || t.kind === 'plan') && !refs.some((r) => r.ref === t.ref)) {
      refs.push({ ref: t.ref, kind: t.kind })
    }
    if (i === start) continue
    const h = splitReportHead(t.text ?? '')
    if (!h) continue
    if (h.agent && !agents.includes(h.agent)) agents.push(h.agent)
    if (h.title && !titles.includes(h.title)) titles.push(h.title)
  }
  // One destination only: a round that dispatched two things has no single
  // page to send the reader to, and a link that guesses is worse than none.
  const only = refs.length === 1 ? refs[0]! : null
  return { agents, titles, state: head.state, ref: only?.ref ?? '', refKind: only?.kind ?? '' }
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

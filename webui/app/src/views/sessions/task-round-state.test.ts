// SPDX-License-Identifier: AGPL-3.0-or-later

import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { Task } from '../../api/client'
import { currentRoundReport, projectTaskRounds, roundReportText } from './task-round-state.ts'
import { receiptCopy, reportExcerpt } from '../../components/task-delivery.ts'
import type { ChatMsg } from './transcript-state'

const at = '2026-11-18T12:00:00Z'
const row = (id: string, extra: Partial<Task> = {}): Task => ({ id, parent_id: '', attempt_id: 'current', project: '', title: id,
  owner: 'local', created_at: at, updated_at: at, state: 'done', operation_id: 'op-a', ...extra })
const prompt = (operation: string): ChatMsg => ({ k: operation, operation_id: operation, role: 'user', text: 'test' })
const receipt = (id: string, op?: string): ChatMsg => ({ k: `${id}-${op}`, role: 'assistant', kind: 'task', ref: id,
  operation_id: op, text: 'dispatch' })

test('orders by dispatch and only counts adapter-executing work', () => {
  const rows = [row('b', { dispatch_order: 2, state: 'running' }), row('a', { dispatch_order: 1, state: 'running', executing: true }),
    row('c', { dispatch_order: 3, state: 'queued', executing: true })]
  const round = projectTaskRounds([prompt('op-a')], rows).rounds[0]!
  assert.deepEqual(round.jobs.map(job => job.id), ['a', 'b', 'c'])
  assert.equal(round.running, 1)
  assert.equal(round.waiting, 1)
  assert.equal(round.anchor, 'op-a')
  assert.equal(round.settled, false)
})

test('late result returns to original round and retries retain previous attempts', () => {
  const previous: ChatMsg = { k: 'old', role: 'assistant', kind: 'answer', ref: 'a', text: '[failed] @codex · A', delivery_attempt_id: 'old' }
  const next: ChatMsg = { ...previous, k: 'next', text: '[done] @codex · A', delivery_attempt_id: 'current' }
  const msgs = [prompt('op-a'), receipt('a', 'op-a'), previous, prompt('op-b'), receipt('b', 'op-b'), next]
  const result = projectTaskRounds(msgs, [row('a'), row('b', { operation_id: 'op-b', state: 'failed' })])
  assert.deepEqual(result.rounds[0]?.jobs[0]?.reports.map(report => report.delivery_attempt_id), ['old', 'current'])
  assert.deepEqual(result.rounds[1]?.jobs[0]?.reports, [])
  assert.equal(result.hidden.has('next'), true)
  assert.equal(result.rounds[0]?.anchor, 'op-a')
})

test('a receiptless board delivery keeps its place before a follow-up', () => {
  const delivery: ChatMsg = { k: 'first-delivery', role: 'assistant', kind: 'answer', ref: 'board', text: '[done] @codex · Board' }
  const messages = [delivery, prompt('second'), { ...delivery, k: 'later-attempt' }]
  const result = projectTaskRounds(messages, [row('board', { operation_id: undefined })])
  assert.equal(result.rounds[0]?.anchor, 'first-delivery')
  assert.equal(result.hidden.has(delivery.k), true)
})

test('unknown state, open dispatch, or pending review cannot close a round', () => {
  const msgs = [prompt('op-a'), receipt('a', 'op-a')]
  for (const rows of [null, [], [row('a', { state: 'future-state' })], [row('a', { dispatch_open: true })],
    [row('a', { state: 'review' })], [row('a', { needs_followup: true })]]) {
    assert.equal(projectTaskRounds(msgs, rows).rounds[0]?.settled, false)
  }
  assert.equal(projectTaskRounds(msgs, [row('a')]).rounds[0]?.settled, true)
})

test('legacy receipts claiming the same task in different rounds remain independent', () => {
  const msgs = [receipt('a', 'op-a'), receipt('a', 'op-b')]
  const result = projectTaskRounds(msgs, [row('a', { operation_id: undefined })])
  assert.equal(result.rounds.length, 0)
  assert.equal(result.hidden.size, 0)
})

test('keeps Panda narration in its own fold without burying dispatch warnings', () => {
  const notes: ChatMsg[] = [
    { k: 'live', operation_id: 'op-a', role: 'assistant', streaming: true, text: 'Working' },
    { k: 'note', operation_id: 'op-a', role: 'assistant', kind: 'answer', text: 'Dispatched' },
    { k: 'warning', operation_id: 'op-a', role: 'assistant', kind: 'answer', text: 'Warning', warning: 'No node' },
    { k: 'stored-summary', operation_id: 'op-a', role: 'assistant', kind: 'answer', round_id: 'op-a', text: 'Summary' },
  ]
  const { rounds, hidden } = projectTaskRounds([prompt('op-a'), ...notes], [row('a', { state: 'running' })], 'op-a')
  assert.deepEqual(rounds[0]?.notes.map(note => note.k), ['live', 'note'])
  assert.deepEqual([...hidden].sort(), ['live', 'note', 'stored-summary'])
})

test('keeps saved Goal and Plan turns alongside the projected task round', () => {
  const round = { operation_id: 'op-a', round_id: 'op-a' }
  const messages: ChatMsg[] = [
    { ...round, k: 'aggregate', role: 'assistant', kind: 'answer', text: 'Task summary' },
    { ...round, k: 'goal', role: 'assistant', kind: 'answer', text: 'Goal audit', idempotency_key: 'goal:op-a' },
    { ...round, k: 'plan-user', role: 'user', text: 'Implement the proposal', idempotency_key: 'plan:user:op-a' },
    { ...round, k: 'plan-reply', role: 'assistant', kind: 'error', text: 'Implementation error', idempotency_key: 'plan:assistant:op-a' },
  ]
  const { hidden } = projectTaskRounds(messages, [row('a')])
  assert.deepEqual([...hidden], ['aggregate', 'plan-user'])
})

test('the collapsed document excerpts actual Markdown prose, not links, headings or code fences', () => {
  const report = '## Changes\n\n```sh\nnode --test\n```\n\nAdded [site/pelican-bike.html](file:///example) with **synchronized** wheels and pedals.\n\nNo browser check.'
  assert.equal(reportExcerpt(report), 'Added site/pelican-bike.html with synchronized wheels and pedals.')
  assert.equal(reportExcerpt('> - Changed both wheels and added a pause control.'), 'Changed both wheels and added a pause control.')
  assert.equal(reportExcerpt('执行报告'.repeat(8), 3), '执行报…')
  assert.equal(reportExcerpt('```txt\nlogs only\n```'), '')
  assert.equal(reportExcerpt('Wrote left.txt.\n\n[isolated] merged one file into the project tree.'), 'Wrote left.txt.')
})

test('receipt shows reported limitations without attributing them to the verifier', () => {
  const report = '已新增 [site/demo.html](site/demo.html)。\n\n背景与车轮同步。\n\n**未完成浏览器实测**：沙箱无法绑定端口。'
  assert.deepEqual(receiptCopy(report + '还有更多细节。\n\n**实际校验**\n\n- 站点检查 48/48，仅覆盖旧页。'),
    { lead: '已新增 site/demo.html。', evidence: '站点检查 48/48，仅覆盖旧页。', detail: '未完成浏览器实测：沙箱无法绑定端口。' })
  assert.deepEqual(receiptCopy('Wrote left.txt.\n\n[isolated] merged one file'), { lead: 'Wrote left.txt.', evidence: '', detail: '' })
  assert.deepEqual(receiptCopy(''), { lead: '', evidence: '', detail: '' })
})

test('a receipt digest, stale retry or pre-approval report is not a current execution summary', () => {
  const old: ChatMsg = { k: 'old', role: 'assistant', kind: 'answer', ref: 'a', text: '[failed] @codex · A',
    delivery_attempt_id: 'old', delivery: 'Previous attempt failed' }
  const current: ChatMsg = { ...old, k: 'current', text: '[done] @codex · A\n\nDigest only',
    delivery_attempt_id: 'current', delivery: undefined }
  const review: ChatMsg = { ...current, k: 'review', text: '[review] @codex · A', delivery: 'Historical approval problem' }
  const job = { id: 'a', task: row('a'), reports: [old, current, review] }
  assert.equal(currentRoundReport(job), current)
  assert.equal(roundReportText(job), '')
  assert.equal(roundReportText({ ...job, task: row('a', { delivery_excerpt: 'Added the animation.' }) }), 'Added the animation.')
  const legacy = { ...current, delivery_attempt_id: undefined, delivery: 'Changed the page', ts: Date.parse(at) / 1000 }
  assert.equal(roundReportText({ ...job, reports: [legacy] }), 'Changed the page')
  assert.equal(roundReportText({ ...job, reports: [{ ...legacy, ts: undefined }] }), '')
  assert.equal(roundReportText({ ...job, reports: [{ ...legacy, ts: legacy.ts - 1 }] }), '')
})

test('a partial failure keeps separate outcomes and does not inherit a sibling report', () => {
  const rows = [row('a', { delivery_excerpt: 'Wrote left.txt' }), row('b', { state: 'failed' })]
  const { rounds } = projectTaskRounds([prompt('op-a')], rows)
  assert.equal(rounds[0]?.settled, true)
  assert.deepEqual(rounds[0]?.jobs.map(job => [job.task?.state, roundReportText(job)]), [['done', 'Wrote left.txt'], ['failed', '']])
})

test('a goal round keeps the published objective visible above its work', () => {
  // A goal is published, not asked: the first round files the objective as the
  // user's own message, and the round's task receipt shares its round id.
  const objective: ChatMsg = { k: 'goal-user', role: 'user', text: '查一下bug', round_id: 'op-a',
    idempotency_key: 'goal:user:op-a' }
  const dispatch: ChatMsg = { k: 'goal-dispatch', role: 'assistant', kind: 'task', ref: 'a', text: '',
    operation_id: 'op-a', round_id: 'op-a', idempotency_key: 'goal:op-a' }
  const result = projectTaskRounds([objective, dispatch], [row('a')])
  assert.equal(result.hidden.has('goal-user'), false)
  assert.notEqual(result.rounds[0]?.anchor, 'goal-user')
})

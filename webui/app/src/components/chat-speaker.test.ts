// SPDX-License-Identifier: AGPL-3.0-or-later

import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { splitReportHead } from './report-head.ts'
import {
  castAgent,
  prettyAgent,
  roundStillOpen,
  speakerOf,
  stripRefId,
  trailingRunSummary,
  type CastTurn,
} from './chat-speaker.ts'

const report = (text: string) => splitReportHead(text)

describe('speakerOf', () => {
  it('puts your own turn on your side', () => {
    assert.deepEqual(speakerOf({ role: 'user', text: '帮我把 HelloWorld 跑起来' }, null), {
      who: 'user',
      agent: '',
      role: 'reply',
    })
  })

  it('draws a dispatch as the panda orchestrating', () => {
    const turn: CastTurn = { role: 'assistant', kind: 'task', text: '已派发任务：x（t1）' }
    assert.deepEqual(speakerOf(turn, report(turn.text!)), {
      who: 'panda',
      agent: '',
      role: 'plan',
    })
    assert.equal(
      speakerOf({ role: 'assistant', kind: 'task', text: '已启动多阶段计划：y（2 个阶段，p1）' }, null)
        .role,
      'plan',
    )
  })

  it('gives a named report to the employee that wrote it', () => {
    const text = '[done] @claude_code · 创建并验证 Java HelloWorld\n\njavac 通过。'
    assert.deepEqual(speakerOf({ role: 'assistant', kind: 'answer', text }, report(text)), {
      who: 'agent',
      agent: 'claude_code',
      role: 'report',
    })
  })

  it('keeps a headless report with the panda', () => {
    const text = '[cancelled] 训练脚本\n\n被取消了。'
    assert.deepEqual(speakerOf({ role: 'assistant', kind: 'answer', text }, report(text)), {
      who: 'panda',
      agent: '',
      role: 'summary',
    })
  })

  it('reads an empty streaming bubble as thinking', () => {
    assert.equal(
      speakerOf({ role: 'assistant', text: '', streaming: true }, null).role,
      'think',
    )
    assert.equal(
      speakerOf({ role: 'assistant', text: '正在看', streaming: true }, null).role,
      'reply',
    )
  })

  it('keeps a plain reply and an error with the panda', () => {
    assert.equal(speakerOf({ role: 'assistant', kind: 'answer', text: '好的' }, null).role, 'reply')
    assert.equal(speakerOf({ role: 'assistant', kind: 'error', text: '炸了' }, null).role, 'error')
  })
})

describe('prettyAgent', () => {
  it('uses the name a person would say', () => {
    assert.equal(prettyAgent('claude_code'), 'Claude Code')
    assert.equal(prettyAgent('opencode'), 'OpenCode')
    assert.equal(prettyAgent('grok_build'), 'Grok Build')
  })

  it('title-cases a hire the table has never seen', () => {
    assert.equal(prettyAgent('some_new_cli'), 'Some New Cli')
    assert.equal(prettyAgent('  '), '  ')
  })
})

describe('stripRefId', () => {
  it('drops the id the dispatch template appends', () => {
    assert.equal(stripRefId('已派发任务：写网页（t1）', 't1'), '已派发任务：写网页')
    assert.equal(stripRefId('dispatched: build (t1)', 't1'), 'dispatched: build')
  })

  it('leaves any other parenthesis alone', () => {
    // The title's own parenthesis is not the id.
    assert.equal(stripRefId('已派发任务：写网页（草稿）', 't1'), '已派发任务：写网页（草稿）')
    assert.equal(stripRefId('已派发任务：写网页（t1）'), '已派发任务：写网页（t1）')
  })
})

describe('trailingRunSummary', () => {
  const ask: CastTurn = { role: 'user', text: '帮我跑一下 HelloWorld' }
  const dispatch: CastTurn = { role: 'assistant', kind: 'task', text: '已派发任务：x（t1）', ref: 't1' }
  const done = (agent: string, title: string): CastTurn => ({
    role: 'assistant',
    kind: 'answer',
    text: `[done] @${agent} · ${title}\n\n好了。`,
  })

  it('closes a round the panda dispatched and an employee finished', () => {
    assert.deepEqual(trailingRunSummary([ask, dispatch, done('claude_code', '跑 HelloWorld')]), {
      agents: ['claude_code'],
      titles: ['跑 HelloWorld'],
      state: 'done',
      ref: 't1',
      refKind: 'task',
    })
  })

  it('collects every employee and job in a multi-stage round', () => {
    const got = trailingRunSummary([
      ask,
      { role: 'assistant', kind: 'plan', text: '已启动多阶段计划：y（2 个阶段，p1）', ref: 'p1' },
      done('claude_code', '阶段 1'),
      done('codex', '阶段 2'),
    ])
    assert.deepEqual(got?.agents, ['claude_code', 'codex'])
    assert.deepEqual(got?.titles, ['阶段 1', '阶段 2'])
    assert.equal(got?.ref, 'p1')
    assert.equal(got?.refKind, 'plan')
  })

  it('says nothing while the round is still open', () => {
    // The last word was the user's.
    assert.equal(trailingRunSummary([ask, dispatch, done('codex', 'x'), ask]), null)
    // The task has not reported yet.
    assert.equal(trailingRunSummary([ask, dispatch]), null)
    // Still running, not settled.
    assert.equal(trailingRunSummary([ask, dispatch, { role: 'assistant', text: '[running] x' }]), null)
    // A report with no dispatch above it is a single answer, not a round.
    assert.equal(trailingRunSummary([ask, done('codex', 'x')]), null)
    assert.equal(trailingRunSummary([]), null)
  })

  it('closes on a round parked for review', () => {
    const got = trailingRunSummary([
      ask,
      dispatch,
      { role: 'assistant', kind: 'answer', text: '[review] @codex · 等确认\n\n需要你点头。' },
    ])
    assert.equal(got?.state, 'review')
  })
})

describe('roundStillOpen', () => {
  it('waits for every task the round dispatched, not just the first home', () => {
    // One employee reported, two are still working: the roll-up must wait.
    assert.equal(roundStillOpen(['done', 'running']), true)
    assert.equal(roundStillOpen(['done', 'queued', 'done']), true)
    assert.equal(roundStillOpen(['done', 'dispatched']), true)
    assert.equal(roundStillOpen(['done', 'waiting_context']), true)
  })

  it('closes once every row is settled', () => {
    assert.equal(roundStillOpen(['done', 'done', 'failed']), false)
    // A parked task has stopped the work; a person has the say.
    assert.equal(roundStillOpen(['done', 'review']), false)
    assert.equal(roundStillOpen([]), false)
  })
})

describe('castAgent', () => {
  it('takes the newest exec_agent_start', () => {
    const traces = [
      { type: 'exec_agent_start', data: { agent: 'codex' } },
      { type: 'progress', data: {} },
      { type: 'exec_agent_start', data: { agent: 'claude_code' } },
    ]
    assert.equal(castAgent(traces), 'claude_code')
  })

  it('falls back to the adapter, then to nothing', () => {
    assert.equal(castAgent([{ type: 'exec_agent_start', data: { adapter: 'pi' } }]), 'pi')
    assert.equal(castAgent([{ type: 'progress', data: {} }]), '')
    assert.equal(castAgent(), '')
  })
})

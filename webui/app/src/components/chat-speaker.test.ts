// SPDX-License-Identifier: AGPL-3.0-or-later

import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { splitReportHead } from './report-head.ts'
import {
  castAgent,
  castPending,
  dispatchAgent,
  pinnedAgent,
  prettyAgent,
  roundStillOpen,
  roundTally,
  sharedTitlePrefix,
  speakerOf,
  stateTone,
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
      outcomes: ['done'],
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
    assert.deepEqual(got?.outcomes, ['done', 'done'])
    assert.equal(got?.ref, 'p1')
    assert.equal(got?.refKind, 'plan')
  })

  it('keeps every report in the round, not just the one that ended it', () => {
    // Two home, one failed: the round's state is the last report's, but the
    // batch's shape is the whole list — one failure must not speak for all.
    const got = trailingRunSummary([
      ask,
      dispatch,
      done('claude_code', 'a'),
      done('codex', 'b'),
      { role: 'assistant', kind: 'answer', text: '[failed] @deepseek_harness · c\n\n没权限。' },
    ])
    assert.equal(got?.state, 'failed')
    assert.deepEqual(got?.outcomes, ['done', 'done', 'failed'])
  })

  it('leaves an unsettled report out of the tally', () => {
    const got = trailingRunSummary([
      ask,
      dispatch,
      { role: 'assistant', kind: 'answer', text: '[running] @codex · a\n\n在跑。' },
      done('codex', 'a'),
    ])
    assert.deepEqual(got?.outcomes, ['done'])
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
    assert.equal(roundStillOpen([{ state: 'done' }, { state: 'running' }]), true)
    assert.equal(roundStillOpen([{ state: 'done' }, { state: 'queued' }, { state: 'done' }]), true)
    assert.equal(roundStillOpen([{ state: 'done' }, { state: 'dispatched' }]), true)
    assert.equal(roundStillOpen([{ state: 'done' }, { state: 'waiting_context' }]), true)
  })

  it('closes once every row is settled', () => {
    assert.equal(roundStillOpen([{ state: 'done' }, { state: 'done' }, { state: 'failed' }]), false)
    // A parked task that already produced its work has stopped; a person has
    // the say, and the roll-up is what tells them so.
    assert.equal(roundStillOpen([{ state: 'done' }, { state: 'review', approval_disposition: 'accept_work' }]), false)
    // Rows that predate the typed disposition keep the old reading: the store
    // defaults them to accept_work.
    assert.equal(roundStillOpen([{ state: 'done' }, { state: 'review' }]), false)
    assert.equal(roundStillOpen([]), false)
  })

  it('stays open while an employee is parked before it ever ran', () => {
    // 待审批 with "尚未执行——批准后将继续执行": nothing was delivered, so the
    // round cannot be summed up as finished just because a colleague reported.
    for (const disposition of ['resume_execution', 'admission_review', 'needs_changed_input']) {
      assert.equal(roundStillOpen([{ state: 'done' }, { state: 'review', approval_disposition: disposition }]), true)
    }
  })
})

describe('roundTally', () => {
  it('counts a mixed batch in one fixed order', () => {
    assert.deepEqual(roundTally(['failed', 'done', 'done']), [
      { state: 'done', n: 2 },
      { state: 'failed', n: 1 },
    ])
    assert.deepEqual(roundTally(['done']), [{ state: 'done', n: 1 }])
    assert.deepEqual(roundTally([]), [])
  })
})

describe('stateTone', () => {
  it('paints delivered work green, a decision amber and a dead job red', () => {
    assert.equal(stateTone('done'), 'ok')
    assert.equal(stateTone('review'), 'warn')
    assert.equal(stateTone('failed'), 'bad')
    assert.equal(stateTone('cancelled'), 'bad')
    assert.equal(stateTone('expired'), 'bad')
  })
})

describe('sharedTitlePrefix', () => {
  it('drops the dispatch template the jobs repeat', () => {
    assert.equal(
      sharedTitlePrefix([
        '连通性测试：调用 agent:claude_code',
        '连通性测试：调用 agent:codex',
        '连通性测试：调用 agent:deepseek_harness',
      ]),
      '连通性测试：调用 agent:',
    )
    assert.equal(sharedTitlePrefix(['Build: the API', 'Build: the UI']), 'Build:')
  })

  it('keeps a prefix that is half a phrase, or that would empty a title', () => {
    assert.equal(sharedTitlePrefix(['Fix bug in a', 'Fix bug in b']), '')
    // One title is nothing but the template: dropping it would leave a blank.
    assert.equal(sharedTitlePrefix(['连通性测试：', '连通性测试：调用 codex']), '')
    assert.equal(sharedTitlePrefix(['only one job']), '')
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

describe('dispatchAgent', () => {
  it('reads the pin when the task never reached an adapter', () => {
    // The bug: a route failure has no exec_agent_start, so the card named
    // nobody — while its two siblings in the same round named their employee.
    assert.equal(dispatchAgent({ traces: [], requires: ['agent:deepseek_harness'] }), 'deepseek_harness')
  })

  it('lets the trace outrank the pin, because a fallback is who really ran it', () => {
    const traces = [{ type: 'exec_agent_start', data: { agent: 'codex' } }]
    assert.equal(dispatchAgent({ traces, requires: ['agent:claude_code'] }), 'codex')
  })

  it('names nobody when the task neither ran nor pinned an agent', () => {
    assert.equal(dispatchAgent({ traces: [], requires: ['coding'] }), '')
    assert.equal(dispatchAgent({}), '')
  })
})

describe('pinnedAgent', () => {
  it('takes the first agent pin and ignores the rest', () => {
    assert.equal(pinnedAgent(['coding', 'agent:pi', 'agent:codex']), 'pi')
  })

  it('ignores requirements that only look like a pin', () => {
    assert.equal(pinnedAgent(['agents:pi', 'agent:', 'agent:   ']), '')
    assert.equal(pinnedAgent(), '')
  })
})

describe('castPending', () => {
  it('only claims to be picking while the task is still being routed', () => {
    assert.equal(castPending('submitted'), 'picking')
  })

  it('waits, instead of claiming to pick, once the row is in the queue', () => {
    // The chip beside this slot already reads 已入队 / 已派发: "正在挑选员工"
    // next to it said the task had no employee while its own state said it did.
    assert.equal(castPending('queued'), 'waiting')
    assert.equal(castPending('dispatched'), 'waiting')
  })

  it('says nothing where the state chip is the whole truth', () => {
    // Running, parked for a person, or settled: a placeholder would either
    // contradict the chip or invent a fact.
    for (const state of ['running', 'waiting_context', 'review', 'done', 'failed', 'cancelled', 'expired', '']) {
      assert.equal(castPending(state), '', state)
    }
  })
})

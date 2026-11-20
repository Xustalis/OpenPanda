// SPDX-License-Identifier: AGPL-3.0-or-later

import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { latestAgentText, liveStepOf, mergeLive, recentAttemptSteps } from './task-live.ts'
import { setLocale } from '../i18n/index.ts'

describe('task-live', () => {
  it('streams recent reasoning and tools without replaying a superseded attempt or result', () => {
    const rows = [
      { ev: 'thinking', attempt_id: 'old', thinking: 'prior attempt' },
      { ev: 'thinking', attempt_id: 'current', thinking: 'checking the rendered layout' },
      { ev: 'tool_use', attempt_id: 'current', name: 'Bash', input: { command: 'npm test' } },
      { ev: 'text', attempt_id: 'current', text: 'the final report' },
    ].map((data, i) => ({ key: `${i}`, ts: i, id: i, type: 'agent_event', data }))
    assert.deepEqual(recentAttemptSteps(rows, 'current').map(step => step.summary), ['checking the rendered layout', 'npm test'])
    assert.deepEqual(recentAttemptSteps(rows, ''), [])
  })

  it('summarizes the latest top-level prose from the active attempt, including live events', () => {
    const history = [{ id: 1, ts: 1, type: 'agent_event', data: '{"ev":"text","text":"saved report","attempt_id":"current"}' }]
    const activity = (id: number, data: object) => ({ id, ts: id, type: 'agent_event', data })
    const traces = [
      activity(2, { ev: 'text', text: '**New progress**', attempt_id: 'current' }),
      activity(3, { ev: 'text', text: 'child', parent: 'nested', attempt_id: 'current' }),
      activity(4, { ev: 'text', text: 'stale', attempt_id: 'old' }),
      activity(5, { ev: 'thinking', thinking: 'diagnostic', attempt_id: 'current' }),
    ]
    assert.equal(latestAgentText(mergeLive(history, traces), 'current'), '**New progress**')
    assert.equal(latestAgentText(mergeLive(history, traces), 'retry'), '')
    const legacy = activity(1, { ev: 'text', text: 'legacy report' })
    assert.equal(latestAgentText(mergeLive([], [legacy])), 'legacy report')
  })

  it('merges history and live traces by id, sorted by time', () => {
    const history = [
      { id: 2, ts: 20, type: 'progress', data: '{"note":"two"}' },
      { id: 1, ts: 10, type: 'progress', data: '{"note":"one"}' },
    ]
    const traces = [
      { id: 2, ts: 20, type: 'progress', data: { note: 'two' } },
      { id: 3, ts: 30, type: 'progress', data: { note: 'three' } },
    ]
    const got = mergeLive(history, traces)
    assert.deepEqual(got.map((e) => e.id), [1, 2, 3])
    assert.equal((got[2]!.data as { note: string }).note, 'three')
  })

  it('turns a tool call into a compact name plus its argument', () => {
    setLocale('en')
    const step = liveStepOf({
      key: 'id:1',
      id: 1,
      ts: 1,
      type: 'agent_event',
      data: { ev: 'tool_use', name: 'Bash', input: { command: 'npm test' } },
    })
    assert.equal(step?.label, 'Bash')
    assert.equal(step?.summary, 'npm test')
  })

  it('keeps a silence heartbeat as a quiet, current step', () => {
    const step = liveStepOf({
      key: 'id:1',
      id: 1,
      ts: 1,
      type: 'progress',
      data: { note: 'no output for 30s — agent still running' },
    })
    assert.equal(step?.quiet, true)
    assert.equal(step?.tone, 'dim')
    assert.ok(step?.label)
  })

  it('surfaces the review reason as the step', () => {
    setLocale('en')
    const step = liveStepOf({
      key: 'id:1',
      id: 1,
      ts: 1,
      type: 'review',
      data: { reason: 'scope drift: 5 files outside scope' },
    })
    assert.equal(step?.label, 'Review')
    assert.equal(step?.summary, 'scope drift: 5 files outside scope')
  })

  it('detail mode keeps the thinking paragraph and drops watchdog heartbeats', () => {
    setLocale('en')
    const thinking = liveStepOf(
      {
        key: 'id:1',
        id: 1,
        ts: 1,
        type: 'agent_event',
        data: { ev: 'thinking', thinking: 'first line\nsecond line\nthird line' },
      },
      true,
    )
    assert.equal(thinking?.label, 'Thinking')
    assert.match(thinking?.summary ?? '', /second line/)

    const quiet = liveStepOf(
      {
        key: 'id:2',
        id: 2,
        ts: 2,
        type: 'progress',
        data: { note: 'no output for 30s — agent still running' },
      },
      true,
    )
    assert.equal(quiet, null)
  })

  it('caps a long tool argument so one row stays one line', () => {
    const step = liveStepOf({
      key: 'id:1',
      id: 1,
      ts: 1,
      type: 'agent_event',
      data: { ev: 'tool_use', name: 'Read', input: { file_path: 'a/'.repeat(100) + 'file.ts' } },
    })
    assert.ok(step)
    assert.ok(step.summary.endsWith('…'))
    assert.ok([...step.summary].length <= 111)
  })
})

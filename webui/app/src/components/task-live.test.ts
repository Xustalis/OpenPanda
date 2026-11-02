// SPDX-License-Identifier: AGPL-3.0-or-later

import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { liveStepOf, mergeLive } from './task-live.ts'
import { setLocale } from '../i18n/index.ts'

describe('task-live', () => {
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

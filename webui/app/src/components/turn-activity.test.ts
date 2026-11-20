// SPDX-License-Identifier: AGPL-3.0-or-later
import { strict as assert } from 'node:assert'
import { test } from 'node:test'
import type { TaskEvent } from '../api/client.ts'
import { activityToolKind, taskWorkedDuration, turnActivitySteps } from './turn-activity.ts'

const event = (id: number, data: object, type = 'agent_event'): TaskEvent =>
  ({ id, ts: id, type, data: JSON.stringify(data) })

test('turn activity keeps the first slot for text snapshots and pairs tool output in place', () => {
  const events = [
    event(1, { ev: 'text', id: 'm', text: 'Checking' }),
    event(2, { ev: 'tool_use', id: 't', name: 'shell', input: { command: 'git status' } }),
    event(3, { ev: 'text', id: 'm', text: 'Checking **the diff**.' }),
    event(4, { ev: 'tool_result', tool_use_id: 't', content: 'clean' }),
    event(5, { ev: 'thinking', thinking: 'private trace remains in task diagnostics' }),
    event(6, { note: 'internal bookkeeping' }, 'progress'),
    event(7, { ev: 'text', id: 'final', text: 'Here is the **result**.' }),
  ]
  const live = turnActivitySteps(events, true)
  const final = turnActivitySteps(events, false, undefined, 'Here is the **result**.\r\n')
  assert.deepEqual(live.map(s => s.id), ['event:1', 'event:2', 'event:7'])
  assert.deepEqual(final.map(s => [s.kind, s.output, s.status]), [
    ['text', 'Checking **the diff**.', 'info'], ['tool', 'clean', 'success'],
  ])
  assert.equal(activityToolKind(final[1]!), 'command')
  assert.equal(turnActivitySteps(events, false, undefined, 'Different report').length, 3)
})

test('turn activity isolates retries, including IDs reused across attempts', () => {
  const events = [
    event(1, { ev: 'text', id: 'm', attempt_id: 'old', text: 'Previous run' }),
    event(2, { ev: 'text', id: 'm', attempt_id: 'new', text: 'Current run' }),
    event(3, { ev: 'text', id: 'legacy', text: 'Nonspecific historical row' }),
  ]
  assert.deepEqual(turnActivitySteps(events, true, 'new').map(s => s.output), ['Current run'])
  assert.equal(turnActivitySteps(events, true, 'other').length, 0)
  assert.equal(turnActivitySteps([events[2]!], true, 'new').length, 1)
})

test('worked duration retains seconds and rejects unusable clocks', () => {
  assert.equal(taskWorkedDuration('2026-01-01T00:00:00Z', '2026-01-01T00:06:11Z'), '6m 11s')
  assert.equal(taskWorkedDuration('bad', '2026-01-01T00:06:11Z'), '')
  assert.equal(taskWorkedDuration('2026-01-01T00:06:11Z', '2026-01-01T00:00:00Z'), '')
})

test('reading an image is distinct from producing a path to one', () => {
  const events = [
    event(1, { ev: 'tool_use', id: 'read', name: 'Read', input: { path: '/tmp/screen.png' } }),
    event(2, { ev: 'tool_result', tool_use_id: 'read', content: 'image data' }),
    event(3, { ev: 'tool_use', id: 'capture', name: 'Screenshot', input: {} }),
    event(4, { ev: 'tool_result', tool_use_id: 'capture', content: '/tmp/screen.png' }),
  ]
  const steps = turnActivitySteps(events, false)
  assert.deepEqual(steps.map(activityToolKind), ['image', 'tool'])
  assert.equal(steps[1]?.image?.path, '/tmp/screen.png')
})

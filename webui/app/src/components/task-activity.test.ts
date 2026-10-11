// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { fmtElapsed, isActiveState, latestActivity } from './task-activity.ts'
import { setLocale } from '../i18n/index.ts'
import type { TraceEvent } from '../api/client.ts'

const trace = (type: TraceEvent['type'], data: Record<string, unknown>, ts = 1): TraceEvent =>
  ({ id: 1, ts, type, data }) as TraceEvent

describe('task-activity', () => {
  it('renders the executing agent from exec_agent_start', () => {
    setLocale('en')
    const line = latestActivity(
      [trace('exec_agent_start', { agent: 'codex', adapter: 'codex.py' })],
      { state: 'running' },
    )
    assert.equal(line, 'Running codex')
  })

  it('shows the supervision round', () => {
    setLocale('en')
    const line = latestActivity(
      [trace('supervision_round', { round: 2, verdict_status: 'continue' })],
      { state: 'running' },
    )
    assert.equal(line, 'Supervision round 2')
  })

  it('falls back to a waiting line for queued tasks with no traces', () => {
    setLocale('en')
    assert.equal(latestActivity([], { state: 'queued' }), 'Waiting to be picked up…')
    assert.equal(latestActivity([], { state: 'done' }), '')
  })

  it('shows elapsed time for a running task with no traces', () => {
    setLocale('en')
    const now = Date.now()
    const updated = new Date(now - 3 * 60_000).toISOString()
    const line = latestActivity([], { state: 'running', updated_at: updated }, now)
    assert.equal(line, 'Running for 3m')
  })

  it('handles string data payloads on trace frames', () => {
    setLocale('en')
    const ev = trace('exec_agent_start', {})
    ;(ev as { data: unknown }).data = '{"agent":"claude"}'
    const line = latestActivity([ev], { state: 'running' })
    assert.equal(line, 'Running claude')
  })

  it('marks only moving states as active', () => {
    assert.equal(isActiveState('running'), true)
    assert.equal(isActiveState('queued'), true)
    assert.equal(isActiveState('done'), false)
    assert.equal(isActiveState('review'), false)
    assert.equal(isActiveState(undefined), false)
  })

  it('formats elapsed durations compactly', () => {
    setLocale('en')
    const now = Date.now()
    assert.equal(fmtElapsed(new Date(now - 30_000).toISOString(), now), '30s')
    assert.equal(fmtElapsed(new Date(now - 5 * 60_000).toISOString(), now), '5m')
    assert.equal(fmtElapsed(new Date(now - 2 * 3600_000).toISOString(), now), '2h')
    assert.equal(fmtElapsed('', now), '')
  })

  // — Agent-activity stream: the board narrates what the run is doing.
  it('narrates a tool call with its primary argument', () => {
    setLocale('en')
    const line = latestActivity(
      [trace('agent_event', { ev: 'tool_use', name: 'bash', input: { command: 'ls -la' } })],
      { state: 'running' },
    )
    assert.equal(line, 'bash · ls -la')
  })

  it('names the tool when the input has no readable argument', () => {
    setLocale('en')
    const line = latestActivity(
      [trace('agent_event', { ev: 'tool_use', name: 'dsh', input: { argc: 3 } })],
      { state: 'running' },
    )
    assert.equal(line, 'dsh · 3 args')
  })

  it('shows thinking and streamed text', () => {
    setLocale('en')
    assert.equal(
      latestActivity([trace('agent_event', { ev: 'thinking' })], { state: 'running' }),
      'Thinking…',
    )
    const line = latestActivity(
      [trace('agent_event', { ev: 'text', text: 'Writing snake.py with curses\nmore detail' })],
      { state: 'running' },
    )
    assert.equal(line, 'Writing snake.py with curses')
  })

  it('surfaces the adapter silence note verbatim (clipped)', () => {
    setLocale('en')
    const line = latestActivity(
      [trace('progress', { note: 'no output for 56s — agent still running' })],
      { state: 'running' },
    )
    assert.equal(line, 'no output for 56s — agent still running')
  })

  it('sums all token fields on usage events', () => {
    setLocale('en')
    const line = latestActivity(
      [
        trace('agent_usage', {
          input_tokens: 12000,
          output_tokens: 450,
          cache_read_tokens: 200,
        }),
      ],
      { state: 'running' },
    )
    assert.equal(line, '12.7k tokens used')
  })

  it('reads the last_event stamp like a stream frame', () => {
    setLocale('en')
    const lastEvent = {
      id: 9,
      ts: 1,
      type: 'agent_event',
      data: { ev: 'tool_use', name: 'write', input: { file_path: 'snake.py' } },
    } as unknown as TraceEvent
    const line = latestActivity([lastEvent], { state: 'running' })
    assert.equal(line, 'write · snake.py')
  })
})

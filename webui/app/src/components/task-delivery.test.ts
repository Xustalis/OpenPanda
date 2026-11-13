// SPDX-License-Identifier: AGPL-3.0-or-later

import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { deliveryFacts, deliveryMatchesTask, deliveryPayload, deliveryPreview, deliverySteps, deliveryText } from './task-delivery.ts'
import { setLocale } from '../i18n/index.ts'

describe('task delivery', () => {
  it('preserves changes, verification, and open items instead of the last paragraph alone', () => {
    const report = '## Changes\nFixed it.\n\n## Verification\nTests passed.\n\n## Remaining\nDeploy manually.'
    assert.equal(deliveryText(JSON.stringify({ result: report })), report)
    assert.equal(deliveryText(JSON.stringify({ stdout: report })), report)
    assert.equal(deliveryPreview(report).text, report)
  })

  it('handles absent, malformed, or non-object output without inventing a report', () => {
    for (const value of [undefined, '', '{broken', 'null', '[]', 'true', '"ok"']) {
      assert.equal(deliveryText(value), '')
      assert.deepEqual(deliveryPayload(value), {})
    }
    assert.equal(deliveryText('{"result":null,"answer":"","stderr":"failed"}'), 'failed')
  })

  it('bounds a long first screen on Unicode characters and keeps the original available', () => {
    const report = '修复✅'.repeat(3000)
    const preview = deliveryPreview(report)
    assert.ok(preview.more)
    assert.equal([...preview.text].length, 6003)
    assert.ok(!preview.text.includes('\ufffd'))
    assert.equal(deliveryText(JSON.stringify({ stdout: report })), report)
  })

  it('never turns task completion or an agent claim into verification', () => {
    const facts = deliveryFacts({ state: 'done', result: '{"verified":true,"stdout":"all good"}' })
    assert.equal(facts.verification, 'unknown')
    assert.equal(facts.remaining, 'unreported')
    assert.deepEqual(facts.files, [])
    assert.equal(deliveryFacts({ state: 'done', verification: 'unrecognized' }).verification, 'unknown')
  })

  it('shows partial checks, failed tasks, reviews and explicit follow-up honestly', () => {
    assert.equal(deliveryFacts({ state: 'done', verification: 'partial' }).remaining, 'followup')
    assert.equal(deliveryFacts({ state: 'failed', verification: 'verified' }).remaining, 'failed')
    assert.equal(deliveryFacts({ state: 'review' }).remaining, 'review')
    const facts = deliveryFacts({ state: 'done', result: JSON.stringify({
      needs_followup: true, followup: 'Deploy manually.', verification: 'partial',
      files_changed: ['src/a.ts', 'src/a.ts', null, false, ''],
    }) })
    assert.equal(facts.followup, 'Deploy manually.')
    assert.equal(facts.remaining, 'followup')
    assert.deepEqual(facts.files, ['src/a.ts'])
  })

  it('previews actual tool steps, not reasoning, usage, or silence heartbeats', () => {
    setLocale('en')
    const steps = deliverySteps([
      { id: 1, ts: 1, type: 'agent_event', data: '{"ev":"thinking","thinking":"private-ish provider text"}' },
      { id: 2, ts: 2, type: 'agent_event', data: '{"ev":"tool_use","name":"Bash","input":{"command":"npm test"}}' },
      { id: 3, ts: 3, type: 'agent_event', data: '{"ev":"tool_use","name":"Bash","input":{"command":"npm test"}}' },
      { id: 4, ts: 4, type: 'agent_usage', data: '{"tokens":100}' },
      { id: 5, ts: 5, type: 'progress', data: '{"note":"no output for 30s — agent still running"}' },
    ])
    assert.equal(steps.length, 1)
    assert.equal(steps[0]?.label, 'Bash')
    assert.equal(steps[0]?.summary, 'npm test')
  })
  it('does not replace an old attempt report with a retry result', () => {
    const task = { attempt_id: 'new-attempt', state: 'done', updated_at: '2026-11-13T12:00:10Z' }
    assert.equal(deliveryMatchesTask(task, 'old-attempt', 1794556800, 'done'), false)
    assert.equal(deliveryMatchesTask(task, 'new-attempt'), true)
    const before = Date.parse('2026-11-13T12:00:00Z') / 1000
    assert.equal(deliveryMatchesTask(task, undefined, before, 'done'), false)
    assert.equal(deliveryMatchesTask(task, undefined, before + 10, 'done'), true)
    assert.equal(deliveryMatchesTask(task, undefined, before + 10, 'failed'), false)
  })

})

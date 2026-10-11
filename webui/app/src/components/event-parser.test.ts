// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { blockDepth, extractThought, formatTaskEvent, toolArgSummary, type AgentBlock } from './event-parser.ts'
import { buildRows } from './event-rows.ts'
import { setLocale } from '../i18n/index.ts'

describe('event-parser', () => {
  it('extracts thought from json string or object', () => {
    assert.equal(extractThought('{"thought":"thinking about step 1"}'), 'thinking about step 1')
    assert.equal(extractThought({ thought: 'thinking about step 2' }), 'thinking about step 2')
    assert.equal(extractThought('raw thought text'), 'raw thought text')
    assert.equal(extractThought(null), '')
  })

  it('formats reasoning event as human-readable model thought with i18n support', () => {
    setLocale('en')
    const formattedEn = formatTaskEvent('reasoning', JSON.stringify({ thought: 'First analyze dependencies, then run tests.' }))
    assert.equal(formattedEn.type, 'reasoning')
    assert.equal(formattedEn.label, 'Model Reasoning')
    assert.equal(formattedEn.badgeClass, 'accent')
    assert.equal(formattedEn.thought, 'First analyze dependencies, then run tests.')

    setLocale('zh-CN')
    const formattedZh = formatTaskEvent('reasoning', JSON.stringify({ thought: 'First analyze dependencies, then run tests.' }))
    assert.equal(formattedZh.label, '模型思考过程')

    setLocale('ja')
    const formattedJa = formatTaskEvent('reasoning', JSON.stringify({ thought: 'First analyze dependencies, then run tests.' }))
    assert.equal(formattedJa.label, 'モデル思考プロセス')
  })

  it('formats classify_result event into clear tags and note', () => {
    setLocale('en')
    const formatted = formatTaskEvent('classify_result', JSON.stringify({ kind: 'task', note: 'execute build' }))
    assert.equal(formatted.label, 'Intent Classification')
    assert.equal(formatted.summary, 'execute build')
    assert.deepEqual(formatted.tags, [{ key: 'Type', value: 'task' }])

    setLocale('zh-CN')
    const formattedZh = formatTaskEvent('classify_result', JSON.stringify({ kind: 'task', note: 'execute build' }))
    assert.equal(formattedZh.label, '意图识别')
    assert.deepEqual(formattedZh.tags, [{ key: '类型', value: 'task' }])
  })

  it('drops noise keys like candidates and score_breakdown in fallback events', () => {
    const formatted = formatTaskEvent('custom_event', JSON.stringify({
      candidates: ['node1', 'node2'],
      score_breakdown: [0.9, 0.8],
      target: 'node1',
    }))
    assert.deepEqual(formatted.tags, [{ key: 'target', value: 'node1' }])
  })

  it('parses agent_event blocks into typed transcript rows', () => {
    setLocale('en')
    const toolUse = formatTaskEvent('agent_event', JSON.stringify({
      ev: 'tool_use', id: 'tu_1', name: 'Bash',
      input: { command: 'ls -la', timeout: 30 },
    }))
    assert.equal(toolUse.badgeClass, 'info')
    assert.equal(toolUse.agent?.ev, 'tool_use')
    assert.equal(toolUse.agent?.name, 'Bash')
    assert.equal(toolUse.agent?.id, 'tu_1')
    assert.equal(toolArgSummary(toolUse.agent?.input), 'ls -la')

    const errResult = formatTaskEvent('agent_event', JSON.stringify({
      ev: 'tool_result', tool_use_id: 'tu_1', is_error: true, content: 'boom',
    }))
    assert.equal(errResult.badgeClass, 'danger')
    assert.equal(errResult.agent?.toolUseId, 'tu_1')
    assert.equal(errResult.agent?.isError, true)

    const thinking = formatTaskEvent('agent_event', JSON.stringify({
      ev: 'thinking', thinking: 'weighing options',
    }))
    assert.equal(thinking.agent?.thinking, 'weighing options')

    const text = formatTaskEvent('agent_event', JSON.stringify({
      ev: 'text', text: 'done',
    }))
    assert.equal(text.agent?.text, 'done')
  })

  it('nests sub-agent blocks under their parent tool_use', () => {
    const depths = new Map<string, number>()
    const task: AgentBlock = { ev: 'tool_use', id: 'task_1', name: 'Task' }
    assert.equal(blockDepth(task, depths), 0)
    const child: AgentBlock = { ev: 'text', parent: 'task_1', text: 'inner' }
    assert.equal(blockDepth(child, depths), 1)
    const grandchildTool: AgentBlock = { ev: 'tool_use', id: 'tu_inner', parent: 'task_1', name: 'Read' }
    assert.equal(blockDepth(grandchildTool, depths), 1)
    const leaf: AgentBlock = { ev: 'text', parent: 'tu_inner', text: 'deep' }
    assert.equal(blockDepth(leaf, depths), 2)
    // A parent outside the window still nests once.
    const orphan: AgentBlock = { ev: 'text', parent: 'gone', text: 'x' }
    assert.equal(blockDepth(orphan, depths), 1)
    // Depth is capped so a deep chain cannot eat the row.
    const depths2 = new Map<string, number>([['a', 10]])
    assert.equal(blockDepth({ ev: 'text', parent: 'a', text: 'x' }, depths2), 6)
  })
})


describe('lifecycle & result event formatting', () => {
  it('marks dispatch plumbing events for pipeline grouping', () => {
    setLocale('en')
    const submit = formatTaskEvent('submit', JSON.stringify({ owner: 'mac-1', parent: '' }))
    assert.equal(submit.lifecycle?.step, 'submitted')
    assert.equal(submit.lifecycle?.node, 'mac-1')

    const accept = formatTaskEvent('accept', JSON.stringify({ owner: 'gpu-box' }))
    assert.equal(accept.lifecycle?.step, 'accepted')
    assert.equal(accept.lifecycle?.node, 'gpu-box')

    const decline = formatTaskEvent('decline', JSON.stringify({ reason: 'busy' }))
    assert.equal(decline.lifecycle?.failed, true)
  })

  it('renders a result event as a result card, not tags', () => {
    setLocale('en')
    const r = formatTaskEvent('result', JSON.stringify({
      ok: true, exit_code: 0, stdout: 'wrote hello.py\n\nAll done.',
      files_changed: ['hello.py'],
    }))
    assert.equal(r.label, 'Result')
    assert.equal(r.badgeClass, 'accent')
    assert.equal(r.result?.ok, true)
    assert.equal(r.result?.stdout, 'wrote hello.py\n\nAll done.')
    assert.equal(r.summary, 'wrote hello.py')
    // never leaks the payload as tags
    assert.equal(r.tags.length, 0)
  })

  it('reads the wrapped result payload used by reconciliation', () => {
    setLocale('en')
    const r = formatTaskEvent('result', JSON.stringify({
      reconciled: 'running→done',
      result: { ok: true, exit_code: 0, stdout: 'done text' },
    }))
    assert.equal(r.result?.ok, true)
    assert.equal(r.result?.stdout, 'done text')
  })

  it('demotes session_summary to a quiet line', () => {
    setLocale('en')
    const f = formatTaskEvent('session_summary', JSON.stringify({ session_id: 'abc', state: 'done' }))
    assert.equal(f.quiet, true)
    assert.equal(f.tags.length, 0)
  })

  it('maps supervise verdicts with their reason as the summary', () => {
    setLocale('en')
    const f = formatTaskEvent('supervise', JSON.stringify({
      round: 2, status: 'done', reason: 'all acceptance criteria met', followup: '',
    }))
    assert.equal(f.label, 'Supervision Verdict')
    assert.equal(f.badgeClass, 'accent')
    assert.equal(f.summary, 'all acceptance criteria met')
    assert.deepEqual(f.tags, [
      { key: 'Round', value: '2' },
      { key: 'Status', value: 'done' },
    ])
  })

  it('caps unknown-event tags at three short hints', () => {
    setLocale('en')
    const f = formatTaskEvent('custom_unknown', JSON.stringify({
      a: '1', b: '2', c: '3', d: '4', e: '5',
    }))
    assert.equal(f.tags.length, 3)
  })
})

describe('buildRows pipeline grouping', () => {
  it('folds consecutive lifecycle events into one pipeline row', () => {
    const events = [
      { ts: 1, type: 'submit', data: '{"owner":"mac"}' },
      { ts: 2, type: 'queue', data: 'null' },
      { ts: 3, type: 'delegate', data: '{"target":"gpu"}' },
      { ts: 4, type: 'accept', data: '{"owner":"gpu"}' },
      { ts: 5, type: 'exec_agent_start', data: '{"agent":"codex"}' },
      { ts: 6, type: 'session_summary', data: '{"session_id":"s1"}' },
    ]
    const rows = buildRows(events as never)
    assert.equal(rows.length, 3)
    const pipe = rows[0]
    assert.equal(pipe?.kind, 'pipeline')
    if (pipe?.kind === 'pipeline') {
      assert.equal(pipe.steps.length, 4)
      assert.equal(pipe.steps[0]?.node, 'mac')
      assert.equal(pipe.steps[2]?.node, 'gpu')
    }
    const ev = rows[1]
    assert.equal(ev?.kind, 'event')
    if (ev?.kind === 'event') assert.equal(ev.event.type, 'exec_agent_start')
  })

  it('splits a pipeline around a non-lifecycle event', () => {
    const events = [
      { ts: 1, type: 'submit', data: '{}' },
      { ts: 2, type: 'exec_agent_start', data: '{}' },
      { ts: 3, type: 'delegate', data: '{}' },
    ]
    const rows = buildRows(events as never)
    assert.deepEqual(rows.map((r) => r.kind), ['pipeline', 'event', 'pipeline'])
  })

  it('folds a run of silence heartbeats into one counted quiet row', () => {
    const events = [
      { ts: 1, type: 'agent_event', data: '{"ev":"tool_use","name":"dsh"}' },
      { ts: 2, type: 'progress', data: '{"note":"no output for 56s — agent still running"}' },
      { ts: 3, type: 'progress', data: '{"note":"no output for 1m41s — agent still running"}' },
      { ts: 4, type: 'progress', data: '{"note":"no output for 2m26s — agent still running"}' },
      { ts: 5, type: 'agent_event', data: '{"ev":"text","text":"awake"}' },
    ]
    const rows = buildRows(events as never)
    assert.equal(rows.length, 3)
    const quiet = rows[1]
    assert.equal(quiet?.kind, 'event')
    if (quiet?.kind === 'event') {
      assert.equal(quiet.count, 3)
      // The folded row shows the NEWEST silence note, not the first.
      assert.match(quiet.formatted.label, /2m26s/)
      assert.equal(quiet.formatted.quiet, true)
    }
  })

  it('keeps non-silence progress notes as normal rows', () => {
    const events = [
      { ts: 1, type: 'progress', data: '{"note":"bash: Inspect working directory"}' },
      { ts: 2, type: 'progress', data: '{"note":"write: plans/snake.py"}' },
    ]
    const rows = buildRows(events as never)
    assert.equal(rows.length, 2)
    const r = rows[0]
    if (r?.kind === 'event') assert.ok(!r.formatted.quiet)
  })
})

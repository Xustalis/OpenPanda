import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { blockDepth, extractThought, formatTaskEvent, toolArgSummary, type AgentBlock } from './event-parser.ts'
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


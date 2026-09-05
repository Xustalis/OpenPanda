import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { extractThought, formatTaskEvent } from './event-parser.ts'

describe('event-parser', () => {
  it('extracts thought from json string or object', () => {
    assert.equal(extractThought('{"thought":"thinking about step 1"}'), 'thinking about step 1')
    assert.equal(extractThought({ thought: 'thinking about step 2' }), 'thinking about step 2')
    assert.equal(extractThought('raw thought text'), 'raw thought text')
    assert.equal(extractThought(null), '')
  })

  it('formats reasoning event as human-readable model thought', () => {
    const formatted = formatTaskEvent('reasoning', JSON.stringify({ thought: 'First analyze dependencies, then run tests.' }))
    assert.equal(formatted.type, 'reasoning')
    assert.equal(formatted.label, '模型思考过程')
    assert.equal(formatted.badgeClass, 'accent')
    assert.equal(formatted.thought, 'First analyze dependencies, then run tests.')
  })

  it('formats classify_result event into clear tags and note', () => {
    const formatted = formatTaskEvent('classify_result', JSON.stringify({ kind: 'task', note: 'execute build' }))
    assert.equal(formatted.label, '意图识别')
    assert.equal(formatted.summary, 'execute build')
    assert.deepEqual(formatted.tags, [{ key: '类型', value: 'task' }])
  })

  it('drops noise keys like candidates and score_breakdown in fallback events', () => {
    const formatted = formatTaskEvent('custom_event', JSON.stringify({
      candidates: ['node1', 'node2'],
      score_breakdown: [0.9, 0.8],
      target: 'node1',
    }))
    assert.deepEqual(formatted.tags, [{ key: 'target', value: 'node1' }])
  })
})

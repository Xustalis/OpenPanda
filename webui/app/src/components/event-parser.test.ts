import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { extractThought, formatTaskEvent } from './event-parser.ts'
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
})


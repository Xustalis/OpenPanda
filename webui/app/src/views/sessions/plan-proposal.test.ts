// SPDX-License-Identifier: AGPL-3.0-or-later

// The plan dock's summary line. It is the only part of a proposal a reader
// sees without opening the dialog, so it has to be the plan's first real
// sentence: headings would only repeat the dock's own title, and code, tables
// and rules would eat the line without saying what the plan does.

import assert from 'node:assert/strict'
import { test } from 'node:test'
import { proposalSummary } from './plan-proposal.ts'

test('the summary starts at the first paragraph, not at the heading', () => {
  const plan = [
    '# 实施计划',
    '',
    '把登录页的表单拆成 **两个** 组件，先做校验再做提交。',
    '',
    '## 步骤',
    '- 抽取校验',
  ].join('\n')
  assert.equal(proposalSummary(plan), '把登录页的表单拆成 两个 组件，先做校验再做提交。')
})

test('block prefixes and inline marks are flattened', () => {
  assert.equal(proposalSummary('> 引用的一段计划说明。'), '引用的一段计划说明。')
  assert.equal(proposalSummary('- 第一步：抽取 `verify()` 并加测试。'), '第一步：抽取 verify() 并加测试。')
  assert.equal(proposalSummary('1. 先写测试，再改实现。'), '先写测试，再改实现。')
  assert.equal(proposalSummary('见 [设计文档](https://example.com/doc) 第三节。'), '见 设计文档 第三节。')
})

test('fenced code, tables and rules never reach the line', () => {
  assert.equal(proposalSummary('```\nconst a = 1\n```\n\n正文第一段。'), '正文第一段。')
  assert.equal(proposalSummary('| 步骤 | 说明 |\n| --- | --- |\n| 1 | 抽取 |'), '')
  assert.equal(proposalSummary('---\n\n正文第一段。'), '正文第一段。')
})

test('a title-only proposal has no summary to show', () => {
  assert.equal(proposalSummary('# 实施计划\n\n## 步骤'), '')
  assert.equal(proposalSummary('   \n\n  '), '')
})

test('long text is cut at a word boundary, CJK text at the limit', () => {
  const words = proposalSummary('word '.repeat(40), 20)
  assert.ok(words.endsWith('…'))
  assert.ok(words.length <= 21)
  assert.ok(!words.includes('  '))

  const cjk = proposalSummary('计'.repeat(200))
  assert.equal(cjk.length, 141)
  assert.ok(cjk.endsWith('…'))
})

test('CRLF input reads the same as LF', () => {
  assert.equal(proposalSummary('# 标题\r\n\r\n第一段。\r\n\r\n第二段。'), '第一段。')
})

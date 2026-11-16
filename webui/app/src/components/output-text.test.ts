// SPDX-License-Identifier: AGPL-3.0-or-later

import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { readableOutput } from './output-text.ts'

// A zh-CN Windows shell writes GBK to a pipe; a CLI that decodes it as UTF-8
// turns every Chinese character into U+FFFD. These are the shapes that came
// back from `ver; wmic os get Version,Caption` through one.
const MOJIBAKE = [
  'ver : \uFFFD\uFFFD\uFFFD\uFFFDver\uFFFD\uFFFD\uFFFD\uFFFD\uFFFD"\uFFFD\uFFFD',
  'cmdlet\uFFFD\uFFFD\uFFFD\uFFFD\uFFFD\uFFFD\uFFFD\uFFFDu\uFFFD|\uFFFD\uFFFD\uFFFDA\uFFFD',
  '\uFFFD\uFFFD\uFFFD\uFFFDP\uFFFD\uFFFD',
].join('\n')

describe('readable output', () => {
  it('leaves text that never lost a byte exactly as it was', () => {
    const clean = 'npm test\n\n  12 passing (3s)\n'
    assert.deepEqual(readableOutput(clean), { text: clean, dropped: 0 })
    assert.deepEqual(readableOutput(''), { text: '', dropped: 0 })
  })

  it('keeps the lines a reader can act on and counts the ones it dropped', () => {
    const out = readableOutput([
      MOJIBAKE,
      '+ ver; wmic os get Version,Caption /value; fsutil fsinfo ntfsinfo C: | ...',
      '    + CategoryInfo          : ObjectNotFound: (ver:String) [], CommandNotFoundException',
    ].join('\n'))
    assert.equal(out.dropped, 3)
    assert.equal(out.text, [
      '+ ver; wmic os get Version,Caption /value; fsutil fsinfo ntfsinfo C: | ...',
      '    + CategoryInfo          : ObjectNotFound: (ver:String) [], CommandNotFoundException',
    ].join('\n'))
  })

  it('drops every line when the whole result is undecodable', () => {
    const out = readableOutput(MOJIBAKE)
    assert.equal(out.text, '')
    assert.equal(out.dropped, 3)
  })

  it('collapses a run inside a readable line instead of dropping the line', () => {
    const out = readableOutput('error: 无法读取 \uFFFD\uFFFD\uFFFD\uFFFD\uFFFD (os error 2)')
    assert.equal(out.text, 'error: 无法读取 \uFFFD (os error 2)')
    assert.equal(out.dropped, 0)
  })

  it('keeps a mostly-Chinese line that lost a single character', () => {
    const line = '构建失败：模块 commander \uFFFD 未找到'
    assert.deepEqual(readableOutput(line), { text: line, dropped: 0 })
  })

  it('reads CRLF output without counting the carriage return as a character', () => {
    const out = readableOutput('\uFFFD\uFFFD\uFFFD\uFFFD\r\nok: 3 files\r\n')
    assert.equal(out.text, 'ok: 3 files')
    assert.equal(out.dropped, 1)
  })
})

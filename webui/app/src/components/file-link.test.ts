// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it } from 'node:test'
import assert from 'node:assert/strict'

import { parseFileRef } from './file-link.ts'

describe('parseFileRef', () => {
  it('reads a relative path with a line', () => {
    assert.deepEqual(parseFileRef('internal/bus/route.go:42'), {
      path: 'internal/bus/route.go',
      line: 42,
    })
  })

  it('reads a bare file name with a known extension', () => {
    assert.deepEqual(parseFileRef('config.yaml'), { path: 'config.yaml', line: 0 })
    assert.deepEqual(parseFileRef('main.go:7'), { path: 'main.go', line: 7 })
  })

  it('accepts a path that is only a directory chain', () => {
    assert.deepEqual(parseFileRef('webui/app/src'), { path: 'webui/app/src', line: 0 })
  })

  it('keeps a Windows drive colon out of the line suffix', () => {
    assert.deepEqual(parseFileRef('C:\\src\\a.go'), { path: 'C:\\src\\a.go', line: 0 })
  })

  it('rejects things that only look like files', () => {
    for (const text of [
      'fmt.Println', // a qualified name, not a path
      '1.5', // a number
      'v2.1.3', // a version
      'npm install', // prose with a space
      'https://example.com/a', // a URL is already a link
      'x', // nothing to go on
      '', // empty
      'main.go:abc', // the tail is not a line number, and the rest is no path
      'a + b', // expression
      'x[0]', // indexing
      '{}', // literal
      '...', // ellipsis
    ]) {
      assert.equal(parseFileRef(text), null, text)
    }
  })

  it('drops a line suffix that is not a positive number', () => {
    // ":0" leaves a perfectly good path; only the focus line is discarded.
    assert.deepEqual(parseFileRef('a/b.go:0'), { path: 'a/b.go', line: 0 })
  })

  it('caps absurd line numbers', () => {
    assert.deepEqual(parseFileRef('a/b.go:99999999999'), { path: 'a/b.go', line: 0 })
  })
})

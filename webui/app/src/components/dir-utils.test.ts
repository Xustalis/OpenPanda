import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { suggestProjectName } from './dir-utils.ts'

describe('suggestProjectName', () => {
  it('handles empty path', () => {
    assert.equal(suggestProjectName(''), '')
  })

  it('extracts folder name from Unix path', () => {
    assert.equal(suggestProjectName('/Users/alice/projects/my-panda'), 'my-panda')
    assert.equal(suggestProjectName('/Users/alice/projects/my-panda/'), 'my-panda')
  })

  it('extracts folder name from Windows path', () => {
    assert.equal(suggestProjectName('C:\\Users\\alice\\projects\\open_panda'), 'open_panda')
    assert.equal(suggestProjectName('C:\\Users\\alice\\projects\\open_panda\\'), 'open_panda')
  })

  it('handles Chinese directory names', () => {
    assert.equal(suggestProjectName('/Users/alice/我的项目'), '我的项目')
  })

  it('sanitizes illegal characters in project names', () => {
    assert.equal(suggestProjectName('/Users/alice/foo@bar#123'), 'foo-bar-123')
  })
})

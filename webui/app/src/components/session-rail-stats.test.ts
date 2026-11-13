// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import {
  contextShare,
  firstChangedLine,
  patchFileStats,
  patchForFile,
  porcelainParts,
  porcelainPath,
} from './session-rail-stats.ts'

describe('patchFileStats', () => {
  it('counts additions and deletions per file', () => {
    const patch = [
      'diff --git a/src/a.ts b/src/a.ts',
      'index 111..222 100644',
      '--- a/src/a.ts',
      '+++ b/src/a.ts',
      '@@ -1,3 +1,4 @@',
      ' const keep = 1',
      '+const added = 2',
      '-const gone = 3',
      'diff --git a/src/b.ts b/src/b.ts',
      '--- a/src/b.ts',
      '+++ b/src/b.ts',
      '@@ -1 +1 @@',
      '-old',
      '+new',
      '',
    ].join('\n')
    const stats = patchFileStats(patch)
    assert.deepEqual(stats.get('src/a.ts'), { insertions: 1, deletions: 1 })
    assert.deepEqual(stats.get('src/b.ts'), { insertions: 1, deletions: 1 })
  })

  it('counts a hunk line that starts with ++ instead of reading it as a header', () => {
    const patch = [
      'diff --git a/x.md b/x.md',
      '--- a/x.md',
      '+++ b/x.md',
      '@@ -0,0 +1,2 @@',
      '+++ not a header',
      '+plain',
      '',
    ].join('\n')
    assert.deepEqual(patchFileStats(patch).get('x.md'), { insertions: 2, deletions: 0 })
  })

  it('names a deleted file from its --- side', () => {
    const patch = [
      'diff --git a/gone.ts b/gone.ts',
      'deleted file mode 100644',
      '--- a/gone.ts',
      '+++ /dev/null',
      '@@ -1,2 +0,0 @@',
      '-a',
      '-b',
      '',
    ].join('\n')
    assert.deepEqual(patchFileStats(patch).get('gone.ts'), { insertions: 0, deletions: 2 })
  })

  it('names an untracked file from its +++ side', () => {
    const patch = [
      'diff --git a/new.ts b/new.ts',
      'new file mode 100644',
      '--- /dev/null',
      '+++ b/new.ts',
      '@@ -0,0 +1 @@',
      '+hello',
      '',
    ].join('\n')
    assert.deepEqual(patchFileStats(patch).get('new.ts'), { insertions: 1, deletions: 0 })
  })

  it('unquotes a path with a space', () => {
    const patch = [
      'diff --git "a/my file.ts" "b/my file.ts"',
      '--- "a/my file.ts"',
      '+++ "b/my file.ts"',
      '@@ -1 +1 @@',
      '-a',
      '+b',
      '',
    ].join('\n')
    assert.deepEqual(patchFileStats(patch).get('my file.ts'), { insertions: 1, deletions: 1 })
  })

  it('drops the block the cap cut through', () => {
    const patch = [
      'diff --git a/first.ts b/first.ts',
      '--- a/first.ts',
      '+++ b/first.ts',
      '@@ -1 +1 @@',
      '-a',
      '+b',
      'diff --git a/last.ts b/last.ts',
      '--- a/last.ts',
      '+++ b/last.ts',
      '@@ -1 +1 @@',
      '-a',
      '+b',
      '… (diff truncated)',
      '',
    ].join('\n')
    const stats = patchFileStats(patch)
    assert.deepEqual(stats.get('first.ts'), { insertions: 1, deletions: 1 })
    assert.equal(stats.has('last.ts'), false)
  })

  it('returns nothing for an empty patch', () => {
    assert.equal(patchFileStats('').size, 0)
  })
})

describe('patchForFile', () => {
  const patch = [
    'diff --git a/src/a.ts b/src/a.ts',
    '--- a/src/a.ts',
    '+++ b/src/a.ts',
    '@@ -1,3 +1,4 @@',
    ' const keep = 1',
    '+const added = 2',
    '-const gone = 3',
    'diff --git a/src/b.ts b/src/b.ts',
    '--- a/src/b.ts',
    '+++ b/src/b.ts',
    '@@ -7 +7 @@',
    '-old',
    '+new',
    '',
  ].join('\n')

  it('returns only the block for the requested file', () => {
    const got = patchForFile(patch, 'src/b.ts')
    assert.equal(got?.truncated, false)
    assert.match(got!.patch, /^diff --git a\/src\/b\.ts/)
    assert.doesNotMatch(got!.patch, /src\/a\.ts/)
    assert.equal(firstChangedLine(got!.patch), 7)
  })

  it('names a renamed file by its new side', () => {
    const renamed = [
      'diff --git a/old name.ts b/new name.ts',
      'similarity index 50%',
      'rename from old name.ts',
      'rename to new name.ts',
      '--- a/old name.ts\t',
      '+++ b/new name.ts\t',
      '@@ -1 +1 @@',
      '-a',
      '+b',
      '',
    ].join('\n')
    const got = patchForFile(renamed, 'new name.ts')
    assert.ok(got && !got.truncated)
    assert.equal(got!.patch, renamed)
  })

  it('has no block for a path the patch does not mention', () => {
    assert.equal(patchForFile(patch, 'src/nope.ts'), null)
  })

  it('does not hand back a block the cap cut through', () => {
    const cut = [
      'diff --git a/first.ts b/first.ts',
      '--- a/first.ts',
      '+++ b/first.ts',
      '@@ -1 +1 @@',
      '-a',
      '+b',
      'diff --git a/last.ts b/last.ts',
      '--- a/last.ts',
      '+++ b/last.ts',
      '@@ -1 +1 @@',
      '-a',
      '+b',
      '… (diff truncated)',
      '',
    ].join('\n')
    assert.equal(patchForFile(cut, 'first.ts')?.truncated, false)
    assert.deepEqual(patchForFile(cut, 'last.ts'), { patch: '', truncated: true })
  })
})

describe('porcelainPath', () => {
  it('takes the destination side of a rename', () => {
    assert.equal(porcelainPath('"b space.txt" -> "c space.txt"'), 'c space.txt')
  })

  it('keeps both sides of a rename', () => {
    assert.deepEqual(porcelainParts('"b space.txt" -> "c space.txt"'), {
      from: 'b space.txt',
      to: 'c space.txt',
    })
    assert.deepEqual(porcelainParts('src/a.ts'), { from: 'src/a.ts', to: 'src/a.ts' })
  })

  it('unquotes a path with a space', () => {
    assert.equal(porcelainPath('"my file.ts"'), 'my file.ts')
  })

  it('leaves a bare path alone, including a leading a/ or b/', () => {
    assert.equal(porcelainPath('src/a.ts'), 'src/a.ts')
    assert.equal(porcelainPath('a/file.ts'), 'a/file.ts')
  })
})

describe('contextShare', () => {
  it('reports whole percent', () => {
    assert.equal(contextShare(72_400, 200_000), 36)
  })

  it('clamps past a window the provider overran', () => {
    assert.equal(contextShare(300_000, 200_000), 100)
  })

  it('has no answer without a window', () => {
    assert.equal(contextShare(1_000, 0), null)
    assert.equal(contextShare(0, 200_000), null)
    assert.equal(contextShare(Number.NaN, 200_000), null)
  })
})

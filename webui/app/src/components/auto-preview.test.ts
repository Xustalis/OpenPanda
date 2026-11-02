// SPDX-License-Identifier: AGPL-3.0-or-later

import assert from 'node:assert/strict'
import { test } from 'node:test'
import { scanArtifacts, type PreviewCandidate } from './auto-preview.ts'

function task(id: string, hash?: string, state = 'done'): PreviewCandidate {
  return hash ? { id, state, plan_meta: { output_artifact: hash } } : { id, state }
}

test('the first scan only establishes the baseline', () => {
  const scan = scanArtifacts(null, [task('a', 'h1'), task('b', 'h2')])
  assert.equal(scan.next, null)
  assert.deepEqual([...scan.seen].sort(), ['a:h1', 'b:h2'])
})

test('an artifact that was not in the previous snapshot opens once', () => {
  const first = scanArtifacts(null, [task('a'), task('b')])
  const second = scanArtifacts(first.seen, [task('a', 'h1'), task('b')])
  assert.equal(second.next, 'a')
  const third = scanArtifacts(second.seen, [task('a', 'h1'), task('b')])
  assert.equal(third.next, null)
})

test('a regenerated site counts again because its hash is new', () => {
  const first = scanArtifacts(null, [task('a', 'old')])
  const second = scanArtifacts(first.seen, [task('a', 'new')])
  assert.equal(second.next, 'a')
  const third = scanArtifacts(second.seen, [task('a', 'new')])
  assert.equal(third.next, null)
})

test('several pages in one poll open the newest, and all are marked seen', () => {
  const first = scanArtifacts(null, [])
  // The task read is newest-first: b finished after a.
  const second = scanArtifacts(first.seen, [task('b', 'h2'), task('a', 'h1')])
  assert.equal(second.next, 'b')
  const third = scanArtifacts(second.seen, [task('b', 'h2')])
  assert.equal(third.next, null)
})

test('a task that falls out of the window is not new when it returns', () => {
  const first = scanArtifacts(null, [task('a', 'h1')])
  const gone = scanArtifacts(first.seen, [])
  assert.equal(gone.next, null)
  const back = scanArtifacts(gone.seen, [task('a', 'h1')])
  assert.equal(back.next, null)
})

test('an unfinished row is left alone until it settles', () => {
  const first = scanArtifacts(null, [])
  const running = scanArtifacts(first.seen, [task('a', 'h1', 'running')])
  assert.equal(running.next, null)
  const done = scanArtifacts(running.seen, [task('a', 'h1', 'done')])
  assert.equal(done.next, 'a')
})

test('a failed row with a partial artifact does not open', () => {
  const first = scanArtifacts(null, [])
  const scan = scanArtifacts(first.seen, [task('a', 'h1', 'failed')])
  assert.equal(scan.next, null)
})

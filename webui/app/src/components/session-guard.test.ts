// Tests for the session-stream race guard (see session-guard.ts).

import assert from 'node:assert/strict'
import { test } from 'node:test'
import { isLiveSession } from './session-guard.ts'

test('writes land while their session is still on screen', () => {
  assert.equal(isLiveSession('s1', 's1'), true)
})

test('writes are dropped once the user switched threads', () => {
  assert.equal(isLiveSession('s1', 's2'), false)
})

test('writes are dropped when the pane went back to the new-chat hero', () => {
  assert.equal(isLiveSession('s1', ''), false)
  assert.equal(isLiveSession('s1', null), false)
  assert.equal(isLiveSession('s1', undefined), false)
})

test('an unaddressed write never matches anything', () => {
  assert.equal(isLiveSession(null, 's1'), false)
  assert.equal(isLiveSession(undefined, 's1'), false)
  assert.equal(isLiveSession('', 's1'), false)
  assert.equal(isLiveSession(null, null), false)
})

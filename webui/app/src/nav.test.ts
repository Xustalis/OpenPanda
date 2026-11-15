// SPDX-License-Identifier: AGPL-3.0-or-later
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { navigate, parseHash } from './nav.ts'

test('business details retain full account-scoped keys and execution bookmarks', () => {
  const previous = Object.getOwnPropertyDescriptor(globalThis, 'location')
  Object.defineProperty(globalThis, 'location', { configurable: true, value: { hash: '' } })
  try {
    navigate({ view: 'work', key: 'instance/workspace/space/task', connection: 'device-account' })
    assert.deepEqual(parseHash(), { view: 'work', key: 'instance/workspace/space/task', connection: 'device-account' })
    for (const hash of ['#/todo', '#/todos']) {
      location.hash = hash
      assert.deepEqual(parseHash(), { view: 'team', tab: 'mine' })
    }
    location.hash = '#/task/kernel-id'
    assert.deepEqual(parseHash(), { view: 'detail', id: 'kernel-id' })
    location.hash = '#/team'
    assert.deepEqual(parseHash(), { view: 'team', tab: 'spaces' })
  } finally {
    if (previous) Object.defineProperty(globalThis, 'location', previous)
    else Reflect.deleteProperty(globalThis, 'location')
  }
})

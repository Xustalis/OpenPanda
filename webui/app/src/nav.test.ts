// SPDX-License-Identifier: AGPL-3.0-or-later
import assert from 'node:assert/strict'
import { afterEach, beforeEach, test } from 'node:test'
import { navigate, parseHash, visibleNavViews } from './nav.ts'
import { setTeamFeatureEnabled } from './features.ts'

let previousStorage: PropertyDescriptor | undefined
let previousLocation: PropertyDescriptor | undefined
beforeEach(() => {
  previousStorage = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  previousLocation = Object.getOwnPropertyDescriptor(globalThis, 'location')
  const values = new Map<string, string>()
  Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
  } })
  Object.defineProperty(globalThis, 'location', { configurable: true, value: { hash: '' } })
  setTeamFeatureEnabled(false)
})
afterEach(() => {
  setTeamFeatureEnabled(false)
  if (previousStorage) Object.defineProperty(globalThis, 'localStorage', previousStorage)
  else Reflect.deleteProperty(globalThis, 'localStorage')
  if (previousLocation) Object.defineProperty(globalThis, 'location', previousLocation)
  else Reflect.deleteProperty(globalThis, 'location')
})

test('disabled collaboration bookmarks open its settings while execution tasks stay available', () => {
  for (const hash of [
    '#/team', '#/team?tab=mine', '#/team?tab=review', '#/work', '#/work/tasks',
    '#/work/tasks/instance%2Fworkspace%2Fspace%2Ftask?connection=account', '#/todo', '#/todos',
  ]) {
    location.hash = hash
    assert.deepEqual(parseHash(), { view: 'settings', tab: 'team' }, hash)
  }
  location.hash = '#/task/kernel-id'
  assert.deepEqual(parseHash(), { view: 'detail', id: 'kernel-id' })
  location.hash = '#/control'
  assert.equal(parseHash().view, 'control')
})

test('navigation and routes immediately follow both directions of the opt-in', () => {
  assert.equal(visibleNavViews().some(([view]) => view === 'team'), false)
  assert.equal(visibleNavViews().some(([view]) => view === 'settings'), true)
  setTeamFeatureEnabled(true)
  assert.equal(visibleNavViews().some(([view]) => view === 'team'), true)
  location.hash = '#/team?tab=review'
  assert.deepEqual(parseHash(), { view: 'team', tab: 'review' })
  setTeamFeatureEnabled(false)
  assert.equal(visibleNavViews().some(([view]) => view === 'team'), false)
  assert.deepEqual(parseHash(), { view: 'settings', tab: 'team' })
})

test('business details retain full account-scoped keys and execution bookmarks', () => {
  setTeamFeatureEnabled(true)
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
})

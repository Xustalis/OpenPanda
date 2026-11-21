// SPDX-License-Identifier: AGPL-3.0-or-later

import assert from 'node:assert/strict'
import { afterEach, beforeEach, test } from 'node:test'
import { onTeamFeatureChange, setTeamFeatureEnabled, teamFeatureEnabled, TEAM_FEATURE_KEY } from './features.ts'

let values: Map<string, string>
let events: EventTarget
let previousStorage: PropertyDescriptor | undefined
let previousWindow: PropertyDescriptor | undefined

beforeEach(() => {
  previousStorage = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  previousWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  values = new Map()
  events = new EventTarget()
  Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
  } })
  Object.defineProperty(globalThis, 'window', { configurable: true, value: events })
  setTeamFeatureEnabled(false)
  values.clear()
})

afterEach(() => {
  // Clear a session-only override even when the test blocked storage writes.
  Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: { setItem() {} } })
  setTeamFeatureEnabled(false)
  if (previousStorage) Object.defineProperty(globalThis, 'localStorage', previousStorage)
  else Reflect.deleteProperty(globalThis, 'localStorage')
  if (previousWindow) Object.defineProperty(globalThis, 'window', previousWindow)
  else Reflect.deleteProperty(globalThis, 'window')
})

test('a new browser shows the board and honors an explicit opt-out', () => {
  values.set('openpanda.team', 'true')
  assert.equal(teamFeatureEnabled(), true)
  for (const value of ['', 'false', '1', 'yes', 'garbage']) {
    values.set(TEAM_FEATURE_KEY, value)
    assert.equal(teamFeatureEnabled(), false)
  }
  values.set(TEAM_FEATURE_KEY, 'true')
  assert.equal(teamFeatureEnabled(), true)
})

test('enabling and disabling persist and update same-page subscribers', () => {
  const seen: boolean[] = []
  const stop = onTeamFeatureChange(() => seen.push(teamFeatureEnabled()))
  try {
    setTeamFeatureEnabled(true)
    assert.equal(values.get(TEAM_FEATURE_KEY), 'true')
    assert.equal(teamFeatureEnabled(), true)
    setTeamFeatureEnabled(false)
    assert.equal(values.get(TEAM_FEATURE_KEY), 'false')
    assert.deepEqual(seen, [true, false])
  } finally { stop() }
  setTeamFeatureEnabled(true)
  assert.deepEqual(seen, [true, false])
})

test('other tabs and clearing storage update subscribers; unrelated keys do not', () => {
  const seen: boolean[] = []
  const stop = onTeamFeatureChange(() => seen.push(teamFeatureEnabled()))
  const storageEvent = (key: string | null) => events.dispatchEvent(Object.assign(new Event('storage'), { key }))
  try {
    values.set(TEAM_FEATURE_KEY, 'true')
    storageEvent('openpanda.theme')
    assert.deepEqual(seen, [])
    storageEvent(TEAM_FEATURE_KEY)
    values.clear()
    storageEvent(null)
    assert.deepEqual(seen, [true, true])
  } finally { stop() }
  storageEvent(TEAM_FEATURE_KEY)
  assert.deepEqual(seen, [true, true])
})

test('unavailable storage defaults on and permits toggling for the current page', () => {
  Object.defineProperty(globalThis, 'localStorage', { configurable: true, get() { throw new Error('blocked') } })
  assert.equal(teamFeatureEnabled(), true)
  setTeamFeatureEnabled(true)
  assert.equal(teamFeatureEnabled(), true)
  setTeamFeatureEnabled(false)
  assert.equal(teamFeatureEnabled(), false)
})

test('a failed write still changes the switch when an old stored value is readable', () => {
  Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: {
    getItem: () => 'false',
    setItem() { throw new Error('quota exceeded') },
  } })
  setTeamFeatureEnabled(true)
  assert.equal(teamFeatureEnabled(), true)
  setTeamFeatureEnabled(false)
  assert.equal(teamFeatureEnabled(), false)
})

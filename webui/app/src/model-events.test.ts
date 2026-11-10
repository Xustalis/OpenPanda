// SPDX-License-Identifier: AGPL-3.0-or-later

import assert from 'node:assert/strict'
import { test } from 'node:test'
import { MODEL_SAVED_EVENT, notifyModelSaved, onModelSaved } from './model-events.ts'

test('a registry write reaches every subscriber until it unsubscribes', () => {
  ;(globalThis as any).window = new EventTarget()
  try {
    let a = 0
    let b = 0
    const stopA = onModelSaved(() => a++)
    const stopB = onModelSaved(() => b++)

    notifyModelSaved()
    assert.equal(a, 1)
    assert.equal(b, 1)

    stopA()
    notifyModelSaved()
    assert.equal(a, 1, 'an unsubscribed listener must not be called again')
    assert.equal(b, 2)

    stopB()
    notifyModelSaved()
    assert.equal(b, 2)
  } finally {
    delete (globalThis as any).window
  }
})

test('the announcement is the named event, with no payload to parse', () => {
  ;(globalThis as any).window = new EventTarget()
  try {
    const seen: string[] = []
    ;(globalThis as any).window.addEventListener(MODEL_SAVED_EVENT, (e: Event) => seen.push(e.type))
    notifyModelSaved()
    assert.deepEqual(seen, [MODEL_SAVED_EVENT])
  } finally {
    delete (globalThis as any).window
  }
})

test('without a window the module stays inert instead of throwing', () => {
  assert.doesNotThrow(() => notifyModelSaved())
  const stop = onModelSaved(() => {
    throw new Error('never subscribed without a window')
  })
  assert.equal(typeof stop, 'function')
  stop()
})

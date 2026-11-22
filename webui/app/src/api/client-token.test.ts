// SPDX-License-Identifier: AGPL-3.0-or-later

import assert from 'node:assert/strict'
import { test } from 'node:test'
import { clearToken, currentToken, isHeaderSafeToken, setToken } from './client.ts'

test('a token that can ride in an Authorization header is accepted', () => {
  assert.equal(isHeaderSafeToken('ff570522e63a1f691f824e76d2f5767afe3d1c4c67290b51'), true)
  assert.equal(isHeaderSafeToken('abc-123_XYZ.+/='), true)
})

test('a token with a non ISO-8859-1 code point is rejected', () => {
  // The browser refuses to build the request: "String contains non
  // ISO-8859-1 code point". One such token in storage breaks every fetch,
  // so both the store and the token gate have to refuse it up front.
  assert.equal(isHeaderSafeToken('令牌'), false)
  assert.equal(isHeaderSafeToken('abc\u3000def'), false)
  assert.equal(isHeaderSafeToken('abc\ndef'), false)
  // Latin-1 range but still not a token character: keep the check strict.
  assert.equal(isHeaderSafeToken('abcé'), false)
})

test('a stored invalid token is removed and a subsequent sign-in persists', (t) => {
  const values = new Map([['openpanda.token', '令牌']])
  const previous = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
    removeItem: (key: string) => values.delete(key),
  } })
  t.after(() => {
    clearToken()
    if (previous) Object.defineProperty(globalThis, 'localStorage', previous)
    else Reflect.deleteProperty(globalThis, 'localStorage')
  })
  assert.equal(currentToken(), '')
  assert.equal(values.size, 0)
  setToken('令牌')
  assert.equal(values.size, 0)
  setToken('valid-token')
  assert.equal(currentToken(), 'valid-token')
  assert.equal(values.get('openpanda.token'), 'valid-token')
})

// SPDX-License-Identifier: AGPL-3.0-or-later

import { test } from 'node:test'
import assert from 'node:assert/strict'
import { needsPlaintextConsent } from './model-endpoint.ts'

test('https never needs consent', () => {
  assert.equal(needsPlaintextConsent('https://api.deepseek.com/anthropic'), false)
  assert.equal(needsPlaintextConsent('HTTPS://relay.example.com/v1'), false)
})

test('loopback http stays exempt, in every spelling', () => {
  for (const url of [
    'http://localhost:8080/v1',
    'http://LOCALHOST/v1',
    'http://127.0.0.1:11434',
    'http://127.9.9.9:11434',
    'http://[::1]:11434/v1',
  ]) {
    assert.equal(needsPlaintextConsent(url), false, url)
  }
})

test('a plain-http relay anywhere else needs the opt-in', () => {
  assert.equal(needsPlaintextConsent('http://106.53.40.245:8080'), true)
  assert.equal(needsPlaintextConsent('  http://192.168.1.10:8080/v1  '), true)
  assert.equal(needsPlaintextConsent('http://relay.example.com'), true)
})

test('an empty or half-typed URL asks nothing', () => {
  assert.equal(needsPlaintextConsent(''), false)
  assert.equal(needsPlaintextConsent('http://'), false)
  assert.equal(needsPlaintextConsent('106.53.40.245:8080'), false)
})

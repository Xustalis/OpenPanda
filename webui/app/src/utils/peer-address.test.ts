// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it } from 'node:test'
import assert from 'node:assert/strict'

import { isPeerAddr } from './peer-address.ts'

describe('isPeerAddr', () => {
  it('accepts the daemon grammar', () => {
    for (const addr of [
      '192.168.1.20:7836',
      'peer.example.com:7836',
      '[fd7a:115e:a214::1]:7836',
      'ws://peer.example.com:7836/ws',
      'wss://peer.example.com',
      'punch:laptop-home',
    ]) {
      assert.equal(isPeerAddr(addr), true, addr)
    }
  })

  it('rejects values the daemon would reject', () => {
    for (const addr of [
      '',
      'no-port-here',
      'host:0',
      'host:70000',
      'host:abc',
      ':7836',
      'ftp://host:21',
      'ws://',
      'punch:',
      'two words:7836',
    ]) {
      assert.equal(isPeerAddr(addr), false, addr)
    }
  })

  it('ignores surrounding whitespace', () => {
    assert.equal(isPeerAddr('  192.168.1.20:7836  '), true)
  })
})

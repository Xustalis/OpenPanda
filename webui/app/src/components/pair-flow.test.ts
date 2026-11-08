// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it } from 'node:test'
import assert from 'node:assert/strict'

import {
  countdown,
  formatLeft,
  hostOf,
  ipSortKey,
  mergeCandidates,
  requestLeft,
} from './pair-flow.ts'

/** `rows[0]` under `noUncheckedIndexedAccess`; asserting here also fails the
 *  test at the right line when the list came back short. */
function first<T>(xs: T[]): T {
  const v = xs[0]
  assert.ok(v !== undefined, 'expected at least one row')
  return v
}

describe('hostOf', () => {
  it('splits host:port and leaves bracketed v6 alone', () => {
    assert.equal(hostOf('10.86.36.57:7836'), '10.86.36.57')
    assert.equal(hostOf('[fd7a:115e::1]:7836'), 'fd7a:115e::1')
    assert.equal(hostOf('peer.local:7836'), 'peer.local')
    assert.equal(hostOf('no-port'), 'no-port')
    assert.equal(hostOf('fd7a:115e::1'), 'fd7a:115e::1')
  })
})

describe('ipSortKey', () => {
  it('orders numerically, not lexically', () => {
    assert.ok(ipSortKey('192.168.1.9:7836') < ipSortKey('192.168.1.10:7836'))
  })

  it('pushes non-IPv4 hosts to the end', () => {
    const last = Number.MAX_SAFE_INTEGER
    assert.equal(ipSortKey('peer.local:7836'), last)
    assert.equal(ipSortKey('10.0.0.999:7836'), last)
  })
})

describe('mergeCandidates', () => {
  it('collapses one device seen by both sources into a single row', () => {
    const rows = mergeCandidates(
      [{ id: 'opi3b', addr: '10.86.36.57:7836', verified: true }],
      [{ addr: '10.86.36.57:7836' }],
    )
    assert.equal(rows.length, 1)
    assert.equal(first(rows).source, 'both')
    assert.equal(first(rows).target, 'opi3b', 'the beacon name is the dial target')
    assert.equal(first(rows).verified, true)
  })

  it('keeps the beacon row when the sweep found a different port on the same host', () => {
    const rows = mergeCandidates(
      [{ id: 'opi3b', addr: '10.86.36.57:7836', verified: true }],
      [{ addr: '10.86.36.57:9000' }],
    )
    assert.equal(rows.length, 1)
    assert.equal(first(rows).addr, '10.86.36.57:7836')
    assert.equal(first(rows).source, 'both')
  })

  it('orders by how much is known, then by address', () => {
    const rows = mergeCandidates(
      [
        { id: 'zeta', addr: '10.0.0.20:7836', verified: true },
        { id: 'alpha', addr: '10.0.0.3:7836', verified: false },
      ],
      [{ addr: '10.0.0.99:7836' }, { addr: '10.0.0.20:7836' }],
    )
    assert.deepEqual(
      rows.map((r) => r.source),
      ['both', 'beacon', 'scan'],
    )
    assert.deepEqual(
      rows.map((r) => r.addr),
      ['10.0.0.20:7836', '10.0.0.3:7836', '10.0.0.99:7836'],
    )
  })

  it('gives a sweep-only hit an empty name so the row falls back to its address', () => {
    const rows = mergeCandidates([], [{ addr: '10.0.0.99:7836' }])
    assert.equal(first(rows).name, '')
    assert.equal(first(rows).target, '10.0.0.99:7836')
    assert.equal(first(rows).verified, false)
  })

  it('returns an empty list for empty input', () => {
    assert.deepEqual(mergeCandidates([], []), [])
  })
})

describe('countdown', () => {
  const now = Date.parse('2026-11-08T23:40:00Z')

  it('renders mm:ss remaining', () => {
    assert.equal(countdown('2026-11-08T23:42:05Z', now), '2:05')
    assert.equal(countdown('2026-11-08T23:40:09Z', now), '0:09')
  })

  it('says nothing once the deadline has passed or is unusable', () => {
    assert.equal(countdown('2026-11-08T23:39:00Z', now), '')
    assert.equal(countdown('not-a-date', now), '')
    assert.equal(countdown(undefined, now), '')
  })
})

describe('requestLeft', () => {
  const arrived = 1_000_000

  it('counts from the server-reported window, not the local clock', () => {
    // A browser an hour behind still gets the daemon's answer: 120s reported,
    // 30s elapsed here.
    assert.equal(requestLeft({ expires_in: 120 }, arrived, arrived + 30_000), 90)
  })

  it('falls back to expires_at when the node predates expires_in', () => {
    const at = '2026-11-08T23:42:00Z'
    assert.equal(requestLeft({ expires_at: at }, arrived, Date.parse(at) - 45_000), 45)
  })

  it('goes non-positive at the deadline instead of counting up', () => {
    assert.equal(formatLeft(requestLeft({ expires_in: 5 }, arrived, arrived + 6_000)), '')
    assert.equal(formatLeft(0), '')
    assert.equal(formatLeft(-3), '')
  })
})

describe('formatLeft', () => {
  it('pads the seconds and keeps minutes unbounded', () => {
    assert.equal(formatLeft(9), '0:09')
    assert.equal(formatLeft(125), '2:05')
    assert.equal(formatLeft(605), '10:05')
  })
})

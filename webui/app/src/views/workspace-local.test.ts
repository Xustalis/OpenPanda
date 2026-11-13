// SPDX-License-Identifier: AGPL-3.0-or-later

import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { NodeInfo, SelfInfo } from '../api/client.ts'
import { isLocal } from './workspace-local.ts'

function self(extra: Partial<SelfInfo> = {}): SelfInfo {
  return {
    hostname: 'xin',
    os: 'windows',
    arch: 'amd64',
    cpu_cores: 8,
    node_running: true,
    node_name: 'xin',
    node_id: 'xin',
    node_kind: 'physical',
    node_identity: 'machine-b8eee8f3db5f921a',
    version: '0.1.6',
    ...extra,
  }
}

function row(extra: Partial<NodeInfo> = {}): NodeInfo {
  return {
    id: 'xin',
    name: 'xin',
    status: 'online',
    node_kind: 'physical',
    running: true,
    last_seen: '',
    scheduler_tier: 0,
    abilities: [],
    capacity: { cpu_cores: 8, ram_gb: 16, max_concurrent_tasks: 4, current_tasks: 0 },
    ...extra,
  }
}

test("the server's own verdict wins over every guess", () => {
  assert.equal(isLocal(self(), row({ id: 'somewhere-else', name: 'somewhere-else', is_local: true })), true)
})

test('a row filed under this runtime id is this machine', () => {
  assert.equal(isLocal(self(), row()), true)
  const vmSelf = self({ node_id: 'xin@vm-805eed2f664a', node_kind: 'vm', node_identity: 'verify-log' })
  const vmRow = row({ id: 'xin@vm-805eed2f664a', name: 'xin', node_kind: 'vm', node_identity: 'verify-log' })
  assert.equal(isLocal(vmSelf, vmRow), true)
})

test('a vm sibling that reuses the host name is not this machine', () => {
  // The regression: vm nodes share their host's display name on purpose, so
  // a name-only fallback lit up every leftover test node as 本机.
  const ghost = row({
    id: 'verify-log@vm-805eed2f664a',
    name: 'xin',
    node_kind: 'vm',
    node_identity: 'verify-log',
  })
  assert.equal(isLocal(self(), ghost), false)
  // A vm node that declared no identity at all: RuntimeNodeID spells it
  // `name@vm`, and the kind check is the only thing left to catch it.
  assert.equal(isLocal(self(), row({ id: 'xin@vm', node_kind: 'vm' })), false)
})

test('a peer with its own name is not this machine', () => {
  assert.equal(isLocal(self(), row({ id: 'DESKTOP-SLH9T00', name: 'DESKTOP-SLH9T00' })), false)
})

test('a legacy row under the bare name still resolves while identity is unknown', () => {
  // An older build filed the row under just the name, and /api/self did not
  // carry a kind yet — the map still has to find itself.
  const legacySelf = self({ node_id: undefined, node_kind: undefined, node_identity: undefined })
  assert.equal(isLocal(legacySelf, row({ node_kind: '', node_identity: '' })), true)
})

test('without a self profile nothing is local', () => {
  assert.equal(isLocal(null, row()), false)
})

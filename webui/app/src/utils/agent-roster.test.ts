// SPDX-License-Identifier: AGPL-3.0-or-later

import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { AgentInfo } from '../api/client.ts'
import { createAgentRosterLoader } from './agent-roster.ts'

const agent: AgentInfo = { name: 'claude_code', binary: 'claude', installed: true }

test('agent roster shares concurrent and subsequent reads', async () => {
  let calls = 0
  let resolve!: (agents: AgentInfo[]) => void
  const load = createAgentRosterLoader(() => {
    calls++
    return new Promise<AgentInfo[]>((res) => {
      resolve = res
    })
  })
  const first = load()
  const concurrent = load()
  assert.strictEqual(concurrent, first)
  resolve([agent])
  assert.deepEqual(await first, [agent])
  assert.deepEqual(await concurrent, [agent])
  assert.strictEqual(load(), first)
  assert.equal(calls, 1)
})

test('agent roster retries after a failed request', async () => {
  let calls = 0
  const load = createAgentRosterLoader(() => {
    calls++
    return calls === 1 ? Promise.reject(new Error('offline')) : Promise.resolve([agent])
  })
  await assert.rejects(load(), /offline/)
  assert.deepEqual(await load(), [agent])
  assert.equal(calls, 2)
})

test('agent roster force refresh bypasses the cached value', async () => {
  const refreshes: Array<boolean | undefined> = []
  let version = 0
  const load = createAgentRosterLoader((refresh) => {
    refreshes.push(refresh)
    version++
    return Promise.resolve([{ ...agent, version: `v${version}` }])
  })
  assert.equal((await load())[0]?.version, 'v1')
  assert.equal((await load())[0]?.version, 'v1')
  assert.equal((await load(true))[0]?.version, 'v2')
  assert.deepEqual(refreshes, [false, true])
})

test('an older failure cannot clear a newer refresh', async () => {
  let calls = 0
  let rejectFirst!: (err: Error) => void
  let resolveSecond!: (agents: AgentInfo[]) => void
  const load = createAgentRosterLoader(() => {
    calls++
    if (calls === 1) {
      return new Promise<AgentInfo[]>((_resolve, reject) => {
        rejectFirst = reject
      })
    }
    return new Promise<AgentInfo[]>((resolve) => {
      resolveSecond = resolve
    })
  })
  const first = load()
  const refreshed = load(true)
  rejectFirst(new Error('stale failure'))
  await assert.rejects(first, /stale failure/)
  assert.strictEqual(load(), refreshed)
  resolveSecond([agent])
  assert.deepEqual(await refreshed, [agent])
  assert.equal(calls, 2)
})

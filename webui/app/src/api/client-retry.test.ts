// SPDX-License-Identifier: AGPL-3.0-or-later

import assert from 'node:assert/strict'
import { test } from 'node:test'
import { api, ApiError } from './client.ts'

test('GET request retries on transient failure and succeeds', async () => {
  const originalFetch = globalThis.fetch
  let callCount = 0

  globalThis.fetch = (async (_url: any, _init?: any) => {
    callCount++
    if (callCount === 1) {
      throw new Error('Failed to fetch')
    }
    return new Response(JSON.stringify({ status: 'ok', database: 'ok', uptime: '10s', version: 'v1', timestamp: '' }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as any

  try {
    const health = await api.health()
    assert.equal(health.status, 'ok')
    assert.equal(callCount, 2, 'should have retried and succeeded on attempt 2')
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('GET request does not retry 4xx errors (fails fast)', async () => {
  const originalFetch = globalThis.fetch
  let callCount = 0

  globalThis.fetch = (async (_url: any, _init?: any) => {
    callCount++
    return new Response('Not found', { status: 404 })
  }) as any

  try {
    await assert.rejects(
      async () => {
        await api.health()
      },
      (err: any) => {
        assert(err instanceof ApiError)
        assert.equal(err.status, 404)
        return true
      }
    )
    assert.equal(callCount, 1, 'should not retry 404 error')
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('POST request does not retry on failure', async () => {
  const originalFetch = globalThis.fetch
  let callCount = 0

  globalThis.fetch = (async (_url: any, _init?: any) => {
    callCount++
    return new Response('Internal error', { status: 500 })
  }) as any

  try {
    await assert.rejects(
      async () => {
        await api.createTask({ title: 'test task' })
      },
      (err: any) => {
        assert(err instanceof ApiError)
        assert.equal(err.status, 500)
        return true
      }
    )
    assert.equal(callCount, 1, 'POST should never retry')
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('manual task retry surfaces a detached refusal instead of reporting success', async () => {
  const originalFetch = globalThis.fetch
  let posts = 0
  globalThis.fetch = (async (_url: any, init?: any) => {
    if (init?.method === 'POST') posts++
    const body = init?.method === 'POST'
      ? { operation_id: 'retry-op', task_id: 'failed-task', status: 'running' }
      : { operation_id: 'retry-op', task_id: 'failed-task', status: 'review', error: 'retry belongs to executor peer-A' }
    return new Response(JSON.stringify(body), { status: init?.method === 'POST' ? 202 : 200 })
  }) as any
  try {
    await assert.rejects(api.retryTask('failed-task'), /retry belongs to executor peer-A/)
    assert.equal(posts, 1, 'manual retry must never repeat its mutation')
  } finally { globalThis.fetch = originalFetch }
})

test('manual task retry returns after execution starts and only sends one POST', async () => {
  const originalFetch = globalThis.fetch
  let posts = 0
  globalThis.fetch = (async (url: any, init?: any) => {
    if (init?.method === 'POST') posts++
    const body = String(url).includes('/operations/') || init?.method === 'POST'
      ? { operation_id: 'retry-op', task_id: 'failed-task', status: 'running' }
      : { id: 'failed-task', state: 'running' }
    return new Response(JSON.stringify(body), { status: init?.method === 'POST' ? 202 : 200 })
  }) as any
  try {
    const op = await api.retryTask('failed-task')
    assert.equal(op.status, 'running')
    assert.equal(posts, 1)
  } finally { globalThis.fetch = originalFetch }
})

test('a completed retry may carry stderr warnings without becoming an error', async () => {
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (_url: any, init?: any) => new Response(JSON.stringify({
    operation_id: 'retry-op', task_id: 'failed-task',
    status: init?.method === 'POST' ? 'running' : 'done',
    error: init?.method === 'POST' ? undefined : 'compiler warning',
  }), { status: init?.method === 'POST' ? 202 : 200 })) as any
  try { assert.equal((await api.retryTask('failed-task')).status, 'done') }
  finally { globalThis.fetch = originalFetch }
})

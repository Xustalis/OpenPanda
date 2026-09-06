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
